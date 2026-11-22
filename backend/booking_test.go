package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidation(t *testing.T) {
	if !validToken(token()) || validToken("short") {
		t.Fatal("token validation")
	}
	if validDuration(30) || !validDuration(90) {
		t.Fatal("duration validation")
	}
}
func fixture(t *testing.T) (*app, *httptest.Server, time.Time) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set: PostgreSQL integration tests skipped")
	}
	ctx := context.Background()
	db, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	var name string
	if e = db.QueryRow(ctx, "SELECT current_database()").Scan(&name); e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(name, "_test") {
		t.Fatal("TEST_DATABASE_URL must use a database ending in _test")
	}
	if e = migrate(ctx, db); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, "TRUNCATE jobs,bookings,shifts,sessions RESTART IDENTITY CASCADE")
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(ctx, `UPDATE services SET active=true,prices='{"60":2500,"90":3500,"120":4500}' WHERE id=1; UPDATE specialists SET active=true; UPDATE extras SET active=true,price=600,minutes=15 WHERE id=2`)
	if e != nil {
		t.Fatal(e)
	}
	tz, _ := time.LoadLocation("Asia/Bishkek")
	now := time.Now().In(tz).AddDate(0, 0, 3)
	start := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, tz)
	_, e = db.Exec(ctx, "INSERT INTO shifts(specialist_id,starts_at,ends_at) VALUES(1,$1,$2)", start, start.Add(9*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	pw, _ := bcrypt.GenerateFromPassword([]byte("a-test-password"), bcrypt.MinCost)
	a := &app{db: db, tz: tz, base: "http://localhost:4200", password: pw, limiter: &rateLimiter{buckets: map[string]bucket{}}}
	srv := httptest.NewServer(a.routes())
	t.Cleanup(func() { srv.Close(); db.Close() })
	return a, srv, start
}
func request(t *testing.T, s *httptest.Server, method, path string, body any, cookie *http.Cookie) (int, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, s.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Error(e)
		return 0, nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
func selectionAt(start time.Time) selection {
	return selection{SpecialistID: 1, ServiceID: 1, Duration: 60, StartsAt: start, Name: "Тестовый клиент", Phone: "+996700000000", Consent: true}
}
func createBooking(t *testing.T, s *httptest.Server, in selection) booking {
	t.Helper()
	code, raw := request(t, s, "POST", "/api/bookings", in, nil)
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	var b booking
	if e := json.Unmarshal(raw, &b); e != nil {
		t.Fatal(e)
	}
	return b
}
func TestConcurrentBookingAndCancellation(t *testing.T) {
	a, s, start := fixture(t)
	in := selectionAt(start)
	var created, conflict atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := request(t, s, "POST", "/api/bookings", in, nil)
			switch code {
			case 201:
				created.Add(1)
			case 409:
				conflict.Add(1)
			default:
				t.Errorf("unexpected status %d", code)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || conflict.Load() != 11 {
		t.Fatalf("created=%d conflict=%d", created.Load(), conflict.Load())
	}
	b, e := scanBooking(a.db.QueryRow(context.Background(), bookingSelect+"LIMIT 1"))
	if e != nil {
		t.Fatal(e)
	}
	code, _ := request(t, s, "GET", "/api/manage/"+b.Token, nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	var status string
	_ = a.db.QueryRow(context.Background(), "SELECT status FROM bookings WHERE id=$1", b.ID).Scan(&status)
	if status != "confirmed" {
		t.Fatal("GET mutated booking")
	}
	createBooking(t, s, selectionAt(start.Add(time.Hour)))
	for i := 0; i < 2; i++ {
		code, raw := request(t, s, "POST", "/api/manage/"+b.Token+"/cancel", map[string]any{}, nil)
		if code != 200 {
			t.Fatalf("cancel %d %s", code, raw)
		}
	}
	createBooking(t, s, in)
}
func TestSlotsExtrasAndReschedule(t *testing.T) {
	a, s, start := fixture(t)
	in := selectionAt(start)
	in.ExtraIDs = []int64{2}
	b := linkBooking(t, a, createBooking(t, s, in))
	if b.Total != 3100 || b.EndsAt.Sub(b.StartsAt) != 75*time.Minute {
		t.Fatal("wrong extra quote")
	}
	other := selectionAt(start.Add(time.Hour))
	code, _ := request(t, s, "POST", "/api/bookings", other, nil)
	if code != 409 {
		t.Fatal("extra minutes didn't block overlap")
	}
	other.StartsAt = start.Add(75 * time.Minute)
	createBooking(t, s, other)
	code, raw := request(t, s, "GET", "/api/slots?specialist_id=1&service_id=1&duration=60&date="+start.Format("2006-01-02"), nil, nil)
	if code != 200 {
		t.Fatalf("slots: %s", raw)
	}
	var slots []time.Time
	_ = json.Unmarshal(raw, &slots)
	if len(slots) == 0 {
		t.Fatal("expected free slots")
	}
	for _, slot := range slots {
		if slot.Before(start.Add(135 * time.Minute)) {
			t.Errorf("occupied slot offered: %s", slot)
		}
	}
	_, e := a.db.Exec(context.Background(), `UPDATE services SET prices='{"60":9999}' WHERE id=1; UPDATE extras SET price=9999,minutes=60 WHERE id=2`)
	if e != nil {
		t.Fatal(e)
	}
	newTime := start.Add(4 * time.Hour)
	code, raw = request(t, s, "POST", "/api/manage/"+b.Token+"/reschedule", map[string]any{"starts_at": newTime}, nil)
	if code != 200 {
		t.Fatalf("reschedule %d %s", code, raw)
	}
	var moved booking
	_ = json.Unmarshal(raw, &moved)
	if moved.Total != 3100 || moved.EndsAt.Sub(moved.StartsAt) != 75*time.Minute || moved.Version != 2 {
		t.Fatal("snapshot not preserved")
	}
	var jobs, old int
	if e = a.db.QueryRow(context.Background(), "SELECT count(*),count(*) FILTER(WHERE version<>2) FROM jobs WHERE booking_id=$1", b.ID).Scan(&jobs, &old); e != nil {
		t.Fatal(e)
	}
	if jobs != 3 || old != 0 {
		t.Fatalf("invalid reminders: jobs=%d old=%d", jobs, old)
	}
	other.StartsAt = newTime.Add(30 * time.Minute)
	code, _ = request(t, s, "POST", "/api/bookings", other, nil)
	if code != 409 {
		t.Fatal("reschedule didn't reserve time")
	}
}
func TestAvailabilityBoundaries(t *testing.T) {
	_, s, start := fixture(t)
	cases := []struct {
		name   string
		edit   func(*selection)
		status int
	}{{"before shift", func(i *selection) { i.StartsAt = start.Add(-time.Hour) }, 409}, {"past", func(i *selection) { i.StartsAt = time.Now().Add(-time.Hour) }, 409}, {"unaligned", func(i *selection) { i.StartsAt = start.Add(7 * time.Minute) }, 409}, {"invalid duration", func(i *selection) { i.Duration = 45 }, 400}, {"duplicate extra", func(i *selection) { i.ExtraIDs = []int64{2, 2} }, 400}, {"missing consent", func(i *selection) { i.Consent = false }, 400}, {"outside shift with extra", func(i *selection) { i.StartsAt = start.Add(8 * time.Hour); i.ExtraIDs = []int64{2} }, 409}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := selectionAt(start)
			tc.edit(&in)
			code, raw := request(t, s, "POST", "/api/bookings", in, nil)
			if code != tc.status {
				t.Fatalf("%d %s", code, raw)
			}
		})
	}
	createBooking(t, s, selectionAt(start.Add(8*time.Hour)))
}
func loginAdmin(t *testing.T, s *httptest.Server) *http.Cookie {
	t.Helper()
	res, e := http.Post(s.URL+"/api/admin/login", "application/json", strings.NewReader(`{"password":"a-test-password"}`))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatal("login failed")
	}
	return res.Cookies()[0]
}
func TestAdminAccessAndShiftProtection(t *testing.T) {
	a, s, start := fixture(t)
	code, _ := request(t, s, "GET", "/api/admin/dashboard", nil, nil)
	if code != 401 {
		t.Fatal("admin exposed")
	}
	cookie := loginAdmin(t, s)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	createBooking(t, s, selectionAt(start))
	var shiftID int64
	_ = a.db.QueryRow(context.Background(), "SELECT id FROM shifts LIMIT 1").Scan(&shiftID)
	code, raw := request(t, s, "DELETE", fmt.Sprintf("/api/admin/shifts/%d", shiftID), nil, cookie)
	if code != 409 {
		t.Fatalf("deleted occupied shift %d %s", code, raw)
	}
	code, raw = request(t, s, "GET", "/api/admin/dashboard?date="+start.Format("2006-01-02"), nil, cookie)
	if code != 200 || !bytes.Contains(raw, []byte(`"confirmed":1`)) {
		t.Fatalf("dashboard %d %s", code, raw)
	}
	req, _ := http.NewRequest("POST", s.URL+"/api/admin/logout", strings.NewReader("{}"))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://evil.example")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("CSRF accepted")
	}
	code, _ = request(t, s, "POST", "/api/admin/logout", nil, cookie)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = request(t, s, "GET", "/api/admin/dashboard", nil, cookie)
	if code != 401 {
		t.Fatal("logout didn't revoke session")
	}
}

