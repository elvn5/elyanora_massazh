package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// likePattern turns free text into an escaped ILIKE "contains" pattern.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// digits keeps only 0-9 so "+996 (700) 12-34" matches a stored "+996700123400".
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func pageParams(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	if offset < 0 || offset > 1_000_000 {
		offset = 0
	}
	return limit, offset
}

// searchBookings lists bookings across any period with text search and filters.
// GET /api/admin/bookings?q=&status=&specialist_id=&service_id=&from=YYYY-MM-DD&to=YYYY-MM-DD&sort=asc|desc&limit=&offset=
func (a *app) searchBookings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{"TRUE"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		if utf8Len(text) > 100 {
			fail(w, 400, "Слишком длинный поисковый запрос")
			return
		}
		cond := []string{"b.name ILIKE " + arg(likePattern(text)), "b.service_name ILIKE " + arg(likePattern(text)), "s.name ILIKE " + arg(likePattern(text))}
		if d := digits(text); len(d) >= 3 {
			cond = append(cond, "regexp_replace(b.phone,'\\D','','g') LIKE "+arg("%"+d+"%"))
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(text, "#"), 10, 64); err == nil {
			cond = append(cond, "b.id = "+arg(id))
		}
		where = append(where, "("+strings.Join(cond, " OR ")+")")
	}
	if phone := digits(q.Get("phone")); phone != "" {
		where = append(where, "regexp_replace(b.phone,'\\D','','g') = "+arg(phone))
	}
	if st := q.Get("status"); st != "" {
		if _, ok := map[string]bool{"confirmed": true, "cancelled": true, "completed": true, "no_show": true}[st]; !ok {
			fail(w, 400, "Недопустимый статус")
			return
		}
		where = append(where, "b.status = "+arg(st))
	}
	if id := parseID(q.Get("specialist_id")); id > 0 {
		where = append(where, "b.specialist_id = "+arg(id))
	}
	if id := parseID(q.Get("service_id")); id > 0 {
		where = append(where, "b.service_id = "+arg(id))
	}
	for _, bound := range []struct{ key, op string }{{"from", ">="}, {"to", "<"}} {
		v := q.Get(bound.key)
		if v == "" {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", v, a.tz)
		if err != nil {
			fail(w, 400, "Укажите дату в формате ГГГГ-ММ-ДД")
			return
		}
		if bound.key == "to" {
			day = day.AddDate(0, 0, 1) // inclusive end date
		}
		where = append(where, "b.starts_at "+bound.op+" "+arg(day))
	}
	order := "ASC"
	if q.Get("sort") == "desc" {
		order = "DESC"
	}
	limit, offset := pageParams(r)
	ctx := r.Context()
	cond := strings.Join(where, " AND ")
	var total, revenue int
	if err := a.db.QueryRow(ctx, "SELECT count(*), COALESCE(sum(b.total) FILTER(WHERE b.status='completed'),0) FROM bookings b JOIN specialists s ON s.id=b.specialist_id WHERE "+cond, args...).Scan(&total, &revenue); err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.db.Query(ctx, bookingSelect+"WHERE "+cond+" ORDER BY b.starts_at "+order+", b.id LIMIT "+arg(limit)+" OFFSET "+arg(offset), args...)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []booking{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			dbError(w, err)
			return
		}
		b.Token = ""
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "total": total, "revenue": revenue})
}

type client struct {
	Phone     string     `json:"phone"`
	Name      string     `json:"name"`
	Bookings  int        `json:"bookings"`
	Completed int        `json:"completed"`
	Cancelled int        `json:"cancelled"`
	NoShow    int        `json:"no_show"`
	Spent     int        `json:"spent"`
	FirstAt   time.Time  `json:"first_at"`
	LastVisit *time.Time `json:"last_visit"`
	NextVisit *time.Time `json:"next_visit"`
	Telegram  bool       `json:"telegram"`
}

