package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNameLengthCountsCharacters(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"Я", false},
		{"Ян", true},
		{strings.Repeat("Ж", 100), true},
		{strings.Repeat("Ж", 101), false},
		{"Ан\x00на", false},
		{"Анна\nИванова", false},
	}
	for _, c := range cases {
		if got := validClientName(c.name); got != c.ok {
			t.Errorf("validClientName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
	if !validText("Строка 1\nСтрока 2", 1000) || validText("a\x00b", 1000) || !validText(strings.Repeat("Ж", 1000), 1000) || validText(strings.Repeat("Ж", 1001), 1000) {
		t.Fatal("validText")
	}
}

func TestPermanentUpdateError(t *testing.T) {
	for code, want := range map[string]bool{"22P05": true, "22001": true, "23514": true, "23P01": false, "23505": false, "40001": false, "57014": false} {
		if got := permanentUpdateError(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code})); got != want {
			t.Errorf("code %s: got %v want %v", code, got, want)
		}
	}
	if permanentUpdateError(fmt.Errorf("connection reset")) {
		t.Fatal("network errors must stay retryable")
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r.RemoteAddr = "10.0.0.5:4321"
	if clientIP(r) != "10.0.0.5" {
		t.Fatal(clientIP(r))
	}
	r.Header.Set("X-Real-IP", "203.0.113.7")
	if clientIP(r) != "203.0.113.7" {
		t.Fatal(clientIP(r))
	}
}

func TestCyrillicCatalogAndClientNames(t *testing.T) {
	_, s, start := fixture(t)
	long := strings.Repeat("Ж", 80) // 160 bytes, previously rejected
	if code, body := request(t, s, "POST", "/api/admin/extras", map[string]any{"name": long, "price": 100, "minutes": 15, "active": true}, testCookie(s)); code != 200 {
		t.Fatalf("80-character Cyrillic name rejected: %d %s", code, body)
	}
	if code, _ := request(t, s, "POST", "/api/admin/extras", map[string]any{"name": "Я", "price": 100, "minutes": 15, "active": true}, testCookie(s)); code != 400 {
		t.Fatalf("one-character name accepted: %d", code)
	}
	in := selectionAt(start)
	in.Name = "Я"
	if code, _ := request(t, s, "POST", "/api/admin/bookings", in, testCookie(s)); code != 400 {
		t.Fatalf("one-character client name accepted: %d", code)
	}
}

func TestBotRejectsNULName(t *testing.T) {
	a, _, start := fixture(t)
	d := botDriver{t: t, a: a, chat: 1301}
	d.say("/start")
	d.click("Записаться")
	d.click("Классический")
	d.click("60 мин")
	d.click("Продолжить")
	d.click("Анна")
	d.click(start.Format("02.01.2006"))
	d.click(start.Format("15:04"))
	// Before the fix this reached jsonb and failed on every retry, stalling polling.
	d.say("Ан\x00на")
	if !strings.Contains(d.r.Text, "Введите имя") {
		t.Fatalf("NUL name not rejected: %q", d.r.Text)
	}
}

func TestLoginLimitIsPerClient(t *testing.T) {
	_, s, _ := fixture(t)
	try := func(ip string) int {
		req, _ := http.NewRequest("POST", s.URL+"/api/admin/login", strings.NewReader(`{"password":"wrong-password"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Real-IP", ip)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	for range 10 {
		try("198.51.100.1")
	}
	if code := try("198.51.100.1"); code != 429 {
		t.Fatalf("attacker not limited: %d", code)
	}
	if code := try("198.51.100.2"); code != 401 {
		t.Fatalf("other client locked out: %d", code)
	}
}
