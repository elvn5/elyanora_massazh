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
		body := fmt.Sprintf("%s\n\n%s, %s\nМастер: %s\n%s (%s)\nПродолжительность: %d мин\nСтоимость: %d %s\n\nУправление записью (перенос и отмена):\n%s/?booking=%s\n\nСохраните эту персональную ссылку и не передавайте её другим.", title, b.Name, b.Service, b.Specialist, b.StartsAt.In(a.tz).Format("02.01.2006 в 15:04"), a.tz, int(b.EndsAt.Sub(b.StartsAt).Minutes()), b.Total, env("CURRENCY", "KGS"), a.base, b.Token)
		if settings.ContactText != "" {
			body += "\n\n" + settings.ContactText
		}
		var sendErr error
		if ch == "telegram" {
			if b.TelegramChat != nil {
				sendErr = a.sendTelegram(ctx, *b.TelegramChat, body)
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
		OK     bool `json:"ok"`
		Result []struct {
			ID      int64 `json:"update_id"`
			Message struct {
				Text string `json:"text"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		} `json:"result"`
	}
	e = a.telegram(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message"}}, &updates)
	if e != nil {
		return e
	}
	settings, err := readBotSettings(ctx, a.db, false)
	if err != nil {
		return err
	}
	for _, u := range updates.Result {
		if u.Message.Chat.Type == "private" {
			parts := strings.Fields(u.Message.Text)
			text := botWelcome(settings, a.base)
			if len(parts) == 2 && parts[0] == "/start" && validToken(parts[1]) {
				text = "Ссылка недействительна или запись уже закрыта."
				b, err := a.linkTelegram(ctx, parts[1], u.Message.Chat.ID)
				if err == nil {
					text = "Напоминания подключены.\nУправление записью: " + a.base + "/?booking=" + b.Token
				}
				if err != nil && err != pgx.ErrNoRows {
					return err
				}
			} else if len(parts) > 0 && parts[0] == "/services" && !settings.ServicesEnabled {
				text = "Каталог в боте временно скрыт. Услуги и запись доступны на сайте: " + a.base
			} else if len(parts) > 0 && parts[0] == "/services" {
				rows, err := a.db.Query(ctx, "SELECT name,prices FROM services WHERE active ORDER BY id")
				if err != nil {
					return err
				}
				text = "Услуги студии Elyanora:\n"
				for rows.Next() {
					var name string
					var prices map[string]int
					if err = rows.Scan(&name, &prices); err != nil {
						rows.Close()
						return err
					}
					text += "\n" + name
					for _, d := range []string{"60", "90", "120"} {
						if p, ok := prices[d]; ok {
							text += fmt.Sprintf("\n%s мин — %d %s", d, p, env("CURRENCY", "KGS"))
						}
					}
					text += "\n"
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
				text += "\nЗапись и свободные окна: " + a.base
			}
			// A blocked chat must not prevent all other users from receiving bot replies.
			for _, chunk := range telegramChunks(text) {
				if e = a.sendTelegram(ctx, u.Message.Chat.ID, chunk); e != nil {
					slog.Warn("telegram reply failed", "update", u.ID)
					break
				}
			}
		}
		if _, e = conn.Exec(ctx, "UPDATE bot_state SET offset_id=$1 WHERE id=1", u.ID+1); e != nil {
			return e
		}
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
	text += "\n\nЗаписаться: " + base
	if settings.ContactText != "" {
		text += "\n\n" + settings.ContactText
	}
	return text
}
