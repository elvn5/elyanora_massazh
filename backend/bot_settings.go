package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type botSettings struct {
	WelcomeMessage  string `json:"welcome_message"`
	ContactText     string `json:"contact_text"`
	ServicesEnabled bool   `json:"services_enabled"`
	Reminder24h     bool   `json:"reminder_24h"`
	Reminder2h      bool   `json:"reminder_2h"`
}
type settingsReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readBotSettings(ctx context.Context, db settingsReader, lock bool) (s botSettings, err error) {
	sql := "SELECT welcome_message,contact_text,services_enabled,reminder_24h,reminder_2h FROM bot_settings WHERE id=1"
	if lock {
		sql += " FOR SHARE"
	}
	err = db.QueryRow(ctx, sql).Scan(&s.WelcomeMessage, &s.ContactText, &s.ServicesEnabled, &s.Reminder24h, &s.Reminder2h)
	return
}
func (s botSettings) allows(kind string) bool {
	return (kind != "reminder_24h" || s.Reminder24h) && (kind != "reminder_2h" || s.Reminder2h)
}
func (a *app) getBotSettings(w http.ResponseWriter, r *http.Request) {
	s, err := readBotSettings(r.Context(), a.db, false)
	if err != nil {
		dbError(w, err)
		return
	}
	var pending, failed, sent, linked int
	err = a.db.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE sent_at IS NULL AND attempts<12),count(*) FILTER(WHERE sent_at IS NULL AND last_error IS NOT NULL),count(*) FILTER(WHERE sent_at>=now()-interval '7 days') FROM jobs`).Scan(&pending, &failed, &sent)
	if err != nil {
		dbError(w, err)
		return
	}
	err = a.db.QueryRow(r.Context(), "SELECT count(*) FROM bookings WHERE telegram_chat IS NOT NULL AND status='confirmed' AND starts_at>now()").Scan(&linked)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"settings": s, "configured": os.Getenv("TELEGRAM_BOT_TOKEN") != "", "username": a.botUsername(), "stats": map[string]int{"pending": pending, "failed": failed, "sent": sent, "linked": linked}})
}
func (a *app) saveBotSettings(w http.ResponseWriter, r *http.Request) {
	var s botSettings
	if !decode(w, r, &s) {
		return
	}
	s.WelcomeMessage = strings.TrimSpace(s.WelcomeMessage)
	s.ContactText = strings.TrimSpace(s.ContactText)
	if n := utf8.RuneCountInString(s.WelcomeMessage); n < 2 || n > 1000 || utf8.RuneCountInString(s.ContactText) > 500 {
		fail(w, 400, "Приветствие: от 2 до 1000 символов. Контакты: до 500 символов.")
		return
	}
	_, err := a.db.Exec(r.Context(), `UPDATE bot_settings SET welcome_message=$1,contact_text=$2,services_enabled=$3,reminder_24h=$4,reminder_2h=$5,updated_at=now() WHERE id=1`, s.WelcomeMessage, s.ContactText, s.ServicesEnabled, s.Reminder24h, s.Reminder2h)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, s)
}
func (a *app) checkBot(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" {
		fail(w, 409, "Токен бота не настроен в окружении сервера")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := a.resolveBot(ctx); err != nil {
		fail(w, 502, "Telegram не подтвердил подключение. Проверьте токен и доступность Telegram API.")
		return
	}
	respond(w, 200, map[string]string{"username": a.botUsername()})
}
func (a *app) retryBotJobs(w http.ResponseWriter, r *http.Request) {
	result, err := a.db.Exec(r.Context(), `UPDATE jobs j SET attempts=0,due_at=now(),last_error=NULL FROM bookings b WHERE b.id=j.booking_id AND b.version=j.version AND j.sent_at IS NULL AND j.last_error IS NOT NULL AND ((b.status='confirmed' AND b.starts_at>now()) OR (b.status='cancelled' AND j.kind='cancellation' AND j.due_at>now()-interval '7 days'))`)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]int64{"retried": result.RowsAffected()})
}

type dailyMetric struct {
	Date     string `json:"date"`
	Bookings int    `json:"bookings"`
	Revenue  int    `json:"revenue"`
}

func (a *app) weeklyMetrics(ctx context.Context, date time.Time) ([]dailyMetric, error) {
	rows, err := a.db.Query(ctx, `SELECT to_char(d.day,'YYYY-MM-DD'),count(b.id),COALESCE(sum(b.total) FILTER(WHERE b.status='completed'),0) FROM generate_series($1::date,$2::date,interval '1 day') d(day) LEFT JOIN bookings b ON b.starts_at >= (d.day::timestamp AT TIME ZONE $3) AND b.starts_at < ((d.day::timestamp+interval '1 day') AT TIME ZONE $3) GROUP BY d.day ORDER BY d.day`, date.AddDate(0, 0, -6).Format("2006-01-02"), date.Format("2006-01-02"), a.tz.String())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[dailyMetric])
}