type telegramTransport func(*http.Request) (*http.Response, error)

func (f telegramTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeTelegram(a *app, status int) *atomic.Int32 {
	count := &atomic.Int32{}
	a.telegramClient = &http.Client{Transport: telegramTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true}`
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			body = `{"ok":true,"result":{"username":"test_studio_bot"}}`
		} else {
			count.Add(1)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return count
}
func linkBooking(t *testing.T, a *app, b booking) booking {
	t.Helper()
	linked, err := a.linkTelegram(context.Background(), b.Token, 12345)
	if err != nil {
		t.Fatal(err)
	}
	return linked
}
func TestNotificationWorkerAndStaleJobs(t *testing.T) {
	a, s, start := fixture(t)
	count := fakeTelegram(a, 200)
	b := linkBooking(t, a, createBooking(t, s, selectionAt(start)))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.deliverOne(context.Background()) }()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("confirmation delivered %d times", count.Load())
	}
	code, _ := request(t, s, "POST", "/api/manage/"+b.Token+"/cancel", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	_, e := a.db.Exec(context.Background(), "INSERT INTO jobs(booking_id,version,kind,channel,due_at) VALUES($1,1,'stale','telegram',now())", b.ID)
	if e != nil {
		t.Fatal(e)
	}
	for a.deliverOne(context.Background()) {
	}
	if count.Load() != 2 {
		t.Fatalf("stale reminder delivered, count=%d", count.Load())
	}
}

func TestDatabaseExclusionAndRetry(t *testing.T) {
	a, s, start := fixture(t)
	b := createBooking(t, s, selectionAt(start))
	_, e := a.db.Exec(context.Background(), `INSERT INTO bookings(token,specialist_id,service_id,service_name,duration,total,starts_at,ends_at,name,phone) SELECT $1,specialist_id,service_id,service_name,duration,total,starts_at,ends_at,name,phone FROM bookings WHERE id=$2`, token(), b.ID)
	if e == nil || !strings.Contains(e.Error(), "23P01") {
		t.Fatalf("database did not reject overlap: %v", e)
	}
	fakeTelegram(a, 500)
	linkBooking(t, a, b)
	if !a.deliverOne(context.Background()) {
		t.Fatal("failed delivery was not recorded")
	}
	var attempts int
	var due time.Time
	var sent *time.Time
	e = a.db.QueryRow(context.Background(), "SELECT attempts,due_at,sent_at FROM jobs WHERE booking_id=$1 AND kind='confirmation'", b.ID).Scan(&attempts, &due, &sent)
	if e != nil {
		t.Fatal(e)
	}
	if attempts != 1 || !due.After(time.Now()) || sent != nil {
		t.Fatal("failed delivery not scheduled for retry")
	}
}
func TestTelegramChunks(t *testing.T) {
	text := strings.Repeat("Привет 🌿", 1000)
	chunks := telegramChunks(text)
	if strings.Join(chunks, "") != text {
		t.Fatal("corrupt text")
	}
	for _, s := range chunks {
		if len([]rune(s)) > 2000 {
			t.Fatal("oversized chunk")
		}
	}
}

func TestTelegramBindingAndDiscovery(t *testing.T) {
	a, s, start := fixture(t)
	fakeTelegram(a, 200)
	if err := a.resolveBot(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, raw := request(t, s, "GET", "/api/catalog", nil, nil)
	if code != 200 || !bytes.Contains(raw, []byte(`"telegram_bot":"test_studio_bot"`)) {
		t.Fatalf("bot discovery: %s", raw)
	}
	b := createBooking(t, s, selectionAt(start))
	var jobs int
	if err := a.db.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE booking_id=$1", b.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatal("unlinked booking has delivery jobs")
	}
	b = linkBooking(t, a, b)
	linkBooking(t, a, b)
	if _, err := a.linkTelegram(context.Background(), b.Token, 999); err == nil {
		t.Fatal("different chat stole booking")
	}
	if err := a.db.QueryRow(context.Background(), "SELECT count(*) FROM jobs WHERE booking_id=$1 AND channel='telegram'", b.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 3 {
		t.Fatalf("want confirmation and two reminders, got %d", jobs)
	}
	code, raw = request(t, s, "GET", "/api/manage/"+b.Token, nil, nil)
	if code != 200 || bytes.Contains(raw, []byte(`"email"`)) {
		t.Fatal("email still exposed")
	}
}
