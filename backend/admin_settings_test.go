package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBotSettingsAuthorizationAndPersistence(t *testing.T) {
	a, s, _ := fixture(t)
	settings := botSettings{WelcomeMessage: "Добро пожаловать в нашу студию", ContactText: "Адрес: тестовая улица", ServicesEnabled: false, Reminder24h: false, Reminder2h: true}
	for _, method := range []string{"GET", "PUT"} {
		code, _ := request(t, s, method, "/api/admin/bot", settings, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("unauthorized %s: %d", method, code)
		}
	}
	for _, path := range []string{"/api/admin/bot/check", "/api/admin/bot/retry"} {
		code, _ := request(t, s, "POST", path, struct{}{}, nil)
		if code != http.StatusUnauthorized {
			t.Fatal("bot action exposed")
		}
	}
	cookie := loginAdmin(t, s)
	code, raw := request(t, s, "PUT", "/api/admin/bot", settings, cookie)
	if code != 200 {
		t.Fatalf("save %d %s", code, raw)
	}
	stored, err := readBotSettings(context.Background(), a.db, false)
	if err != nil || stored != settings {
		t.Fatalf("settings not persisted: %+v %v", stored, err)
	}
	code, raw = request(t, s, "GET", "/api/admin/bot", nil, cookie)
	if code != 200 || bytes.Contains(raw, []byte("TELEGRAM_BOT_TOKEN")) {
		t.Fatalf("unsafe settings response %s", raw)
	}
	code, raw = request(t, s, "GET", "/api/admin/catalog", nil, cookie)
	if code != 200 || !bytes.Contains(raw, []byte(`"reminder_24h":false`)) {
		t.Fatalf("public reminder settings: %s", raw)
	}
	settings.WelcomeMessage = strings.Repeat("я", 1001)
	code, _ = request(t, s, "PUT", "/api/admin/bot", settings, cookie)
	if code != 400 {
		t.Fatal("oversized greeting accepted")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-secret-token")
	fakeTelegram(a, 200)
	code, raw = request(t, s, "POST", "/api/admin/bot/check", struct{}{}, cookie)
	if code != 200 || !bytes.Contains(raw, []byte("test_studio_bot")) || bytes.Contains(raw, []byte("test-secret-token")) {
		t.Fatalf("connection check %d %s", code, raw)
	}
}
func TestDisabledRemindersAndNotificationFooter(t *testing.T) {
	a, s, start := fixture(t)
	b := linkBooking(t, a, createBooking(t, s, selectionAt(start)))
	var messages []string
	a.telegramClient = &http.Client{Transport: telegramTransport(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, payload.Text)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
	})}
	_, err := a.db.Exec(context.Background(), `UPDATE bot_settings SET reminder_24h=false,contact_text='Адрес студии: тест'; UPDATE jobs SET due_at=now()`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if !a.deliverOne(context.Background()) {
			break
		}
	}
	if len(messages) != 2 {
		t.Fatalf("expected confirmation and 2h reminder, got %d", len(messages))
	}
	for _, message := range messages {
		if !strings.Contains(message, "Адрес студии: тест") {
			t.Fatal("contact footer missing")
		}
		if strings.Contains(message, "завтрашнем") {
			t.Fatal("disabled reminder sent")
		}
	}
	var pending int
	err = a.db.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE booking_id=$1 AND kind='reminder_24h'", b.ID).Scan(&pending)
	if err != nil || pending != 0 {
		t.Fatal("disabled reminder was not discarded")
	}
}
func TestRetryOnlyCurrentNotifications(t *testing.T) {
	a, s, start := fixture(t)
	b := linkBooking(t, a, createBooking(t, s, selectionAt(start)))
	cookie := loginAdmin(t, s)
	_, err := a.db.Exec(context.Background(), "UPDATE jobs SET attempts=12,last_error='delivery failed' WHERE booking_id=$1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, raw := request(t, s, "POST", "/api/admin/bot/retry", struct{}{}, cookie)
	if code != 200 || !bytes.Contains(raw, []byte(`"retried":3`)) {
		t.Fatalf("retry %s", raw)
	}
	_, err = a.db.Exec(context.Background(), "UPDATE bookings SET version=version+1 WHERE id=$1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(context.Background(), "UPDATE jobs SET attempts=12,last_error='delivery failed' WHERE booking_id=$1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, raw = request(t, s, "POST", "/api/admin/bot/retry", struct{}{}, cookie)
	if code != 200 || !bytes.Contains(raw, []byte(`"retried":0`)) {
		t.Fatal("stale notifications retried")
	}
}
func TestDashboardWeekAndBotWelcome(t *testing.T) {
	a, s, start := fixture(t)
	b := createBooking(t, s, selectionAt(start))
	cookie := loginAdmin(t, s)
	// A booking just after local midnight belongs to the salon date, not UTC's previous day.
	_, err := a.db.Exec(context.Background(), "UPDATE bookings SET starts_at=$2,ends_at=$3,status='completed' WHERE id=$1", b.ID, start.Add(-9*time.Hour), start.Add(-8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	code, raw := request(t, s, "GET", "/api/admin/dashboard?date="+start.Format("2006-01-02"), nil, cookie)
	if code != 200 {
		t.Fatalf("dashboard %s", raw)
	}
	var data struct {
		Weekly []dailyMetric `json:"weekly"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Weekly) != 7 || data.Weekly[6].Bookings != 1 || data.Weekly[6].Revenue != b.Total || data.Weekly[5].Bookings != 0 {
		t.Fatalf("wrong weekly aggregation: %+v", data.Weekly)
	}
	settings := botSettings{WelcomeMessage: "Наше приветствие", ContactText: "Контакты студии", ServicesEnabled: false}
	text := botWelcome(settings, "https://example.com")
	if !strings.Contains(text, settings.WelcomeMessage) || !strings.Contains(text, settings.ContactText) || strings.Contains(text, "/services") {
		t.Fatal("welcome settings ignored")
	}
	settings.ServicesEnabled = true
	if !strings.Contains(botWelcome(settings, "https://example.com"), "/services") {
		t.Fatal("service command missing")
	}
}
