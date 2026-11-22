package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *app) worker(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for i := 0; i < 50; i++ {
			if !a.deliverOne(ctx) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (a *app) deliverOne(ctx context.Context) bool {
	tx, e := a.db.Begin(ctx)
	if e != nil {
		return false
	}
	defer tx.Rollback(ctx)
	settings, e := readBotSettings(ctx, tx, true)
	if e != nil {
		return false
	}
	b, e := scanBooking(tx.QueryRow(ctx, bookingSelect+`WHERE EXISTS(SELECT 1 FROM jobs j WHERE j.booking_id=b.id AND j.sent_at IS NULL AND j.due_at<=now() AND j.attempts<12) ORDER BY b.id LIMIT 1 FOR UPDATE OF b SKIP LOCKED`))
	if e != nil {
		return false
	}
	var id int64
	var version, attempts int
	var kind, ch string
	e = tx.QueryRow(ctx, "SELECT id,version,kind,channel,attempts FROM jobs WHERE booking_id=$1 AND sent_at IS NULL AND due_at<=now() AND attempts<12 ORDER BY due_at,id LIMIT 1 FOR UPDATE", b.ID).Scan(&id, &version, &kind, &ch, &attempts)
	if e != nil {
		return false
	}
	if !settings.allows(kind) || b.Version != version || (strings.HasPrefix(kind, "reminder") && (b.Status != "confirmed" || !b.StartsAt.After(time.Now()))) {
		_, e = tx.Exec(ctx, "DELETE FROM jobs WHERE id=$1", id)
	} else {
		title := "Ваша запись подтверждена"
		switch kind {
		case "cancellation":
			title = "Ваша запись отменена"
		case "rescheduled":
			title = "Ваша запись перенесена"
		case "reminder_24h":
			title = "Напоминаем о завтрашнем визите"
		case "reminder_2h":
			title = "До вашего визита осталось 2 часа"
		}
		reply := a.bookingReply(b)
		body := title + "\n\n" + reply.Text
		if settings.ContactText != "" {
			body += "\n\n" + settings.ContactText
		}
		var sendErr error
		if ch == "telegram" {
			if b.TelegramChat != nil {
				sendErr = a.sendBotReply(ctx, *b.TelegramChat, botReply{Text: body, Buttons: reply.Buttons})
			} else {
				sendErr = fmt.Errorf("telegram not linked")
			}
		} else {
			sendErr = fmt.Errorf("unsupported notification channel")
		}
		if sendErr == nil {
			_, e = tx.Exec(ctx, "UPDATE jobs SET sent_at=now(),last_error=NULL WHERE id=$1", id)
		} else {
			slog.Warn("notification delivery failed", "job", id, "channel", ch)
			delay := time.Minute * time.Duration(1<<min(attempts, 8))
			_, e = tx.Exec(ctx, "UPDATE jobs SET attempts=attempts+1,last_error=$2,due_at=$3 WHERE id=$1", id, "Ошибка доставки: проверьте настройки провайдера", time.Now().Add(delay))
		}
	}
	if e != nil {
		return false
	}
	return tx.Commit(ctx) == nil
}
func (a *app) telegram(ctx context.Context, method string, payload any, out any) error {
	raw, _ := json.Marshal(payload)
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+os.Getenv("TELEGRAM_BOT_TOKEN")+"/"+method, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	client := a.telegramClient
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}
	res, e := client.Do(req)
	if e != nil {
		return fmt.Errorf("telegram unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("telegram HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}
func (a *app) sendTelegram(ctx context.Context, chat int64, text string) error {
	var res struct {
		OK bool `json:"ok"`
	}
	e := a.telegram(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}, &res)
	if e == nil && !res.OK {
		return fmt.Errorf("telegram rejected message")
	}
	return e
}
func (a *app) bot(ctx context.Context) {
	for ctx.Err() == nil {
		if e := a.resolveAndPollBot(ctx); e != nil {
			slog.Warn("telegram polling retry")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}
func (a *app) pollBot(ctx context.Context) error {
	conn, e := a.db.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(814730)").Scan(&locked); e != nil {
		return e
	}
	if !locked {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
			return nil
		}
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(814730)")
	var offset int64
	if e = conn.QueryRow(ctx, "SELECT offset_id FROM bot_state WHERE id=1").Scan(&offset); e != nil {
		return e
	}
	var updates struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	e = a.telegram(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"}}, &updates)
	if e != nil {
		return e
	}
	if !updates.OK {
		return fmt.Errorf("telegram rejected updates")
	}
	for _, u := range updates.Result {
		chat, _, _, valid := u.identity()
		if u.Callback != nil {
			var result any
			// Acknowledge even expired buttons so the Telegram spinner stops.
			_ = a.telegram(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": u.Callback.ID}, &result)
		}
		if valid {
			reply, err := a.processBotUpdate(ctx, u)
			if err != nil {
				return err
			}
			if err = a.sendBotReply(ctx, chat, reply); err != nil {
				// A blocked chat must not prevent updates from other guests.
				slog.Warn("telegram reply failed", "update", u.ID)
			}
		}
		if _, e = conn.Exec(ctx, "UPDATE bot_state SET offset_id=$1 WHERE id=1", u.ID+1); e != nil {
			return e
		}
	}
	// Telegram retains updates for at most 24h; retain receipts for a full week.
	_, e = conn.Exec(ctx, "DELETE FROM bot_updates WHERE created_at<now()-interval '7 days'")
	if e != nil {
		return e
	}
	_, e = conn.Exec(ctx, "DELETE FROM bot_sessions WHERE updated_at<now()-interval '30 days'")
	if e != nil {
		return e
	}

	return nil
}

// Leave room below Telegram's 4096-character limit, including UTF-16 pairs.
func telegramChunks(text string) []string {
	r := []rune(text)
	chunks := []string{}
	for len(r) > 0 {
		n := min(len(r), 2000)
		chunks = append(chunks, string(r[:n]))
		r = r[n:]
	}
	return chunks
}

func (a *app) botUsername() string {
	if name := a.botName.Load(); name != nil {
		return name.(string)
	}
	return ""
}
func (a *app) resolveBot(ctx context.Context) error {
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := a.telegram(ctx, "getMe", map[string]any{}, &result); err != nil {
		return err
	}
	if !result.OK || result.Result.Username == "" {
		return fmt.Errorf("invalid bot identity")
	}
	a.botName.Store(result.Result.Username)
	return nil
}
func (a *app) resolveAndPollBot(ctx context.Context) error {
	if a.botUsername() == "" {
		if err := a.resolveBot(ctx); err != nil {
			return err
		}
		commands := []map[string]string{
			{"command": "start", "description": "Главное меню Elyanora"},
			{"command": "book", "description": "Записаться на массаж"},
			{"command": "bookings", "description": "Мои записи, перенос и отмена"},
			{"command": "services", "description": "Услуги и цены"},
			{"command": "menu", "description": "Начать заново / главное меню"},
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if err := a.telegram(ctx, "setMyCommands", map[string]any{"commands": commands}, &result); err != nil || !result.OK {
			a.botName.Store("")
			return fmt.Errorf("telegram command setup failed")
		}
	}
	return a.pollBot(ctx)
}
func (a *app) linkTelegram(ctx context.Context, token string, chat int64) (booking, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return booking{}, err
	}
	defer tx.Rollback(ctx)
	b, err := scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.token=$1 AND b.status='confirmed' AND b.starts_at>now() FOR UPDATE OF b", token))
	if err != nil {
		return b, err
	}
	if b.TelegramChat != nil {
		if *b.TelegramChat != chat {
			return booking{}, pgx.ErrNoRows
		}
		return b, nil
	}
	if _, err = tx.Exec(ctx, "UPDATE bookings SET telegram_chat=$2 WHERE id=$1", b.ID, chat); err != nil {
		return b, err
	}
	b.TelegramChat = &chat
	if err = enqueue(ctx, tx, b, "confirmation"); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

func botWelcome(settings botSettings, base string) string {
	text := settings.WelcomeMessage
	if settings.ServicesEnabled {
		text += "\n\n/services — услуги и цены"
	}
	text += "\n\nНажмите «Записаться» — выберите услугу, мастера и удобное время прямо здесь."
	if settings.ContactText != "" {
		text += "\n\n" + settings.ContactText
	}
	return text
}

func (a *app) sendBotReply(ctx context.Context, chat int64, r botReply) error {
	if r.RemoveKeyboard {
		var result struct {
			OK bool `json:"ok"`
		}
		if e := a.telegram(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": "Продолжаем.", "reply_markup": map[string]bool{"remove_keyboard": true}}, &result); e != nil {
			return e
		}
	}
	chunks := telegramChunks(r.Text)
	for i, text := range chunks {
		payload := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
		if i == len(chunks)-1 {
			if r.Contact {
				payload["reply_markup"] = map[string]any{"keyboard": [][]map[string]any{{{"text": "Отправить мой телефон", "request_contact": true}}, {{"text": "Главное меню"}}}, "resize_keyboard": true, "one_time_keyboard": true}
			} else if len(r.Buttons) > 0 {
				payload["reply_markup"] = map[string]any{"inline_keyboard": r.Buttons}
			}
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if e := a.telegram(ctx, "sendMessage", payload, &result); e != nil {
			return e
		}
		if !result.OK {
			return fmt.Errorf("telegram rejected reply")
		}
	}
	return nil
}
