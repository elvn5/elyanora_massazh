package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var migrations embed.FS

type app struct {
	botName        atomic.Value
	telegramClient *http.Client
	db             *pgxpool.Pool
	tz             *time.Location
	base           string
	secure         bool
	password       []byte
	limiter        *rateLimiter
}
type rateLimiter struct {
	sync.Mutex
	buckets map[string]bucket
}
type bucket struct {
	n     int
	until time.Time
}

func (l *rateLimiter) allow(key string, limit int) bool {
	l.Lock()
	defer l.Unlock()
	now := time.Now()
	for k, b := range l.buckets {
		if b.until.Before(now) {
			delete(l.buckets, k)
		}
	}
	b := l.buckets[key]
	if b.n >= limit {
		return false
	}
	if b.n == 0 {
		b.until = now.Add(time.Minute)
	}
	b.n++
	l.buckets[key] = b
	return true
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	respond(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		fail(w, 400, "Проверьте данные запроса")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "Ожидается один JSON-объект")
		return false
	}
	return true
}
func dbError(w http.ResponseWriter, e error) {
	var p *pgconn.PgError
	if errors.As(e, &p) && (p.Code == "23P01" || p.Code == "23505") {
		fail(w, 409, "Время уже занято или интервалы пересекаются. Выберите другое окно.")
		return
	}
	slog.Error("database operation", "error", e)
	fail(w, 500, "Не удалось выполнить операцию. Попробуйте ещё раз.")
}
func migrate(ctx context.Context, db *pgxpool.Pool) error {
	tx, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(814729)"); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('public.schema_migrations') IS NOT NULL").Scan(&exists); e != nil {
		return e
	}
	if !exists {
		sql, _ := migrations.ReadFile("migrations/001_init.sql")
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return e
		}
	}
	var applied bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=2)").Scan(&applied); e != nil {
		return e
	}
	if !applied {
		sql, err := migrations.ReadFile("migrations/002_telegram_only.sql")
		if err != nil {
			return err
		}
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return e
		}
	}
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=3)").Scan(&applied); e != nil {
		return e
	}
	if !applied {
		sql, err := migrations.ReadFile("migrations/003_bot_settings.sql")
		if err != nil {
			return err
		}
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return e
		}
	}

	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=4)").Scan(&applied); e != nil {
		return e
	}
	if !applied {
		sql, err := migrations.ReadFile("migrations/004_elyanora_brand.sql")
		if err != nil {
			return err
		}
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return e
		}
	}

	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=5)").Scan(&applied); e != nil {
		return e
	}
	if !applied {
		sql, err := migrations.ReadFile("migrations/005_bot_booking.sql")
		if err != nil {
			return err
		}
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return e
		}
	}

	return tx.Commit(ctx)
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "hash-password" {
		b, e := bcrypt.GenerateFromPassword([]byte(os.Getenv("ADMIN_PASSWORD")), 12)
		if e != nil {
			panic(e)
		}
		fmt.Println(string(b))
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, e := pgxpool.New(ctx, env("DATABASE_URL", "postgres://masajka:masajka@localhost:5432/masajka?sslmode=disable"))
	if e != nil {
		panic(e)
	}
	defer db.Close()
	if e = migrate(ctx, db); e != nil {
		panic(e)
	}
	tz, e := time.LoadLocation(env("SALON_TIMEZONE", "Asia/Bishkek"))
	if e != nil {
		panic(e)
	}
	password := os.Getenv("ADMIN_PASSWORD_HASH")
	if password == "" {
		plain := os.Getenv("ADMIN_PASSWORD")
		if len(plain) < 12 {
			panic("set ADMIN_PASSWORD (at least 12 characters) or ADMIN_PASSWORD_HASH")
		}
		b, e := bcrypt.GenerateFromPassword([]byte(plain), 12)
		if e != nil {
			panic(e)
		}
		password = string(b)
	}
	if _, e = bcrypt.Cost([]byte(password)); e != nil {
		panic("invalid ADMIN_PASSWORD_HASH")
	}
	base := strings.TrimRight(env("PUBLIC_URL", "http://localhost:4200"), "/")
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		panic("invalid PUBLIC_URL")
	}
	a := &app{db: db, tz: tz, base: base, secure: u.Scheme == "https", password: []byte(password), limiter: &rateLimiter{buckets: map[string]bucket{}}}
	srv := &http.Server{Addr: env("HTTP_ADDR", ":8080"), Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go a.worker(ctx)
	if os.Getenv("TELEGRAM_BOT_TOKEN") != "" {
		go a.bot(ctx)
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	slog.Info("API ready", "address", srv.Addr)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		panic(e)
	}
}
func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if a.db.Ping(r.Context()) != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	m.Handle("GET /api/admin/catalog", a.auth(http.HandlerFunc(a.catalog)))
	m.Handle("GET /api/admin/slots", a.auth(http.HandlerFunc(a.slots)))
	m.Handle("POST /api/admin/bookings", a.auth(http.HandlerFunc(a.book)))
	m.Handle("GET /api/admin/manage/{token}", a.auth(http.HandlerFunc(a.manage)))
	m.Handle("POST /api/admin/manage/{token}/cancel", a.auth(http.HandlerFunc(a.cancel)))
	m.Handle("POST /api/admin/manage/{token}/reschedule", a.auth(http.HandlerFunc(a.reschedule)))
	m.HandleFunc("POST /api/admin/login", a.login)
	m.Handle("POST /api/admin/logout", a.auth(http.HandlerFunc(a.logout)))
	m.Handle("GET /api/admin/dashboard", a.auth(http.HandlerFunc(a.dashboard)))
	m.Handle("GET /api/admin/bot", a.auth(http.HandlerFunc(a.getBotSettings)))
	m.Handle("PUT /api/admin/bot", a.auth(http.HandlerFunc(a.saveBotSettings)))
	m.Handle("POST /api/admin/bot/check", a.auth(http.HandlerFunc(a.checkBot)))
	m.Handle("POST /api/admin/bot/retry", a.auth(http.HandlerFunc(a.retryBotJobs)))
	m.Handle("POST /api/admin/{kind}", a.auth(http.HandlerFunc(a.saveCatalog)))
	m.Handle("POST /api/admin/shifts", a.auth(http.HandlerFunc(a.addShift)))
	m.Handle("DELETE /api/admin/shifts/{id}", a.auth(http.HandlerFunc(a.deleteShift)))
	m.Handle("POST /api/admin/bookings/{id}/status", a.auth(http.HandlerFunc(a.status)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != a.base {
				fail(w, 403, "Недопустимый источник запроса")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func (a *app) login(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow("login", 10) {
		fail(w, 429, "Слишком много попыток. Подождите минуту.")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if bcrypt.CompareHashAndPassword(a.password, []byte(in.Password)) != nil {
		fail(w, 401, "Неверный пароль")
		return
	}
	t := token()
	_, e := a.db.Exec(r.Context(), "INSERT INTO sessions VALUES($1,now()+interval '12 hours')", hash(t))
	if e != nil {
		dbError(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: t, Path: "/api/admin", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *app) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("session")
		if e != nil {
			fail(w, 401, "Войдите в панель администратора")
			return
		}
		var valid bool
		e = a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND expires_at>now())", hash(c.Value)).Scan(&valid)
		if e != nil || !valid {
			fail(w, 401, "Сессия истекла. Войдите снова.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("session")
	_, e := a.db.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash(c.Value))
	if e != nil {
		dbError(w, e)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/api/admin", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	respond(w, 200, map[string]bool{"ok": true})
}
func validToken(t string) bool { b, e := hex.DecodeString(t); return e == nil && len(b) == 32 }
func parseID(s string) int64   { n, _ := strconv.ParseInt(s, 10, 64); return n }