// searchClients groups bookings by phone number into client cards.
// GET /api/admin/clients?q=&segment=all|upcoming|regular|telegram|no_show|lapsed&sort=recent|visits|spent|name&limit=&offset=
func (a *app) searchClients(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	having := []string{"TRUE"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if text := strings.TrimSpace(q.Get("q")); text != "" {
		if utf8Len(text) > 100 {
			fail(w, 400, "Слишком длинный поисковый запрос")
			return
		}
		cond := []string{"bool_or(b.name ILIKE " + arg(likePattern(text)) + ")"}
		if d := digits(text); len(d) >= 3 {
			cond = append(cond, "c.key LIKE "+arg("%"+d+"%"))
		}
		having = append(having, "("+strings.Join(cond, " OR ")+")")
	}
	switch q.Get("segment") {
	case "", "all":
	case "upcoming":
		having = append(having, "bool_or(b.status='confirmed' AND b.starts_at>now())")
	case "regular":
		having = append(having, "count(*) FILTER(WHERE b.status='completed') >= 3")
	case "telegram":
		having = append(having, "bool_or(b.telegram_chat IS NOT NULL)")
	case "no_show":
		having = append(having, "count(*) FILTER(WHERE b.status='no_show') > 0")
	case "lapsed":
		having = append(having, "NOT bool_or(b.status='confirmed' AND b.starts_at>now()) AND max(b.starts_at) FILTER(WHERE b.status='completed') < now()-interval '60 days'")
	default:
		fail(w, 400, "Неизвестный сегмент клиентов")
		return
	}
	order := map[string]string{
		"":       "max(b.starts_at) DESC",
		"recent": "max(b.starts_at) DESC",
		"visits": "count(*) FILTER(WHERE b.status='completed') DESC, max(b.starts_at) DESC",
		"spent":  "COALESCE(sum(b.total) FILTER(WHERE b.status='completed'),0) DESC, max(b.starts_at) DESC",
		"name":   "lower((array_agg(b.name ORDER BY b.starts_at DESC))[1]) ASC",
	}[q.Get("sort")]
	if order == "" {
		fail(w, 400, "Неизвестная сортировка")
		return
	}
	limit, offset := pageParams(r)
	grouped := `FROM bookings b CROSS JOIN LATERAL (SELECT regexp_replace(b.phone,'\D','','g') AS key) c GROUP BY c.key HAVING ` + strings.Join(having, " AND ")
	ctx := r.Context()
	var total int
	if err := a.db.QueryRow(ctx, "SELECT count(*) FROM (SELECT 1 "+grouped+") t", args...).Scan(&total); err != nil {
		dbError(w, err)
		return
	}
	rows, err := a.db.Query(ctx, `SELECT (array_agg(b.phone ORDER BY b.starts_at DESC))[1], (array_agg(b.name ORDER BY b.starts_at DESC))[1],
		count(*), count(*) FILTER(WHERE b.status='completed'), count(*) FILTER(WHERE b.status='cancelled'), count(*) FILTER(WHERE b.status='no_show'),
		COALESCE(sum(b.total) FILTER(WHERE b.status='completed'),0), min(b.starts_at),
		max(b.starts_at) FILTER(WHERE b.status='completed'), min(b.starts_at) FILTER(WHERE b.status='confirmed' AND b.starts_at>now()),
		bool_or(b.telegram_chat IS NOT NULL) `+grouped+" ORDER BY "+order+" LIMIT "+arg(limit)+" OFFSET "+arg(offset), args...)
	if err != nil {
		dbError(w, err)
		return
	}
	defer rows.Close()
	items := []client{}
	for rows.Next() {
		var c client
		if err := rows.Scan(&c.Phone, &c.Name, &c.Bookings, &c.Completed, &c.Cancelled, &c.NoShow, &c.Spent, &c.FirstAt, &c.LastVisit, &c.NextVisit, &c.Telegram); err != nil {
			dbError(w, err)
			return
		}
		items = append(items, c)
	}
	if err := rows.Err(); err != nil {
		dbError(w, err)
		return
	}
	respond(w, 200, map[string]any{"items": items, "total": total})
}

func utf8Len(s string) int { return len([]rune(s)) }
