package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type telegramUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
}
type telegramMessage struct {
	Text string       `json:"text"`
	From telegramUser `json:"from"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Contact *struct {
		Phone  string `json:"phone_number"`
		UserID int64  `json:"user_id"`
	} `json:"contact"`
}
type telegramUpdate struct {
	ID       int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
	Callback *struct {
		ID      string           `json:"id"`
		From    telegramUser     `json:"from"`
		Data    string           `json:"data"`
		Message *telegramMessage `json:"message"`
	} `json:"callback_query"`
}
type botButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type botReply struct {
	Text           string        `json:"text"`
	Buttons        [][]botButton `json:"buttons,omitempty"`
	Contact        bool          `json:"contact,omitempty"`
	RemoveKeyboard bool          `json:"remove_keyboard,omitempty"`
}
type botSession struct {
	Step           string    `json:"step"`
	Revision       string    `json:"revision"`
	Selection      selection `json:"selection"`
	BookingID      int64     `json:"booking_id"`
	BookingVersion int       `json:"booking_version"`
	Page           int       `json:"page"`
	Date           string    `json:"date"`
	Total          int       `json:"total"`
	Minutes        int       `json:"minutes"`
	Summary        string    `json:"summary"`
	Catalog        bool      `json:"catalog"`
}

func button(text, data string) []botButton { return []botButton{{text, data}} }
func (s botSession) cb(action, arg string) string {
	return "w:" + s.Revision + ":" + action + ":" + arg
}
func menuReply(text string, services bool) botReply {
	r := botReply{Text: text, Buttons: [][]botButton{button("Записаться", "new"), button("Мои записи", "mine")}}
	if services {
		r.Buttons = append(r.Buttons, button("Услуги и цены", "catalog"))
	}
	r.Buttons = append(r.Buttons, button("Контакты студии", "contacts"))
	return r
}
func (u telegramUpdate) identity() (int64, string, string, bool) {
	if u.Callback != nil {
		m := u.Callback.Message
		if m != nil && m.Chat.Type == "private" && m.Chat.ID == u.Callback.From.ID {
			return m.Chat.ID, "", u.Callback.Data, true
		}
	} else if m := u.Message; m != nil && m.Chat.Type == "private" && m.Chat.ID == m.From.ID {
		return m.Chat.ID, strings.TrimSpace(m.Text), "", true
	}
	return 0, "", "", false
}

// The session, booking mutation, jobs and update receipt commit together.
func (a *app) processBotUpdate(ctx context.Context, u telegramUpdate) (botReply, error) {
	chat, text, data, ok := u.identity()
	if !ok {
		return botReply{}, nil
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return botReply{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "INSERT INTO bot_sessions(chat_id) VALUES($1) ON CONFLICT DO NOTHING", chat); err != nil {
		return botReply{}, err
	}
	var s botSession
	var updated time.Time
	if err = tx.QueryRow(ctx, "SELECT data,updated_at FROM bot_sessions WHERE chat_id=$1 FOR UPDATE", chat).Scan(&s, &updated); err != nil {
		return botReply{}, err
	}
	var reply botReply
	err = tx.QueryRow(ctx, "SELECT reply FROM bot_updates WHERE update_id=$1 AND chat_id=$2", u.ID, chat).Scan(&reply)
	if err == nil {
		return reply, nil
	}
	if err != pgx.ErrNoRows {
		return reply, err
	}
	if time.Since(updated) > 24*time.Hour {
		s = botSession{}
	}
	settings, err := readBotSettings(ctx, tx, false)
	if err != nil {
		return reply, err
	}
	previousStep := s.Step
	reply, err = a.advanceBot(ctx, tx, u, chat, text, data, &s, settings)
	if err != nil {
		return botReply{}, err
	}
	reply.RemoveKeyboard = previousStep == "phone" && !reply.Contact
	if !settings.ServicesEnabled {
		filtered := [][]botButton{}
		for _, row := range reply.Buttons {
			buttons := []botButton{}
			for _, b := range row {
				if b.Data != "catalog" {
					buttons = append(buttons, b)
				}
			}
			if len(buttons) > 0 {
				filtered = append(filtered, buttons)
			}
		}
		reply.Buttons = filtered
	}
	if _, err = tx.Exec(ctx, "UPDATE bot_sessions SET data=$2,updated_at=now() WHERE chat_id=$1", chat, s); err != nil {
		return reply, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO bot_updates(update_id,chat_id,reply) VALUES($1,$2,$3)", u.ID, chat, reply); err != nil {
		return reply, err
	}
	return reply, tx.Commit(ctx)
}
func (a *app) advanceBot(ctx context.Context, tx pgx.Tx, u telegramUpdate, chat int64, text, data string, s *botSession, settings botSettings) (botReply, error) {
	command := strings.Fields(text)
	if len(command) > 0 {
		switch strings.Split(command[0], "@")[0] {
		case "/start", "/menu", "/cancel":
			*s = botSession{}
			if len(command) == 2 && validToken(command[1]) {
				b, e := scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.token=$1 AND b.status='confirmed' AND b.starts_at>now() FOR UPDATE OF b", command[1]))
				if e != nil && e != pgx.ErrNoRows {
					return botReply{}, e
				}
				if e == nil && (b.TelegramChat == nil || *b.TelegramChat == chat) {
					if b.TelegramChat == nil {
						if _, e = tx.Exec(ctx, "UPDATE bookings SET telegram_chat=$2 WHERE id=$1", b.ID, chat); e != nil {
							return botReply{}, e
						}
						b.TelegramChat = &chat
						if e = enqueue(ctx, tx, b, "confirmation"); e != nil {
							return botReply{}, e
						}
					}
					return a.bookingReply(b), nil
				}
				return menuReply("Эта запись недоступна. Откройте «Мои записи» или оформите новую.", settings.ServicesEnabled), nil
			}
			return menuReply(botWelcome(settings, a.base), settings.ServicesEnabled), nil
		case "/book":
			data = "new"
		case "/bookings":
			data = "mine"
		case "/services":
			data = "catalog"
		}
	}
	switch text {
	case "Главное меню":
		data = "menu"
	case "Записаться":
		data = "new"
	case "Мои записи":
		data = "mine"
	case "← Назад":
		return a.botBack(ctx, tx, chat, s, settings)
	}
	switch data {
	case "menu":
		*s = botSession{}
		return menuReply(botWelcome(settings, a.base), settings.ServicesEnabled), nil
	case "contacts":
		return menuReply(contactText(settings), settings.ServicesEnabled), nil
	case "new":
		*s = botSession{Step: "service"}
	case "catalog":
		if !settings.ServicesEnabled {
			return menuReply("Просмотр каталога сейчас отключён. Для записи нажмите «Записаться».", false), nil
		}
		*s = botSession{Step: "catalog", Catalog: true}
	case "mine":
		*s = botSession{Step: "mine"}
	default:
		if strings.HasPrefix(data, "view:") {
			b, e := a.ownedBooking(ctx, tx, chat, parseID(strings.TrimPrefix(data, "view:")), false)
			if e == pgx.ErrNoRows {
				return menuReply("Запись не найдена.", settings.ServicesEnabled), nil
			}
			if e != nil {
				return botReply{}, e
			}
			*s = botSession{}
			return a.bookingReply(b), nil
		}
		if strings.HasPrefix(data, "move:") || strings.HasPrefix(data, "cancel:") {
			parts := strings.Split(data, ":")
			if len(parts) != 3 {
				return menuReply("Кнопка недействительна.", settings.ServicesEnabled), nil
			}
			b, e := a.ownedBooking(ctx, tx, chat, parseID(parts[1]), false)
			if e != nil && e != pgx.ErrNoRows {
				return botReply{}, e
			}
			if e != nil || b.Status != "confirmed" || !b.StartsAt.After(time.Now()) || b.Version != int(parseID(parts[2])) {
				return menuReply("Запись изменилась или уже закрыта. Откройте «Мои записи».", settings.ServicesEnabled), nil
			}
			*s = botSession{Step: "date", BookingID: b.ID, BookingVersion: b.Version, Minutes: int(b.EndsAt.Sub(b.StartsAt).Minutes()), Total: b.Total, Selection: selection{SpecialistID: b.SpecialistID}}
			if parts[0] == "cancel" {
				s.Step = "cancel"
			}
		} else if strings.HasPrefix(data, "w:") {
			p := strings.Split(data, ":")
			if len(p) != 4 || s.Revision == "" || p[1] != s.Revision {
				return botReply{Text: "Эта кнопка устарела. Используйте последнее сообщение или начните заново.", Buttons: [][]botButton{button("Главное меню", "menu"), button("Мои записи", "mine")}}, nil
			}
			action, arg := p[2], p[3]
			if action == "back" {
				return a.botBack(ctx, tx, chat, s, settings)
			}
			if action == "page" {
				n, e := strconv.Atoi(arg)
				if e == nil && n >= 0 && n <= 10000 {
					s.Page = n
				}
			}
			switch s.Step {
			case "service":
				if action == "service" {
					s.Selection.ServiceID = parseID(arg)
					s.Step = "duration"
					s.Page = 0
				}
			case "catalog":
				if action == "service" {
					s.Selection.ServiceID = parseID(arg)
					s.Step = "duration"
					s.Page = 0
				}
			case "duration":
				if action == "duration" {
					s.Selection.Duration = int(parseID(arg))
					s.Step = "extras"
					s.Page = 0
				}
			case "extras":
				if action == "extra" {
					id := parseID(arg)
					found := false
					for i, x := range s.Selection.ExtraIDs {
						if x == id {
							s.Selection.ExtraIDs = append(s.Selection.ExtraIDs[:i], s.Selection.ExtraIDs[i+1:]...)
							found = true
							break
						}
					}
					if !found && len(s.Selection.ExtraIDs) < 20 {
						s.Selection.ExtraIDs = append(s.Selection.ExtraIDs, id)
					}
				}
				if action == "next" {
					s.Step = "master"
					s.Page = 0
				}
			case "master":
				if action == "master" {
					s.Selection.SpecialistID = parseID(arg)
					s.Step = "date"
					s.Page = 0
				}
			case "date":
				if action == "date" {
					if _, e := time.Parse("2006-01-02", arg); e == nil {
						s.Date = arg
						s.Step = "time"
						s.Page = 0
					}
				}
			case "time":
				if action == "dates" {
					s.Step = "date"
					s.Page = 0
				}
				if action == "time" {
					n, e := strconv.ParseInt(arg, 10, 64)
					if e == nil {
						s.Selection.StartsAt = time.Unix(n, 0)
						s.Page = 0
						if s.BookingID != 0 {
							s.Step = "review"
						} else {
							s.Step = "name"
						}
					}
				}
			case "name":
				if action == "name" && u.Callback != nil {
					v := strings.TrimSpace(u.Callback.From.FirstName)
					if validClientName(v) {
						s.Selection.Name = v
						s.Step = "phone"
					}
				}
			case "review":
				if action == "confirm" {
					return a.confirmBotBooking(ctx, tx, chat, s)
				}
				if action == "dates" {
					s.Step = "date"
					s.Page = 0
				}
			case "cancel":
				if action == "confirm" {
					return a.cancelBotBooking(ctx, tx, chat, s)
				}
			}
		} else if data == "" {
			switch s.Step {
			case "name":
				if validClientName(text) {
					s.Selection.Name = text
					s.Step = "phone"
				} else {
					return botReply{Text: "Введите имя: от 2 до 100 символов.", Buttons: [][]botButton{button("← Назад", s.cb("back", "")), button("Главное меню", "menu")}}, nil
				}
			case "phone":
				phone := text
				if u.Message != nil && u.Message.Contact != nil {
					if u.Message.Contact.UserID != chat {
						return botReply{Text: "Отправьте свой контакт или введите номер вручную.", Contact: true}, nil
					}
					phone = u.Message.Contact.Phone
				}
				if validPhone(phone) {
					s.Selection.Phone = phone
					s.Step = "review"
				} else {
					return botReply{Text: "Введите телефон с кодом страны, например +996700000000, или нажмите «Отправить мой телефон».", Contact: true}, nil
				}
			default:
				return menuReply("Выберите действие с помощью кнопок.", settings.ServicesEnabled), nil
			}
		}
	}
	s.Revision = token()[:12]
	return a.renderBotStep(ctx, tx, chat, s, u)
}
func validClientName(s string) bool { return validName(s, 100) }
func validPhone(s string) bool {
	digits := 0
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits++
		} else if !strings.ContainsRune("+ ()-", r) {
			return false
		}
	}
	return digits >= 7 && digits <= 15 && len(s) <= 30
}
func contactText(s botSettings) string {
	if s.ContactText != "" {
		return s.ContactText
	}
	return "Elyanora — студия массажа. Контактная информация пока не добавлена администратором."
}
func (a *app) ownedBooking(ctx context.Context, tx pgx.Tx, chat, id int64, lock bool) (booking, error) {
	q := bookingSelect + "WHERE b.id=$1 AND b.telegram_chat=$2"
	if lock {
		q += " FOR UPDATE OF b"
	}
	return scanBooking(tx.QueryRow(ctx, q, id, chat))
}
func (a *app) bookingReply(b booking) botReply {
	status := map[string]string{"confirmed": "Подтверждена", "cancelled": "Отменена", "completed": "Завершена", "no_show": "Неявка"}[b.Status]
	r := botReply{Text: fmt.Sprintf("Запись №%d · %s\n\n%s\nМастер: %s\n%s (%s)\nДлительность: %d мин\nСтоимость: %d %s", b.ID, status, b.Service, b.Specialist, b.StartsAt.In(a.tz).Format("02.01.2006 в 15:04"), a.tz, int(b.EndsAt.Sub(b.StartsAt).Minutes()), b.Total, env("CURRENCY", "KGS"))}
	if len(b.Extras) > 0 {
		r.Text += "\nДополнения: "
		for i, x := range b.Extras {
			if i > 0 {
				r.Text += ", "
			}
			r.Text += x.Name
		}
	}
	if b.Status == "confirmed" && b.StartsAt.After(time.Now()) {
		key := fmt.Sprintf("%d:%d", b.ID, b.Version)
		r.Buttons = append(r.Buttons, button("Перенести", "move:"+key), button("Отменить запись", "cancel:"+key))
	}
	r.Buttons = append(r.Buttons, button("Мои записи", "mine"), button("Главное меню", "menu"))
	return r
}
func paginate(rows [][]botButton, s botSession) [][]botButton {
	const size = 8
	start := min(s.Page*size, len(rows))
	end := min(start+size, len(rows))
	out := append([][]botButton{}, rows[start:end]...)
	nav := []botButton{}
	if start > 0 {
		nav = append(nav, botButton{"← Назад", s.cb("page", strconv.Itoa(max(0, s.Page-1)))})
	}
	if end < len(rows) {
		nav = append(nav, botButton{"Далее →", s.cb("page", strconv.Itoa(s.Page+1))})
	}
	if len(nav) > 0 {
		out = append(out, nav)
	}
	return out
}
func shortened(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (a *app) renderBotStep(ctx context.Context, tx pgx.Tx, chat int64, s *botSession, u telegramUpdate) (botReply, error) {
	r := botReply{}
	rowsOut := [][]botButton{}
	switch s.Step {
	case "service", "catalog":
		rows, e := tx.Query(ctx, "SELECT id,name FROM services WHERE active ORDER BY id")
		if e != nil {
			return r, e
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var name string
			if e = rows.Scan(&id, &name); e != nil {
				return r, e
			}
			rowsOut = append(rowsOut, button(name, s.cb("service", strconv.FormatInt(id, 10))))
		}
		if e = rows.Err(); e != nil {
			return r, e
		}
		r.Text = "Выберите услугу. Цены и длительность появятся на следующем шаге."
		if len(rowsOut) == 0 {
			r.Text = "Пока нет услуг для записи. Попробуйте позже или откройте контакты студии."
		}
		r.Buttons = paginate(rowsOut, *s)
	case "duration":
		var name, description string
		var prices map[string]int
		e := tx.QueryRow(ctx, "SELECT name,description,prices FROM services WHERE id=$1 AND active", s.Selection.ServiceID).Scan(&name, &description, &prices)
		if e == pgx.ErrNoRows {
			return menuReply("Услуга больше недоступна. Выберите другую.", true), nil
		}
		if e != nil {
			return r, e
		}
		r.Text = name + "\n" + description + "\n\nВыберите длительность:"
		for _, d := range []int{60, 90, 120} {
			if price, ok := prices[strconv.Itoa(d)]; ok {
				r.Buttons = append(r.Buttons, button(fmt.Sprintf("%d мин · %d %s", d, price, env("CURRENCY", "KGS")), s.cb("duration", strconv.Itoa(d))))
			}
		}
	case "extras":
		// Validate the selected duration before moving through the rest of the wizard.
		_, _, _, _, e := a.quote(ctx, tx, s.Selection)
		if e != nil {
			return menuReply(e.Error()+". Начните запись заново.", true), nil
		}
		rows, e := tx.Query(ctx, "SELECT id,name,price,minutes,active FROM extras WHERE active ORDER BY id")
		if e != nil {
			return r, e
		}
		xs, e := pgx.CollectRows(rows, pgx.RowToStructByPos[extra])
		if e != nil {
			return r, e
		}
		r.Text = "Дополните сеанс по желанию. Выберите опции и нажмите «Продолжить»."
		for _, x := range xs {
			mark := "▫️"
			for _, id := range s.Selection.ExtraIDs {
				if id == x.ID {
					mark = "✓"
				}
			}
			label := fmt.Sprintf("%s %s · +%d %s", mark, x.Name, x.Price, env("CURRENCY", "KGS"))
			if x.Minutes > 0 {
				label += fmt.Sprintf(" · +%d мин", x.Minutes)
			}
			rowsOut = append(rowsOut, button(label, s.cb("extra", strconv.FormatInt(x.ID, 10))))
		}
		if len(xs) == 0 {
			r.Text = "Дополнительных опций нет. Нажмите «Продолжить»."
		}
		r.Buttons = paginate(rowsOut, *s)
		r.Buttons = append(r.Buttons, button("Продолжить", s.cb("next", "")))
	case "master":
		rows, e := tx.Query(ctx, "SELECT id,name,bio,active FROM specialists WHERE active ORDER BY id")
		if e != nil {
			return r, e
		}
		xs, e := pgx.CollectRows(rows, pgx.RowToStructByPos[specialist])
		if e != nil {
			return r, e
		}
		r.Text = "Выберите специалиста:"
		for _, x := range xs {
			rowsOut = append(rowsOut, button(x.Name, s.cb("master", strconv.FormatInt(x.ID, 10))))
		}
		if len(xs) == 0 {
			r.Text = "Пока нет доступных специалистов. Попробуйте позже."
		}
		r.Buttons = paginate(rowsOut, *s)
	case "date", "time":
		slots, e := a.botSlots(ctx, tx, chat, s)
		if e != nil {
			var userErr botInputError
			if errors.As(e, &userErr) {
				return menuReply(e.Error(), true), nil
			}
			return r, e
		}
		if s.Step == "date" {
			r.Text = "Выберите дату. Показываем только дни со свободными окнами (" + a.tz.String() + ")."
			seen := map[string]bool{}
			for _, v := range slots {
				day := v.In(a.tz).Format("2006-01-02")
				if !seen[day] {
					rowsOut = append(rowsOut, button(v.In(a.tz).Format("02.01.2006"), s.cb("date", day)))
					seen[day] = true
				}
			}
		} else {
			r.Text = "Выберите время на " + s.Date + " (" + a.tz.String() + ")."
			for _, v := range slots {
				if v.In(a.tz).Format("2006-01-02") == s.Date {
					rowsOut = append(rowsOut, button(v.In(a.tz).Format("15:04"), s.cb("time", strconv.FormatInt(v.Unix(), 10))))
				}
			}
		}
		if len(rowsOut) == 0 {
			r.Text = "Свободных окон пока нет. Выберите другую дату или начните запись с другим специалистом."
		}
		if s.Step == "time" {
			grid := [][]botButton{}
			for i, row := range rowsOut {
				if i%3 == 0 {
					grid = append(grid, []botButton{})
				}
				grid[len(grid)-1] = append(grid[len(grid)-1], row...)
			}
			rowsOut = grid
		}
		r.Buttons = paginate(rowsOut, *s)
		if s.Step == "time" {
			r.Buttons = append(r.Buttons, button("← Другие даты", s.cb("dates", "")))
		}
		r.Buttons = append(r.Buttons, button("Выбрать другую услугу / мастера", "new"))
	case "name":
		r.Text = "Как к вам обращаться? Напишите имя сообщением."
		name := s.Selection.Name
		if name == "" && u.Callback != nil {
			name = u.Callback.From.FirstName
		} else if name == "" && u.Message != nil {
			name = u.Message.From.FirstName
		}
		if validClientName(name) {
			r.Buttons = append(r.Buttons, button("Использовать имя: "+shortened(name, 35), s.cb("name", "")))
		}
	case "phone":
		return botReply{Text: "Поделитесь своим телефоном кнопкой ниже или введите номер с кодом страны. Номер нужен студии для связи по вашему визиту.", Contact: true}, nil
	case "review":
		if s.BookingID != 0 {
			b, e := a.ownedBooking(ctx, tx, chat, s.BookingID, false)
			if e != nil && e != pgx.ErrNoRows {
				return r, e
			}
			if e != nil || b.Status != "confirmed" || b.Version != s.BookingVersion {
				return menuReply("Запись изменилась. Откройте «Мои записи».", true), nil
			}
			r.Text = fmt.Sprintf("Перенести запись №%d?\n%s\nМастер: %s\nНовое время: %s (%s)\nСтоимость: %d %s", b.ID, b.Service, b.Specialist, s.Selection.StartsAt.In(a.tz).Format("02.01.2006 в 15:04"), a.tz, b.Total, env("CURRENCY", "KGS"))
		} else {
			name, xs, total, mins, e := a.quote(ctx, tx, s.Selection)
			if e != nil {
				return menuReply(e.Error()+". Начните запись заново.", true), nil
			}
			s.Total = total
			s.Minutes = mins
			var master string
			e = tx.QueryRow(ctx, "SELECT name FROM specialists WHERE id=$1 AND active", s.Selection.SpecialistID).Scan(&master)
			if e == pgx.ErrNoRows {
				return menuReply("Специалист недоступен. Начните запись заново.", true), nil
			}
			if e != nil {
				return r, e
			}
			r.Text = fmt.Sprintf("Проверьте запись\n\n%s\nМастер: %s\n%s (%s)\nПродолжительность: %d мин\nСтоимость: %d %s\nИмя: %s\nТелефон: %s", name, master, s.Selection.StartsAt.In(a.tz).Format("02.01.2006 в 15:04"), a.tz, mins, total, env("CURRENCY", "KGS"), s.Selection.Name, s.Selection.Phone)
			for _, x := range xs {
				r.Text += "\n+ " + x.Name
			}
			r.Text += "\n\nНажимая «Подтвердить запись», вы соглашаетесь на обработку имени, телефона и Telegram ID студией Elyanora для организации визита и уведомлений. Оплата после сеанса."
		}
		s.Summary = r.Text
		r.Buttons = append(r.Buttons, button("Подтвердить запись", s.cb("confirm", "")), button("Выбрать другое время", s.cb("dates", "")))
	case "cancel":
		r.Text = "Отменить запись? Освободившееся время смогут занять другие гости."
		r.Buttons = append(r.Buttons, button("Да, отменить запись", s.cb("confirm", "")), button("Сохранить запись", "view:"+strconv.FormatInt(s.BookingID, 10)))
	case "mine":
		rows, e := tx.Query(ctx, bookingSelect+"WHERE b.telegram_chat=$1 AND b.status='confirmed' AND b.starts_at>now() ORDER BY b.starts_at", chat)
		if e != nil {
			return r, e
		}
		defer rows.Close()
		for rows.Next() {
			b, e := scanBooking(rows)
			if e != nil {
				return r, e
			}
			rowsOut = append(rowsOut, button(b.StartsAt.In(a.tz).Format("02.01 15:04")+" · "+shortened(b.Service, 35), "view:"+strconv.FormatInt(b.ID, 10)))
		}
		if e = rows.Err(); e != nil {
			return r, e
		}
		r.Text = "Ваши предстоящие записи:"
		if len(rowsOut) == 0 {
			r.Text = "У вас пока нет предстоящих записей."
		}
		r.Buttons = paginate(rowsOut, *s)
		r.Buttons = append(r.Buttons, button("Записаться", "new"))
	default:
		return menuReply("Выберите действие:", true), nil
	}
	if s.Step != "" {
		r.Buttons = append(r.Buttons, button("← Назад", s.cb("back", "")))
	}
	r.Buttons = append(r.Buttons, button("Главное меню", "menu"))
	return r, nil
}

func (a *app) botBack(ctx context.Context, tx pgx.Tx, chat int64, s *botSession, settings botSettings) (botReply, error) {
	switch s.Step {
	case "service", "catalog", "mine":
		*s = botSession{}
		return menuReply("Выберите действие.", settings.ServicesEnabled), nil
	case "duration":
		if s.Catalog {
			s.Step = "catalog"
		} else {
			s.Step = "service"
		}
	case "extras":
		s.Step = "duration"
	case "master":
		s.Step = "extras"
	case "date":
		if s.BookingID != 0 {
			b, err := a.ownedBooking(ctx, tx, chat, s.BookingID, false)
			if err == nil {
				*s = botSession{}
				return a.bookingReply(b), nil
			}
			if err != pgx.ErrNoRows {
				return botReply{}, err
			}
			*s = botSession{}
			return menuReply("Запись больше недоступна.", settings.ServicesEnabled), nil
		}
		s.Step = "master"
	case "time":
		s.Step = "date"
		s.Page = 0
	case "name":
		s.Step = "time"
		s.Page = 0
	case "phone":
		s.Step = "name"
	case "review":
		if s.BookingID != 0 {
			s.Step = "time"
		} else {
			s.Step = "phone"
		}
	case "cancel":
		b, err := a.ownedBooking(ctx, tx, chat, s.BookingID, false)
		if err == nil {
			*s = botSession{}
			return a.bookingReply(b), nil
		}
		if err != pgx.ErrNoRows {
			return botReply{}, err
		}
		*s = botSession{}
		return menuReply("Запись больше недоступна.", settings.ServicesEnabled), nil
	default:
		*s = botSession{}
		return menuReply("Выберите действие.", settings.ServicesEnabled), nil
	}
	s.Revision = token()[:12]
	return a.renderBotStep(ctx, tx, chat, s, telegramUpdate{})
}

type botInputError string

func (e botInputError) Error() string { return string(e) }
func (a *app) botSlots(ctx context.Context, tx pgx.Tx, chat int64, s *botSession) ([]time.Time, error) {
	var mins int
	sid := s.Selection.SpecialistID
	if s.BookingID != 0 {
		b, e := a.ownedBooking(ctx, tx, chat, s.BookingID, false)
		if e != nil && e != pgx.ErrNoRows {
			return nil, e
		}
		if e != nil || b.Status != "confirmed" || b.Version != s.BookingVersion || !b.StartsAt.After(time.Now()) {
			return nil, botInputError("Запись изменилась. Откройте «Мои записи».")
		}
		sid = b.SpecialistID
		mins = int(b.EndsAt.Sub(b.StartsAt).Minutes())
	} else {
		_, _, _, m, e := a.quote(ctx, tx, s.Selection)
		if e != nil {
			return nil, botInputError(e.Error())
		}
		mins = m
	}
	rows, e := tx.Query(ctx, `SELECT slot FROM shifts sh JOIN specialists sp ON sp.id=sh.specialist_id, LATERAL generate_series(sh.starts_at,sh.ends_at-make_interval(mins=>$2),interval '15 minutes') slot WHERE sh.specialist_id=$1 AND sp.active AND sh.ends_at>now() AND sh.starts_at<now()+interval '90 days' AND slot>=now()+interval '30 minutes' AND slot<=now()+interval '90 days' AND NOT EXISTS(SELECT 1 FROM bookings b WHERE b.specialist_id=$1 AND b.id<>$3 AND b.status<>'cancelled' AND tstzrange(b.starts_at,b.ends_at,'[)') && tstzrange(slot,slot+make_interval(mins=>$2),'[)')) ORDER BY slot`, sid, mins, s.BookingID)
	if e != nil {
		return nil, e
	}
	return pgx.CollectRows(rows, pgx.RowTo[time.Time])
}
func (a *app) confirmBotBooking(ctx context.Context, tx pgx.Tx, chat int64, s *botSession) (botReply, error) {
	var b booking
	var err error
	if s.BookingID != 0 {
		b, err = a.ownedBooking(ctx, tx, chat, s.BookingID, true)
		if err != nil && err != pgx.ErrNoRows {
			return botReply{}, err
		}
		if err != nil || b.Status != "confirmed" || !b.StartsAt.After(time.Now()) || b.Version != s.BookingVersion {
			return menuReply("Запись изменилась. Откройте «Мои записи».", true), nil
		}
		end := s.Selection.StartsAt.Add(b.EndsAt.Sub(b.StartsAt))
		if !a.available(ctx, tx, b.SpecialistID, s.Selection.StartsAt, end, b.ID) {
			return a.botSlotConflict(ctx, tx, chat, s)
		}
		b.StartsAt = s.Selection.StartsAt
		b.EndsAt = end
		b.Version++
		_, err = tx.Exec(ctx, "UPDATE bookings SET starts_at=$2,ends_at=$3,version=$4 WHERE id=$1", b.ID, b.StartsAt, b.EndsAt, b.Version)
		if err == nil {
			err = enqueue(ctx, tx, b, "rescheduled")
		}
	} else {
		if !validClientName(s.Selection.Name) || !validPhone(s.Selection.Phone) {
			return menuReply("Проверьте имя и телефон. Начните запись заново.", true), nil
		}
		name, xs, total, mins, e := a.quote(ctx, tx, s.Selection)
		if e != nil {
			return menuReply(e.Error()+". Начните запись заново.", true), nil
		}
		if total != s.Total || mins != s.Minutes {
			s.Revision = token()[:12]
			r, e := a.renderBotStep(ctx, tx, chat, s, telegramUpdate{})
			r.Text = "Условия сеанса изменились. Проверьте новую стоимость и подтвердите ещё раз.\n\n" + r.Text
			return r, e
		}
		end := s.Selection.StartsAt.Add(time.Duration(mins) * time.Minute)
		if !a.available(ctx, tx, s.Selection.SpecialistID, s.Selection.StartsAt, end, 0) {
			return a.botSlotConflict(ctx, tx, chat, s)
		}
		raw, _ := json.Marshal(xs)
		var id int64
		err = tx.QueryRow(ctx, `INSERT INTO bookings(token,specialist_id,service_id,service_name,duration,extras,total,starts_at,ends_at,name,phone,telegram_chat) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, token(), s.Selection.SpecialistID, s.Selection.ServiceID, name, s.Selection.Duration, raw, total, s.Selection.StartsAt, end, s.Selection.Name, s.Selection.Phone, chat).Scan(&id)
		if err == nil {
			b, err = scanBooking(tx.QueryRow(ctx, bookingSelect+"WHERE b.id=$1", id))
		}
		if err == nil {
			err = enqueue(ctx, tx, b, "confirmation")
		}
		if err == nil {
			err = enqueueStaffBooking(ctx, tx, b)
		}
	}
	if err != nil {
		return botReply{}, err
	}
	*s = botSession{}
	// Confirmation and reminders are delivered from the durable notification queue.
	return botReply{Text: "Готово! Подтверждение с деталями визита придёт следующим сообщением.", Buttons: [][]botButton{button("Открыть запись", "view:"+strconv.FormatInt(b.ID, 10)), button("Главное меню", "menu")}}, nil
}
func (a *app) botSlotConflict(ctx context.Context, tx pgx.Tx, chat int64, s *botSession) (botReply, error) {
	s.Step = "date"
	s.Page = 0
	s.Revision = token()[:12]
	r, e := a.renderBotStep(ctx, tx, chat, s, telegramUpdate{})
	r.Text = "Это время уже заняли или оно стало недоступно. Выберите другое окно.\n\n" + r.Text
	return r, e
}
func (a *app) cancelBotBooking(ctx context.Context, tx pgx.Tx, chat int64, s *botSession) (botReply, error) {
	b, e := a.ownedBooking(ctx, tx, chat, s.BookingID, true)
	if e != nil && e != pgx.ErrNoRows {
		return botReply{}, e
	}
	if e != nil || b.Status != "confirmed" || b.Version != s.BookingVersion || !b.StartsAt.After(time.Now()) {
		return menuReply("Запись изменилась или уже закрыта. Откройте «Мои записи».", true), nil
	}
	b.Status = "cancelled"
	b.Version++
	_, e = tx.Exec(ctx, "UPDATE bookings SET status='cancelled',version=$2 WHERE id=$1", b.ID, b.Version)
	if e == nil {
		e = enqueue(ctx, tx, b, "cancellation")
	}
	if e != nil {
		return botReply{}, e
	}
	*s = botSession{}
	return a.bookingReply(b), nil
}
