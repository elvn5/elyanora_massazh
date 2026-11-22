package main

import (
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

type shift struct {
	ID           int64     `json:"id"`
	SpecialistID int64     `json:"specialist_id"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	date, e := time.ParseInLocation("2006-01-02", r.URL.Query().Get("date"), a.tz)
	if e != nil {
		date = time.Now().In(a.tz)
		date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, a.tz)
	}
	rows, e := a.db.Query(ctx, bookingSelect+"WHERE b.starts_at >= $1 AND b.starts_at < $2 ORDER BY b.starts_at", date, date.AddDate(0, 0, 1))
	if e != nil {
		dbError(w, e)
		return
	}
	bs := []booking{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			rows.Close()
			dbError(w, err)
			return
		}
		b.Token = ""
		bs = append(bs, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		dbError(w, e)
		return
	}
	rows, e = a.db.Query(ctx, "SELECT id,specialist_id,starts_at,ends_at FROM shifts WHERE ends_at>$1 AND starts_at<$2 ORDER BY starts_at", date, date.AddDate(0, 0, 1))
	if e != nil {
		dbError(w, e)
		return
	}
	shifts, e := pgx.CollectRows(rows, pgx.RowToStructByPos[shift])
	if e != nil {
		dbError(w, e)
		return
	}
	var confirmed, cancelled, completed, noShow, revenue, failed int
	e = a.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='confirmed'), count(*) FILTER(WHERE status='cancelled'),count(*) FILTER(WHERE status='completed'),count(*) FILTER(WHERE status='no_show'),COALESCE(sum(total) FILTER(WHERE status='completed'),0) FROM bookings WHERE starts_at>=$1 AND starts_at<$2`, date, date.AddDate(0, 0, 1)).Scan(&confirmed, &cancelled, &completed, &noShow, &revenue)
	if e != nil {
		dbError(w, e)
		return
	}
	if e = a.db.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE sent_at IS NULL AND last_error IS NOT NULL").Scan(&failed); e != nil {
		dbError(w, e)
		return
	}
	catalog := map[string]json.RawMessage{}
	for _, kind := range []string{"specialists", "services", "extras"} {
		var raw []byte
		e = a.db.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(t ORDER BY id),'[]') FROM "+kind+" t").Scan(&raw)
		if e != nil {
			dbError(w, e)
			return
		}
		catalog[kind] = raw
	}
	weekly, err := a.weeklyMetrics(ctx, date)
	if err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"weekly": weekly, "bookings": bs, "shifts": shifts, "catalog": catalog, "stats": map[string]int{"confirmed": confirmed, "cancelled": cancelled, "completed": completed, "no_show": noShow, "revenue": revenue, "failed_notifications": failed}})
}
func (a *app) saveCatalog(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	var in struct {
		ID          int64          `json:"id"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Bio         string         `json:"bio"`
		Prices      map[string]int `json:"prices"`
		Price       int            `json:"price"`
		Minutes     int            `json:"minutes"`
		Active      bool           `json:"active"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Name) < 2 || len(in.Name) > 100 || len(in.Description) > 1000 || len(in.Bio) > 1000 {
		fail(w, 400, "Название: от 2 до 100 символов. Описание: до 1000.")
		return
	}
	ctx := r.Context()
	var id int64
	var e error
	switch kind {
	case "specialists":
		if in.ID == 0 {
			e = a.db.QueryRow(ctx, "INSERT INTO specialists(name,bio,active) VALUES($1,$2,$3) RETURNING id", in.Name, in.Bio, in.Active).Scan(&id)
		} else {
			e = a.db.QueryRow(ctx, "UPDATE specialists SET name=$2,bio=$3,active=$4 WHERE id=$1 RETURNING id", in.ID, in.Name, in.Bio, in.Active).Scan(&id)
		}
	case "services":
		if len(in.Prices) == 0 {
			fail(w, 400, "Добавьте хотя бы одну длительность")
			return
		}
		for k, v := range in.Prices {
			if (k != "60" && k != "90" && k != "120") || v < 0 || v > 1000000 {
				fail(w, 400, "Допустимы 60, 90, 120 минут и цена от 0 до 1 000 000")
				return
			}
		}
		if in.ID == 0 {
			e = a.db.QueryRow(ctx, "INSERT INTO services(name,description,prices,active) VALUES($1,$2,$3,$4) RETURNING id", in.Name, in.Description, in.Prices, in.Active).Scan(&id)
		} else {
			e = a.db.QueryRow(ctx, "UPDATE services SET name=$2,description=$3,prices=$4,active=$5 WHERE id=$1 RETURNING id", in.ID, in.Name, in.Description, in.Prices, in.Active).Scan(&id)
		}
	case "extras":
		if in.Price < 0 || in.Price > 1000000 || in.Minutes < 0 || in.Minutes > 60 || in.Minutes%15 != 0 {
			fail(w, 400, "Цена от 0 до 1 000 000; время от 0 до 60 минут, шаг 15")
			return
		}
		if in.ID == 0 {
			e = a.db.QueryRow(ctx, "INSERT INTO extras(name,price,minutes,active) VALUES($1,$2,$3,$4) RETURNING id", in.Name, in.Price, in.Minutes, in.Active).Scan(&id)
		} else {
			e = a.db.QueryRow(ctx, "UPDATE extras SET name=$2,price=$3,minutes=$4,active=$5 WHERE id=$1 RETURNING id", in.ID, in.Name, in.Price, in.Minutes, in.Active).Scan(&id)
		}
	default:
		fail(w, 404, "Раздел не найден")
		return
	}
	if e == pgx.ErrNoRows {
		fail(w, 404, "Объект не найден")
		return
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, map[string]int64{"id": id})
}
func (a *app) addShift(w http.ResponseWriter, r *http.Request) {
	var in shift
	if !decode(w, r, &in) {
		return
	}
	if !in.EndsAt.After(in.StartsAt) || in.EndsAt.Sub(in.StartsAt) > 24*time.Hour || in.StartsAt.In(a.tz).Minute()%15 != 0 || in.EndsAt.In(a.tz).Minute()%15 != 0 || in.StartsAt.Second() != 0 || in.EndsAt.Second() != 0 || in.StartsAt.Nanosecond() != 0 || in.EndsAt.Nanosecond() != 0 {
		fail(w, 400, "Укажите рабочий интервал до 24 часов с шагом 15 минут")
		return
	}
	ctx := r.Context()
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	var id int64
	if e = tx.QueryRow(ctx, "SELECT id FROM specialists WHERE id=$1 FOR UPDATE", in.SpecialistID).Scan(&id); e != nil {
		fail(w, 400, "Специалист не найден")
		return
	}
	e = tx.QueryRow(ctx, "INSERT INTO shifts(specialist_id,starts_at,ends_at) VALUES($1,$2,$3) RETURNING id", in.SpecialistID, in.StartsAt, in.EndsAt).Scan(&id)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 201, map[string]int64{"id": id})
}
func (a *app) deleteShift(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	var s shift
	e = tx.QueryRow(ctx, "SELECT id,specialist_id,starts_at,ends_at FROM shifts WHERE id=$1", parseID(r.PathValue("id"))).Scan(&s.ID, &s.SpecialistID, &s.StartsAt, &s.EndsAt)
	if e != nil {
		fail(w, 404, "Интервал не найден")
		return
	}
	var id int64
	if e = tx.QueryRow(ctx, "SELECT id FROM specialists WHERE id=$1 FOR UPDATE", s.SpecialistID).Scan(&id); e != nil {
		dbError(w, e)
		return
	}
	var occupied bool
	e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM bookings WHERE specialist_id=$1 AND status<>'cancelled' AND tstzrange(starts_at,ends_at,'[)') && tstzrange($2::timestamptz,$3::timestamptz,'[)'))", s.SpecialistID, s.StartsAt, s.EndsAt).Scan(&occupied)
	if e != nil {
		dbError(w, e)
		return
	}
	if occupied {
		fail(w, 409, "В интервале есть записи. Сначала отмените или перенесите их.")
		return
	}
	_, e = tx.Exec(ctx, "DELETE FROM shifts WHERE id=$1", s.ID)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (a *app) status(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Status != "cancelled" && in.Status != "completed" && in.Status != "no_show" {
		fail(w, 400, "Недопустимый статус")
		return
	}
	ctx := r.Context()
	tx, e := a.db.Begin(ctx)
	if e != nil {
		dbError(w, e)
		return
	}
	defer tx.Rollback(ctx)
	b, e := scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.id=$1 FOR UPDATE OF b", parseID(r.PathValue("id"))))
	if e != nil {
		fail(w, 404, "Запись не найдена")
		return
	}
	if b.Status != "confirmed" {
		fail(w, 409, "Запись уже закрыта")
		return
	}
	if in.Status != "cancelled" && b.EndsAt.After(time.Now()) {
		fail(w, 409, "Сеанс ещё не завершился")
		return
	}
	b.Status = in.Status
	b.Version++
	_, e = tx.Exec(ctx, "UPDATE bookings SET status=$2,version=$3 WHERE id=$1", b.ID, b.Status, b.Version)
	if e == nil {
		if b.Status == "cancelled" {
			e = enqueue(ctx, tx, b, "cancellation")
		} else {
			_, e = tx.Exec(ctx, "DELETE FROM jobs WHERE booking_id=$1 AND sent_at IS NULL", b.ID)
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		dbError(w, e)
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
