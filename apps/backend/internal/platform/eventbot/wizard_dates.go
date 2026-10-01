package eventbot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Every date question of the wizard (a date of the event, the date a price
// changes, the date a category stops selling) works the same way:
//
//   - the question shows a calendar of buttons; days outside the allowed range
//     are dead cells, so a wrong day cannot be picked at all;
//   - a date typed as text is never taken on trust: it is read as every date it
//     could mean (day and month are easy to swap, "04.05.2026" is the 4th of
//     May or the 5th of April) and the person confirms the one they meant,
//     spelled with the month in words and the weekday.
//
// Everything here is pure (the clock comes from Wizard.now), like the rest of
// the state machine.

// dateStepNames are the steps that ask for a date.
func isDateStep(step string) bool {
	return step == stSDate || step == stTChangeDate || step == stTCatUntil
}

const (
	calCallback    = "cal:" // "cal:2026-11" shows another month
	pickCallback   = "d:"   // "d:2026-11-05" a day was pressed
	confirmPrefix  = "dc:"  // "dc:2026-05-04" a typed date was confirmed
	confirmRetry   = "dc:retry"
	calNoop        = "noop"
	isoLayout      = "2006-01-02"
	calMonthLayout = "2006-01"
)

var monthNames = map[string][12]string{
	"ru": {"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"},
	"en": {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
}

// monthNamesGenitive is "20 октября" (ru); English keeps the same words.
var monthNamesGenitive = map[string][12]string{
	"ru": {"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"},
	"en": monthNames["en"],
}

var weekdayShort = map[string][7]string{ // Monday first
	"ru": {"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"},
	"en": {"Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"},
}

var weekdayLong = map[string][7]string{ // Monday first
	"ru": {"понедельник", "вторник", "среда", "четверг", "пятница", "суббота", "воскресенье"},
	"en": {"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"},
}

func dateLoc(loc string) string {
	if loc == "ru" {
		return "ru"
	}
	return "en"
}

// mondayIndex is 0 for Monday … 6 for Sunday.
func mondayIndex(t time.Time) int { return (int(t.Weekday()) + 6) % 7 }

// DateWords spells a date for a confirmation: "вторник, 20 октября 2026".
func DateWords(loc, iso string) string {
	t, err := time.Parse(isoLayout, iso)
	if err != nil {
		return iso
	}
	l := dateLoc(loc)
	return fmt.Sprintf("%s, %d %s %d", weekdayLong[l][mondayIndex(t)], t.Day(), monthNamesGenitive[l][t.Month()-1], t.Year())
}

// ─── reading a typed date ─────────────────────────────────────────────────────

var (
	numericDate  = regexp.MustCompile(`^(\d{1,2})[\s./,\-]+(\d{1,2})(?:[\s./,\-]+(\d{2}|\d{4}))?$`)
	compactDate  = regexp.MustCompile(`^(\d{2})(\d{2})(\d{2}|\d{4})$`)
	yearFirst    = regexp.MustCompile(`^(\d{4})[\s./,\-]+(\d{1,2})[\s./,\-]+(\d{1,2})$`)
	dayMonthWord = regexp.MustCompile(`^(\d{1,2})(?:-?(?:го|е|th|st|nd|rd))?\s+([a-zа-яё]+)\.?(?:\s+(\d{4}))?$`)
	monthWordDay = regexp.MustCompile(`^([a-z]+)\.?\s+(\d{1,2})(?:th|st|nd|rd)?(?:,?\s+(\d{4}))?$`)
)

var monthPrefixes = map[string]int{
	"янв": 1, "фев": 2, "мар": 3, "апр": 4, "мая": 5, "май": 5, "июн": 6, "июл": 7, "авг": 8, "сен": 9, "окт": 10, "ноя": 11, "дек": 12,
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

func monthFromWord(w string) (int, bool) {
	w = strings.ToLower(strings.TrimSpace(w))
	if len([]rune(w)) < 3 {
		return 0, false
	}
	m, ok := monthPrefixes[string([]rune(w)[:3])]
	return m, ok
}

// DateCandidates lists every calendar date raw can mean, most likely first
// (day.month before month.day). A date with no year is the next such day on
// or after today. It lists nothing when raw is not a date at all.
func DateCandidates(raw string, today time.Time) []string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimRight(s, ".,;:!? \t")
	if s == "" {
		return nil
	}
	day0 := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch s {
	case "сегодня", "today":
		return []string{day0.Format(isoLayout)}
	case "завтра", "tomorrow":
		return []string{day0.AddDate(0, 0, 1).Format(isoLayout)}
	case "послезавтра":
		return []string{day0.AddDate(0, 0, 2).Format(isoLayout)}
	}

	var out []string
	seen := map[string]bool{}
	add := func(y, m, d int, yearGiven bool) {
		if !yearGiven {
			cand := time.Date(day0.Year(), time.Month(m), d, 0, 0, 0, 0, time.UTC)
			if cand.Month() != time.Month(m) || cand.Day() != d {
				return
			}
			if cand.Before(day0) {
				y = day0.Year() + 1
			} else {
				y = day0.Year()
			}
		}
		if y < 100 {
			y += 2000
		}
		if y < 2000 || y > 2100 || m < 1 || m > 12 || d < 1 || d > 31 {
			return
		}
		t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
		if t.Day() != d || int(t.Month()) != m {
			return // 31.02
		}
		iso := t.Format(isoLayout)
		if !seen[iso] {
			seen[iso] = true
			out = append(out, iso)
		}
	}
	atoi := func(v string) int { n, _ := strconv.Atoi(v); return n }
	pairs := func(a, b int, y string) {
		add(atoi(y), b, a, y != "") // day.month
		if a <= 12 && b <= 12 && a != b {
			add(atoi(y), a, b, y != "") // month.day
		}
	}

	if m := yearFirst.FindStringSubmatch(s); m != nil {
		add(atoi(m[1]), atoi(m[2]), atoi(m[3]), true)
		return out
	}
	if m := numericDate.FindStringSubmatch(s); m != nil {
		pairs(atoi(m[1]), atoi(m[2]), m[3])
		return out
	}
	if m := compactDate.FindStringSubmatch(s); m != nil {
		pairs(atoi(m[1]), atoi(m[2]), m[3])
		return out
	}
	if m := dayMonthWord.FindStringSubmatch(s); m != nil {
		if mon, ok := monthFromWord(m[2]); ok {
			add(atoi(m[3]), mon, atoi(m[1]), m[3] != "")
		}
		return out
	}
	if m := monthWordDay.FindStringSubmatch(s); m != nil {
		if mon, ok := monthFromWord(m[1]); ok {
			add(atoi(m[3]), mon, atoi(m[2]), m[3] != "")
		}
		return out
	}
	return nil
}

// ─── the allowed range ────────────────────────────────────────────────────────

// today is the calendar day in the venue's zone (UTC until a venue is known).
func (w *Wizard) today(d *Draft) time.Time {
	now := w.now()
	for _, s := range d.Sessions {
		if s.Timezone != "" {
			if zone, err := time.LoadLocation(s.Timezone); err == nil {
				now = now.In(zone)
			}
			break
		}
	}
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

// lastSessionDate is the day of the latest date of the event, "" when none is
// known yet. A price can change, or a category stop selling, up to that day.
func lastSessionDate(d *Draft) string {
	last := ""
	for _, s := range d.Sessions {
		if s.Date > last {
			last = s.Date
		}
	}
	return last
}

// dateBounds is the range the current date question accepts, inclusive. min
// is never before today; max is "" when the question has no upper limit.
func (w *Wizard) dateBounds(d *Draft) (min, max string) {
	min = w.today(d).Format(isoLayout)
	switch d.Step {
	case stTChangeDate:
		if n := len(d.Tickets.Schedule); n > 0 {
			if prev, err := time.Parse(isoLayout, d.Tickets.Schedule[n-1].From); err == nil {
				if next := prev.AddDate(0, 0, 1).Format(isoLayout); next > min {
					min = next
				}
			}
		}
		max = lastSessionDate(d)
	case stTCatUntil:
		if prev := lastUntil(d.Tickets.Categories); prev > min {
			min = prev
		}
		max = lastSessionDate(d)
	}
	return min, max
}

func inBounds(iso, min, max string) bool {
	return iso >= min && (max == "" || iso <= max)
}

// ─── the answer side ──────────────────────────────────────────────────────────

// dateInput handles the answers peculiar to a date question: month paging,
// a pressed day, a typed date that must be confirmed, and the confirmation.
// handled is true when the answer is fully dealt with (the step stays); when
// false, text is what the step's own code should read — a plain DD.MM.YYYY
// for a chosen date, or the person's own text when it was not a date.
func (w *Wizard) dateInput(loc string, d *Draft, text, data string) (outText string, handled bool, note string) {
	t := func(key string, vals map[string]any) string { return w.texts.T(loc, key, vals) }
	min, max := w.dateBounds(d)
	fmtDate := func(iso string) string { return DisplayDate(iso) }

	// outOfRange explains why a date is not allowed.
	outOfRange := func(iso string) string {
		switch {
		case iso < w.today(d).Format(isoLayout):
			return t("bot.wz.err_date_past", nil)
		case iso < min:
			return t("bot.wz.err_date_early", map[string]any{"Date": fmtDate(min)})
		default:
			return t("bot.wz.err_date_late", map[string]any{"Date": fmtDate(max)})
		}
	}

	switch {
	case data == calNoop:
		return "", true, ""

	case strings.HasPrefix(data, calCallback):
		if _, err := time.Parse(calMonthLayout, strings.TrimPrefix(data, calCallback)); err == nil {
			d.Scratch.CalMonth = strings.TrimPrefix(data, calCallback)
		}
		return "", true, ""

	case strings.HasPrefix(data, pickCallback):
		iso := strings.TrimPrefix(data, pickCallback)
		if _, err := time.Parse(isoLayout, iso); err != nil {
			return "", true, ""
		}
		if !inBounds(iso, min, max) {
			return "", true, outOfRange(iso)
		}
		d.Scratch.DatePending, d.Scratch.CalMonth = nil, ""
		return fmtDate(iso), false, ""

	case data == confirmRetry:
		d.Scratch.DatePending, d.Scratch.DateRaw, d.Scratch.DateLimit = nil, "", 0
		return "", true, ""

	case strings.HasPrefix(data, confirmPrefix):
		iso := strings.TrimPrefix(data, confirmPrefix)
		ok := false
		for _, c := range d.Scratch.DatePending {
			ok = ok || c == iso
		}
		if !ok || !inBounds(iso, min, max) {
			return "", true, ""
		}
		out := fmtDate(iso)
		if d.Scratch.DateLimit > 0 {
			out += " " + strconv.Itoa(d.Scratch.DateLimit)
		}
		d.Scratch.DatePending, d.Scratch.DateRaw, d.Scratch.DateLimit, d.Scratch.CalMonth = nil, "", 0, ""
		return out, false, ""

	case data == "" && text != "":
		// A typed answer. "a count only" is a valid answer of the category
		// question and is not a date at all.
		if d.Step == stTCatUntil && countShape.MatchString(text) {
			return text, false, ""
		}
		today := w.today(d)
		whole := DateCandidates(text, today)
		limit := 0
		cands := whole
		if d.Step == stTCatUntil && !anyInBounds(cands, min, max) {
			// "31.12.2026 50": a date and a count.
			if head, n, ok := splitDateAndCount(text); ok {
				if c := DateCandidates(head, today); anyInBounds(c, min, max) {
					cands, limit = c, n
				}
			}
		}
		if len(cands) == 0 {
			return text, false, "" // not a date: the step answers with its own hint
		}
		var ok []string
		for _, c := range cands {
			if inBounds(c, min, max) {
				ok = append(ok, c)
			}
		}
		if len(ok) == 0 {
			return "", true, outOfRange(cands[0])
		}
		d.Scratch.DatePending, d.Scratch.DateRaw, d.Scratch.DateLimit = ok, text, limit
		return "", true, ""
	}
	return text, false, ""
}

func anyInBounds(cands []string, min, max string) bool {
	for _, c := range cands {
		if inBounds(c, min, max) {
			return true
		}
	}
	return false
}

var trailingCount = regexp.MustCompile(`^(.+?)[\s;]+(\d{1,3}(?:[  ,]\d{3})+|\d+)$`)

// splitDateAndCount cuts "31.12.2026 50" into the date part and the count.
func splitDateAndCount(raw string) (head string, n int, ok bool) {
	m := trailingCount.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil || !countShape.MatchString(m[2]) {
		return "", 0, false
	}
	n, ok = ParseCount(m[2])
	return m[1], n, ok
}

// ─── the screens ──────────────────────────────────────────────────────────────

// dateConfirmScreen asks which of the dates a typed answer could mean is the
// right one.
func (w *Wizard) dateConfirmScreen(loc string, d *Draft, nav func(rows ...[]Button) [][]Button) (string, [][]Button) {
	t := func(key string, vals map[string]any) string { return w.texts.T(loc, key, vals) }
	cands := d.Scratch.DatePending
	var text string
	rows := [][]Button{}
	suffix := ""
	if d.Scratch.DateLimit > 0 {
		suffix = " · " + t("bot.wz.date_limit_suffix", map[string]any{"N": d.Scratch.DateLimit})
	}
	if len(cands) == 1 {
		text = t("bot.wz.date_confirm_one", map[string]any{"Raw": Esc(d.Scratch.DateRaw), "Date": DateWords(loc, cands[0]) + suffix})
	} else {
		text = t("bot.wz.date_confirm_many", map[string]any{"Raw": Esc(d.Scratch.DateRaw)})
	}
	for _, c := range cands {
		rows = append(rows, []Button{{Label: "✔ " + DateWords(loc, c), Data: confirmPrefix + c}})
	}
	rows = append(rows, []Button{{Label: t("bot.wz.date_retry_btn", nil), Data: confirmRetry}})
	return text, nav(rows...)
}

// calendarRows draws one month of buttons. Days outside [min, max] are dead
// cells; the arrows are dead when the neighbouring month has nothing to pick.
func (w *Wizard) calendarRows(loc string, d *Draft, min, max string) [][]Button {
	t := func(key string, vals map[string]any) string { return w.texts.T(loc, key, vals) }
	l := dateLoc(loc)
	minT, _ := time.Parse(isoLayout, min)
	month := time.Date(minT.Year(), minT.Month(), 1, 0, 0, 0, 0, time.UTC)
	if m, err := time.Parse(calMonthLayout, d.Scratch.CalMonth); err == nil {
		month = m
	}
	if month.Before(time.Date(minT.Year(), minT.Month(), 1, 0, 0, 0, 0, time.UTC)) {
		month = time.Date(minT.Year(), minT.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	if max != "" {
		if maxT, err := time.Parse(isoLayout, max); err == nil && month.After(maxT) {
			month = time.Date(maxT.Year(), maxT.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}
	noop := func(label string) Button { return Button{Label: label, Data: calNoop} }
	prev := month.AddDate(0, -1, 0)
	next := month.AddDate(0, 1, 0)
	prevBtn, nextBtn := noop("·"), noop("·")
	if prev.AddDate(0, 1, -1).Format(isoLayout) >= min {
		prevBtn = Button{Label: "‹", Data: calCallback + prev.Format(calMonthLayout)}
	}
	if max == "" || next.Format(isoLayout) <= max {
		nextBtn = Button{Label: "›", Data: calCallback + next.Format(calMonthLayout)}
	}
	rows := [][]Button{{prevBtn, noop(fmt.Sprintf("%s %d", capitalize(monthNames[l][month.Month()-1]), month.Year())), nextBtn}}
	head := make([]Button, 7)
	for i, n := range weekdayShort[l] {
		head[i] = noop(n)
	}
	rows = append(rows, head)

	week := make([]Button, 0, 7)
	for i := 0; i < mondayIndex(month); i++ {
		week = append(week, noop(" "))
	}
	for day := month; day.Month() == month.Month(); day = day.AddDate(0, 0, 1) {
		iso := day.Format(isoLayout)
		if inBounds(iso, min, max) {
			week = append(week, Button{Label: strconv.Itoa(day.Day()), Data: pickCallback + iso})
		} else {
			week = append(week, noop("·"))
		}
		if len(week) == 7 {
			rows = append(rows, week)
			week = make([]Button, 0, 7)
		}
	}
	if len(week) > 0 {
		for len(week) < 7 {
			week = append(week, noop(" "))
		}
		rows = append(rows, week)
	}

	// Shortcuts that fall inside the range.
	today := w.today(d)
	quick := []Button{}
	for _, q := range []struct {
		key  string
		days int
	}{{"bot.wz.cal_today", 0}, {"bot.wz.cal_tomorrow", 1}, {"bot.wz.cal_week", 7}, {"bot.wz.cal_month", 30}} {
		iso := today.AddDate(0, 0, q.days).Format(isoLayout)
		if inBounds(iso, min, max) {
			quick = append(quick, Button{Label: t(q.key, nil), Data: pickCallback + iso})
		}
	}
	if len(quick) > 0 {
		rows = append(rows, quick)
	}
	return rows
}

func capitalize(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// dateQuestion builds a date question: the question text, the calendar and
// the person's typed answer waiting for a confirmation when there is one.
func (w *Wizard) dateQuestion(loc string, d *Draft, head, question string, nav func(rows ...[]Button) [][]Button) Screen {
	t := func(key string, vals map[string]any) string { return w.texts.T(loc, key, vals) }
	if len(d.Scratch.DatePending) > 0 {
		text, rows := w.dateConfirmScreen(loc, d, nav)
		return Screen{Text: head + text, Buttons: rows}
	}
	min, max := w.dateBounds(d)
	if max != "" && min > max {
		return Screen{Text: head + question + "\n\n" + t("bot.wz.cal_no_days", map[string]any{"Min": DisplayDate(min), "Max": DisplayDate(max)}), Buttons: nav()}
	}
	text := head + question + "\n\n" + t("bot.wz.cal_hint", map[string]any{"Min": DisplayDate(min)})
	if max != "" {
		text += " " + t("bot.wz.cal_range", map[string]any{"Min": DisplayDate(min), "Max": DisplayDate(max)})
	}
	return Screen{Text: text, Buttons: nav(w.calendarRows(loc, d, min, max)...)}
}
