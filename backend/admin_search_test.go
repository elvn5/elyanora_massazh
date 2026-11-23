package main

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestSearchBookingsAndClients(t *testing.T) {
	a, s, start := fixture(t)
	book := func(at time.Time, name, phone string) booking {
		in := selectionAt(at)
		in.Name, in.Phone = name, phone
		return createBooking(t, s, in)
	}
	first := book(start, "Анна Иванова", "+996 700 111 222")
	book(start.Add(2*time.Hour), "Анна", "+996700111222") // same client, other formatting
	other := book(start.Add(4*time.Hour), "Бекзат", "+996555000111")
	// Mark Bekzat's visit as completed in the past to exercise spend and segments.
	past := time.Now().Add(-90 * 24 * time.Hour).Truncate(time.Hour)
	if _, err := a.db.Exec(context.Background(), "UPDATE bookings SET status='completed', starts_at=$2, ends_at=$3 WHERE id=$1", other.ID, past, past.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Items   []map[string]any `json:"items"`
		Total   int              `json:"total"`
		Revenue int              `json:"revenue"`
	}
	get := func(path string, want int) page {
		t.Helper()
		code, raw := request(t, s, "GET", path, nil, testCookie(s))
		if code != want {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
		var p page
		_ = json.Unmarshal(raw, &p)
		return p
	}
	day := start.In(a.tz).Format("2006-01-02")
	cases := []struct {
		query string
		total int
	}{
		{"", 3},
		{"q=" + url.QueryEscape("иванова"), 1},          // case-insensitive Cyrillic client name
		{"q=" + url.QueryEscape("анна"), 3},             // also matches the specialist «Анна»
		{"q=" + url.QueryEscape("111 222"), 2},          // phone digits regardless of formatting
		{"q=" + url.QueryEscape("#"+itoa(first.ID)), 1}, // booking number
		{"q=" + url.QueryEscape("%"), 0},                // LIKE wildcards are literal
		{"status=completed", 1},
		{"specialist_id=1&status=confirmed", 2},
		{"from=" + day + "&to=" + day, 2},
		{"phone=" + url.QueryEscape("996700111222"), 2},
	}
	for _, c := range cases {
		if p := get("/api/admin/bookings?"+c.query, 200); p.Total != c.total || len(p.Items) != c.total {
			t.Errorf("bookings?%s: total=%d items=%d want %d", c.query, p.Total, len(p.Items), c.total)
		}
	}
	if p := get("/api/admin/bookings?limit=1&sort=desc", 200); p.Total != 3 || len(p.Items) != 1 || p.Revenue != other.Total {
		t.Errorf("paging/revenue: %+v", p)
	}
	for _, bad := range []string{"status=bogus", "from=23.11.2026"} {
		get("/api/admin/bookings?"+bad, 400)
	}

	clients := get("/api/admin/clients", 200)
	if clients.Total != 2 {
		t.Fatalf("clients grouped by phone: %+v", clients)
	}
	anna := clients.Items[0]
	if anna["bookings"].(float64) != 2 || anna["next_visit"] == nil {
		t.Errorf("anna card: %+v", anna)
	}
	for q, want := range map[string]int{
		"q=" + url.QueryEscape("бек"): 1, "q=555": 1, "segment=upcoming": 1, "segment=lapsed": 1,
		"segment=no_show": 0, "segment=regular": 0, "sort=spent": 2, "sort=name": 2,
	} {
		if p := get("/api/admin/clients?"+q, 200); p.Total != want {
			t.Errorf("clients?%s: total=%d want %d", q, p.Total, want)
		}
	}
	if p := get("/api/admin/clients?sort=spent", 200); p.Items[0]["name"] != "Бекзат" {
		t.Errorf("sort by spent: %+v", p.Items)
	}
	get("/api/admin/clients?segment=bogus", 400)
	get("/api/admin/clients?sort=bogus", 400)
	if code, _ := request(t, s, "GET", "/api/admin/clients", nil, nil); code != 401 {
		t.Fatalf("clients without session: %d", code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
