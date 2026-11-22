package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var botTestUpdate atomic.Int64

func botEvent(chat int64, text, data string) telegramUpdate {
	id := botTestUpdate.Add(1)
	from := map[string]any{"id": chat, "first_name": "Гость"}
	msg := map[string]any{"text": text, "from": from, "chat": map[string]any{"id": chat, "type": "private"}}
	raw := map[string]any{"update_id": id, "message": msg}
	if data != "" {
		raw = map[string]any{"update_id": id, "callback_query": map[string]any{"id": fmt.Sprint(id), "from": from, "data": data, "message": msg}}
	}
	b, _ := json.Marshal(raw)
	var u telegramUpdate
	_ = json.Unmarshal(b, &u)
	return u
}

type botDriver struct {
	t    *testing.T
	a    *app
	chat int64
	r    botReply
}

func (d *botDriver) event(u telegramUpdate) {
	d.t.Helper()
	r, e := d.a.processBotUpdate(context.Background(), u)
	if e != nil {
		d.t.Fatal(e)
	}
	d.r = r
	for _, row := range r.Buttons {
		for _, b := range row {
			if len(b.Data) > 64 {
				d.t.Fatalf("oversized callback: %s", b.Data)
			}
		}
	}
}
func (d *botDriver) say(s string) { d.event(botEvent(d.chat, s, "")) }
func (d *botDriver) key(prefix string) string {
	d.t.Helper()
	for _, row := range d.r.Buttons {
		for _, b := range row {
			if strings.HasPrefix(b.Text, prefix) {
				return b.Data
			}
		}
	}
	d.t.Fatalf("button %q missing from %+v", prefix, d.r)
	return ""
}
func (d *botDriver) click(prefix string) { d.t.Helper(); d.event(botEvent(d.chat, "", d.key(prefix))) }
func (d *botDriver) prepare(start time.Time) {
	d.say("/start")
	d.click("Записаться")
	d.click("Классический")
	d.click("60 мин")
	d.click("Продолжить")
	d.click("Анна")
	d.click(start.Format("02.01.2006"))
	d.click(start.Format("15:04"))
	d.click("Использовать имя")
	d.say("+996700000000")
	if !strings.Contains(d.r.Text, "соглашаетесь") {
		d.t.Fatal("consent not shown")
	}
}
func TestBotBookingMoveCancelAndReplay(t *testing.T) {
	a, _, start := fixture(t)
	d := botDriver{t: t, a: a, chat: 1101}
	d.prepare(start)
	// Restore in another app instance to prove the wizard does not depend on memory.
	d.a = &app{db: a.db, tz: a.tz, base: a.base, limiter: a.limiter}
	confirm := botEvent(d.chat, "", d.key("Подтвердить"))
	d.event(confirm)
	d.event(confirm)
	d.event(botEvent(d.chat, "", confirm.Callback.Data))
	var count int
	var b booking
	e := a.db.QueryRow(context.Background(), "SELECT count(*) FROM bookings").Scan(&count)
	if e != nil || count != 1 {
		t.Fatalf("duplicate booking count=%d err=%v", count, e)
	}
	b, e = scanBooking(a.db.QueryRow(context.Background(), bookingSelect+"WHERE b.telegram_chat=$1", d.chat))
	if e != nil {
		t.Fatal(e)
	}
	if b.TelegramChat == nil || *b.TelegramChat != d.chat {
		t.Fatal("chat not linked")
	}
	if e = a.db.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE booking_id=$1", b.ID).Scan(&count); e != nil || count != 3 {
		t.Fatal("reminders missing", count, e)
	}
	d.say("/bookings")
	d.click(start.Format("02.01 15:04"))
	oldCancel := d.key("Отменить запись")
	d.click("Перенести")
	d.click(start.Format("02.01.2006"))
	d.click("11:00")
	d.click("Подтвердить")
	b, e = scanBooking(a.db.QueryRow(context.Background(), bookingSelect+"WHERE b.id=$1", b.ID))
	if e != nil || b.Version != 2 || b.StartsAt.In(a.tz).Hour() != 11 {
		t.Fatalf("reschedule failed %+v %v", b, e)
	}
	d.event(botEvent(d.chat, "", oldCancel))
	if !strings.Contains(d.r.Text, "изменилась") {
		t.Fatal("old cancellation accepted")
	}
	other := botDriver{t: t, a: a, chat: 1102}
	other.event(botEvent(other.chat, "", fmt.Sprintf("view:%d", b.ID)))
	if strings.Contains(other.r.Text, b.Service) {
		t.Fatal("booking leaked to another chat")
	}
	other.event(botEvent(other.chat, "", fmt.Sprintf("cancel:%d:2", b.ID)))
	if !strings.Contains(other.r.Text, "изменилась") {
		t.Fatal("foreign cancellation offered")
	}
	d.say("/bookings")
	d.click(start.Format("02.01") + " 11:00")
	d.click("Отменить запись")
	cancel := botEvent(d.chat, "", d.key("Да, отменить"))
	d.event(cancel)
	d.event(cancel)
	var status string
	if e = a.db.QueryRow(context.Background(), "SELECT status FROM bookings WHERE id=$1", b.ID).Scan(&status); e != nil || status != "cancelled" {
		t.Fatal("cancel failed")
	}
	if e = a.db.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE booking_id=$1 AND sent_at IS NULL", b.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("stale reminders survived cancellation", count, e)
	}
}
func TestBotConcurrentConfirmationAndStaleButtons(t *testing.T) {
	a, _, start := fixture(t)
	d1 := botDriver{t: t, a: a, chat: 1201}
	d2 := botDriver{t: t, a: a, chat: 1202}
	d1.prepare(start)
	d2.prepare(start)
	us := []telegramUpdate{botEvent(d1.chat, "", d1.key("Подтвердить")), botEvent(d2.chat, "", d2.key("Подтвердить"))}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, u := range us {
		wg.Add(1)
		go func(u telegramUpdate) {
			defer wg.Done()
			_, e := a.processBotUpdate(context.Background(), u)
			errs <- e
		}(u)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := a.db.QueryRow(context.Background(), "SELECT count(*) FROM bookings").Scan(&count); e != nil || count != 1 {
		t.Fatal("overlap or lost booking", count, e)
	}
	d1.say("/book")
	old := d1.key("Классический")
	d1.click("Классический")
	d1.click("60 мин")
	d1.event(botEvent(d1.chat, "", old))
	if !strings.Contains(d1.r.Text, "устарела") {
		t.Fatal("stale wizard button accepted")
	}
}
func TestBotPricesExtrasContactAndHiddenCatalog(t *testing.T) {
	a, _, start := fixture(t)
	d := botDriver{t: t, a: a, chat: 1301}
	d.say("/book")
	d.click("Классический")
	d.click("60 мин")
	d.click("▫️ Массаж головы")
	d.click("Продолжить")
	d.click("Анна")
	d.click(start.Format("02.01.2006"))
	d.click("09:00")
	d.click("Использовать имя")
	if !d.r.Contact {
		t.Fatal("contact button missing")
	}
	u := botEvent(d.chat, "", "")
	u.Message.Contact = &struct {
		Phone  string `json:"phone_number"`
		UserID int64  `json:"user_id"`
	}{"+996700000000", 999}
	d.event(u)
	if !d.r.Contact {
		t.Fatal("foreign contact accepted")
	}
	u = botEvent(d.chat, "", "")
	u.Message.Contact = &struct {
		Phone  string `json:"phone_number"`
		UserID int64  `json:"user_id"`
	}{"+996700000000", d.chat}
	d.event(u)
	if !strings.Contains(d.r.Text, "75 мин") || !strings.Contains(d.r.Text, "3100") || !d.r.RemoveKeyboard {
		t.Fatal("wrong quote", d.r)
	}
	if _, e := a.db.Exec(context.Background(), "UPDATE extras SET price=800 WHERE id=2"); e != nil {
		t.Fatal(e)
	}
	d.click("Подтвердить")
	if !strings.Contains(d.r.Text, "изменились") || !strings.Contains(d.r.Text, "3300") {
		t.Fatal("price change not reconfirmed", d.r)
	}
	d.click("Подтвердить")
	var total int
	if e := a.db.QueryRow(context.Background(), "SELECT total FROM bookings WHERE telegram_chat=$1", d.chat).Scan(&total); e != nil || total != 3300 {
		t.Fatal(total, e)
	}
	if _, e := a.db.Exec(context.Background(), "UPDATE bot_settings SET services_enabled=false"); e != nil {
		t.Fatal(e)
	}
	d.say("/services")
	if !strings.Contains(d.r.Text, "отключён") {
		t.Fatal("catalog setting ignored")
	}
	d.click("Записаться")
	d.key("Классический")
}
func TestPublicClientAPIsClosed(t *testing.T) {
	_, srv, _ := fixture(t)
	for _, path := range []string{"/api/catalog", "/api/admin/catalog", "/api/slots?date=2027-01-01", "/api/manage/" + token()} {
		res, e := http.Get(srv.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 401 && res.StatusCode != 404 {
			t.Fatalf("public API %s: %d", path, res.StatusCode)
		}
	}
	res, e := http.Post(srv.URL+"/api/bookings", "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 && res.StatusCode != 404 {
		t.Fatal("public booking is open")
	}
}
func TestPollCallbacksAndReminderButtons(t *testing.T) {
	a, srv, start := fixture(t)
	b := linkBooking(t, a, createBooking(t, srv, selectionAt(start)))
	var ack, inline, allowed bool
	a.telegramClient = &http.Client{Transport: telegramTransport(func(req *http.Request) (*http.Response, error) {
		var p map[string]any
		_ = json.NewDecoder(req.Body).Decode(&p)
		body := `{"ok":true}`
		if strings.HasSuffix(req.URL.Path, "getUpdates") {
			raw, _ := json.Marshal(p["allowed_updates"])
			allowed = strings.Contains(string(raw), "callback_query")
			u := botEvent(1401, "", "menu")
			raw, _ = json.Marshal(map[string]any{"ok": true, "result": []telegramUpdate{u}})
			body = string(raw)
		}
		if strings.HasSuffix(req.URL.Path, "answerCallbackQuery") {
			ack = true
		}
		if strings.HasSuffix(req.URL.Path, "sendMessage") {
			raw, _ := json.Marshal(p)
			if strings.Contains(string(raw), "inline_keyboard") {
				inline = true
			}
			if strings.Contains(string(raw), "?booking=") {
				t.Fatal("website link in notification")
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if e := a.pollBot(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !ack || !inline || !allowed {
		t.Fatal("callback polling not working")
	}
	inline = false
	if !a.deliverOne(context.Background()) || !inline {
		t.Fatalf("notification buttons missing booking %d", b.ID)
	}
}
