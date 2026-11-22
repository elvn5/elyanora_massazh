package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type specialist struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Bio    string `json:"bio"`
	Active bool   `json:"active"`
}
type service struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Prices      map[string]int `json:"prices"`
	Active      bool           `json:"active"`
}
type extra struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Price   int    `json:"price"`
	Minutes int    `json:"minutes"`
	Active  bool   `json:"active"`
}
type booking struct {
	ID           int64     `json:"id"`
	Token        string    `json:"token,omitempty"`
	SpecialistID int64     `json:"specialist_id"`
	Specialist   string    `json:"specialist"`
	ServiceID    int64     `json:"service_id"`
	Service      string    `json:"service"`
	Duration     int       `json:"duration"`
	Extras       []extra   `json:"extras"`
	Total        int       `json:"total"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Name         string    `json:"name"`
	Phone        string    `json:"phone"`
	Status       string    `json:"status"`
	Version      int       `json:"version"`
	TelegramChat *int64    `json:"telegram_chat,omitempty"`
}

const bookingSelect = `SELECT b.id,b.token,b.specialist_id,s.name,b.service_id,b.service_name,b.duration,b.extras,b.total,b.starts_at,b.ends_at,b.name,b.phone,b.status,b.version,b.telegram_chat FROM bookings b JOIN specialists s ON s.id=b.specialist_id `

func scanBooking(row pgx.Row) (b booking, e error) {
	e = row.Scan(&b.ID, &b.Token, &b.SpecialistID, &b.Specialist, &b.ServiceID, &b.Service, &b.Duration, &b.Extras, &b.Total, &b.StartsAt, &b.EndsAt, &b.Name, &b.Phone, &b.Status, &b.Version, &b.TelegramChat)
	return
}
func (a *app) catalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ss := []specialist{}
	sv := []service{}
	ex := []extra{}
	rows, e := a.db.Query(ctx, "SELECT id,name,bio,active FROM specialists WHERE active ORDER BY id")
	if e != nil {
		dbError(w, e)
		return
	}
	ss, e = pgx.CollectRows(rows, pgx.RowToStructByPos[specialist])
	if e != nil {
		dbError(w, e)
		return
	}
	rows, e = a.db.Query(ctx, "SELECT id,name,description,prices,active FROM services WHERE active ORDER BY id")
	if e != nil {
		dbError(w, e)
		return
	}
	sv, e = pgx.CollectRows(rows, pgx.RowToStructByPos[service])
	if e != nil {
		dbError(w, e)
		return
	}
	rows, e = a.db.Query(ctx, "SELECT id,name,price,minutes,active FROM extras WHERE active ORDER BY id")
	if e != nil {
		dbError(w, e)
		return
	}
	ex, e = pgx.CollectRows(rows, pgx.RowToStructByPos[extra])
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, map[string]any{"specialists": ss, "services": sv, "extras": ex, "timezone": a.tz.String(), "currency": env("CURRENCY", "KGS"), "telegram_bot": a.botUsername()})
}

type selection struct {
	SpecialistID int64     `json:"specialist_id"`
	ServiceID    int64     `json:"service_id"`
	Duration     int       `json:"duration"`
	ExtraIDs     []int64   `json:"extra_ids"`
	StartsAt     time.Time `json:"starts_at"`
	Name         string    `json:"name"`
	Phone        string    `json:"phone"`
	Consent      bool      `json:"consent"`
}

func (a *app) quote(ctx context.Context, tx pgx.Tx, in selection) (string, []extra, int, int, error) {
	var name string
	var prices map[string]int
	e := tx.QueryRow(ctx, "SELECT name,prices FROM services WHERE id=$1 AND active FOR SHARE", in.ServiceID).Scan(&name, &prices)
	if e != nil {
		return "", nil, 0, 0, errors.New("Услуга недоступна")
	}
	price, ok := prices[strconv.Itoa(in.Duration)]
	if !ok || !validDuration(in.Duration) {
		return "", nil, 0, 0, errors.New("Выберите доступную длительность")
	}
	extras := []extra{}
	minutes := in.Duration
	seen := map[int64]bool{}
	if len(in.ExtraIDs) > 20 {
		return "", nil, 0, 0, errors.New("Слишком много дополнительных опций")
	}
	for _, id := range in.ExtraIDs {
		if seen[id] {
			return "", nil, 0, 0, errors.New("Повторяющаяся опция")
		}
		seen[id] = true
		var x extra
		e = tx.QueryRow(ctx, "SELECT id,name,price,minutes,active FROM extras WHERE id=$1 AND active FOR SHARE", id).Scan(&x.ID, &x.Name, &x.Price, &x.Minutes, &x.Active)
		if e != nil {
			return "", nil, 0, 0, errors.New("Дополнительная опция недоступна")
		}
		extras = append(extras, x)
		price += x.Price
		minutes += x.Minutes
	}
	return name, extras, price, minutes, nil
}
func validDuration(n int) bool { return n == 60 || n == 90 || n == 120 }
func (a *app) available(ctx context.Context, tx pgx.Tx, sid int64, start, end time.Time, exclude int64) bool {
	if start.Before(time.Now().Add(30*time.Minute)) || start.After(time.Now().AddDate(0, 0, 90)) {
		return false
	}
	local := start.In(a.tz)
	if local.Minute()%15 != 0 || local.Second() != 0 || local.Nanosecond() != 0 {
		return false
	}
	var active bool
	if tx.QueryRow(ctx, "SELECT active FROM specialists WHERE id=$1 FOR UPDATE", sid).Scan(&active) != nil || !active {
		return false
	}
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM shifts WHERE specialist_id=$1 AND starts_at<=$2 AND ends_at>=$3) AND NOT EXISTS(SELECT 1 FROM bookings WHERE specialist_id=$1 AND id<>$4 AND status<>'cancelled' AND tstzrange(starts_at,ends_at,'[)') && tstzrange($2::timestamptz,$3::timestamptz,'[)'))`, sid, start, end, exclude).Scan(&ok)
	return e == nil && ok
}
func (a *app) slots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	date, e := time.ParseInLocation("2006-01-02", q.Get("date"), a.tz)
	if e != nil {
		fail(w, 400, "Укажите дату")
		return
	}
	in := selection{SpecialistID: parseID(q.Get("specialist_id")), ServiceID: parseID(q.Get("service_id")), Duration: int(parseID(q.Get("duration")))}
	for _, s := range strings.Split(q.Get("extras"), ",") {
		if s != "" {
			in.ExtraIDs = append(in.ExtraIDs, parseID(s))
		}
	}
	ctx := r.Context()
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	exclude := int64(0)
	var minutes int
	if t := q.Get("token"); t != "" {
		var b booking
		b, e = scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.token=$1", t))
		if e != nil || b.Status != "confirmed" {
			fail(w, 404, "Запись недоступна")
			return
		}
		in.SpecialistID = b.SpecialistID
		exclude = b.ID
		minutes = int(b.EndsAt.Sub(b.StartsAt).Minutes())
	} else {
		_, _, _, minutes, e = a.quote(ctx, tx, in)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
	}
	out := []time.Time{}
	var active bool
	if tx.QueryRow(ctx, "SELECT active FROM specialists WHERE id=$1", in.SpecialistID).Scan(&active) != nil || !active {
		respond(w, 200, out)
		return
	}
	rows, e := tx.Query(ctx, `SELECT slot FROM shifts, LATERAL generate_series(GREATEST(starts_at,$2::timestamptz), LEAST(ends_at - make_interval(mins=>$4),$3::timestamptz-interval '15 minutes'), interval '15 minutes') AS slot WHERE specialist_id=$1 AND ends_at>$2 AND starts_at<$3 AND slot>=now()+interval '30 minutes' AND slot<=now()+interval '90 days' AND NOT EXISTS(SELECT 1 FROM bookings WHERE specialist_id=$1 AND id<>$5 AND status<>'cancelled' AND tstzrange(starts_at,ends_at,'[)') && tstzrange(slot,slot+make_interval(mins=>$4),'[)')) ORDER BY slot`, in.SpecialistID, date, date.AddDate(0, 0, 1), minutes, exclude)
	if e != nil {
		dbError(w, e)
		return
	}
	out, e = pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, out)
}
func (a *app) book(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.allow("book", 60) {
		fail(w, 429, "Слишком много запросов. Попробуйте позже.")
		return
	}
	var in selection
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Phone = strings.TrimSpace(in.Phone)
	if !in.Consent || len(in.Name) < 2 || len(in.Name) > 100 || len(in.Phone) < 7 || len(in.Phone) > 30 {
		fail(w, 400, "Укажите имя, телефон и согласие на обработку данных")
		return
	}
	ctx := r.Context()
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	name, extras, total, minutes, e := a.quote(ctx, tx, in)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	end := in.StartsAt.Add(time.Duration(minutes) * time.Minute)
	if !a.available(ctx, tx, in.SpecialistID, in.StartsAt, end, 0) {
		fail(w, 409, "Это время недоступно. Обновите свободные окна.")
		return
	}
	raw, _ := json.Marshal(extras)
	var id int64
	e = tx.QueryRow(ctx, `INSERT INTO bookings(token,specialist_id,service_id,service_name,duration,extras,total,starts_at,ends_at,name,phone) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, token(), in.SpecialistID, in.ServiceID, name, in.Duration, raw, total, in.StartsAt, end, in.Name, in.Phone).Scan(&id)
	if e != nil {
		dbError(w, e)
		return
	}
	b, e := scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.id=$1", id))
	if e == nil {
		e = enqueue(ctx, tx, b, "confirmation")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 201, b)
}
func (a *app) manage(w http.ResponseWriter, r *http.Request) {
	t := r.PathValue("token")
	if !validToken(t) {
		fail(w, 404, "Запись не найдена")
		return
	}
	b, e := scanBooking(a.db.QueryRow(r.Context(), bookingSelect+"WHERE b.token=$1", t))
	if e != nil {
		fail(w, 404, "Запись не найдена")
		return
	}
	respond(w, 200, b)
}
func (a *app) cancel(w http.ResponseWriter, r *http.Request)     { a.change(w, r, false) }
func (a *app) reschedule(w http.ResponseWriter, r *http.Request) { a.change(w, r, true) }
func (a *app) change(w http.ResponseWriter, r *http.Request, reschedule bool) {
	ctx := r.Context()
	var in struct {
		StartsAt time.Time `json:"starts_at"`
	}
	if reschedule && !decode(w, r, &in) {
		return
	}
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	b, e := scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.token=$1 FOR UPDATE OF b", r.PathValue("token")))
	if e != nil {
		fail(w, 404, "Запись не найдена")
		return
	}
	if !reschedule && b.Status == "cancelled" {
		respond(w, 200, b)
		return
	}
	if b.Status != "confirmed" || !b.StartsAt.After(time.Now()) {
		fail(w, 409, "Изменить прошедшую или закрытую запись нельзя")
		return
	}
	kind := "cancellation"
	if reschedule {
		end := in.StartsAt.Add(b.EndsAt.Sub(b.StartsAt))
		if !a.available(ctx, tx, b.SpecialistID, in.StartsAt, end, b.ID) {
			fail(w, 409, "Выбранное время уже недоступно")
			return
		}
		b.StartsAt = in.StartsAt
		b.EndsAt = end
		kind = "rescheduled"
	} else {
		b.Status = "cancelled"
	}
	b.Version++
	_, e = tx.Exec(ctx, "UPDATE bookings SET starts_at=$2,ends_at=$3,status=$4,version=$5 WHERE id=$1", b.ID, b.StartsAt, b.EndsAt, b.Status, b.Version)
	if e == nil {
		e = enqueue(ctx, tx, b, kind)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, b)
}
func enqueue(ctx context.Context, tx pgx.Tx, b booking, kind string) error {
	_, e := tx.Exec(ctx, "DELETE FROM jobs WHERE booking_id=$1 AND sent_at IS NULL", b.ID)
	if e != nil {
		return e
	}
	for _, ch := range []string{"telegram"} {
		if ch == "telegram" && b.TelegramChat == nil {
			continue
		}
		if _, e = tx.Exec(ctx, "INSERT INTO jobs(booking_id,version,kind,channel,due_at) VALUES($1,$2,$3,$4,now())", b.ID, b.Version, kind, ch); e != nil {
			return e
		}
		if b.Status != "confirmed" {
			continue
		}
		for _, hours := range []int{24, 2} {
			due := b.StartsAt.Add(-time.Duration(hours) * time.Hour)
			if due.After(time.Now()) {
				if _, e = tx.Exec(ctx, "INSERT INTO jobs(booking_id,version,kind,channel,due_at) VALUES($1,$2,$3,$4,$5)", b.ID, b.Version, fmt.Sprintf("reminder_%dh", hours), ch, due); e != nil {
					return e
				}
			}
		}
	}
	return nil
}
