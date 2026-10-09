package eventbot

import (
	"strconv"
	"time"
)

// The sales end and doors-open questions of a date (migration 0128), asked
// right after its start time. Both are kept as OFFSETS from the start, not
// as instants: the venue — and so the time zone — is only chosen later in
// the wizard, and a moved start keeps "sales close an hour after the start".
//
//   - DraftSession.SalesEndMin: minutes after the start ticket sales close
//     (0 = at the start, the default; negative = before it).
//   - DraftSession.DoorsMin: minutes before the start the doors open
//     (0 = not given).

// maxDoorsMin bounds a typed doors-open time: more than 12 hours before the
// start is a typo, not a door policy.
const maxDoorsMin = 12 * 60

// clockAt adds minutes to an "HH:MM" wall-clock value and prints the result
// as "HH:MM" (it wraps past midnight, which the buttons say in words).
func clockAt(hhmm string, minutes int) string {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return hhmm
	}
	return t.Add(time.Duration(minutes) * time.Minute).Format("15:04") // allow:timeformat: wall-clock label of a bot button
}

// minutesOfDay is "HH:MM" as minutes after midnight, -1 when it is not one.
func minutesOfDay(hhmm string) int {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return -1
	}
	return t.Hour()*60 + t.Minute()
}

// salesEndOffset turns a typed "HH:MM" into minutes after the start. The
// time counts on the event's day; a time more than twelve hours before the
// start is read as the NEXT day (a 22:00 show selling until 01:00).
func salesEndOffset(start, typed string) (int, bool) {
	s, v := minutesOfDay(start), minutesOfDay(typed)
	if s < 0 || v < 0 {
		return 0, false
	}
	off := v - s
	if off < -12*60 {
		off += 24 * 60
	}
	return off, true
}

// doorsOffset turns a typed "HH:MM" into minutes before the start; the
// doors must open at or before the start, at most twelve hours earlier.
func doorsOffset(start, typed string) (int, bool) {
	s, v := minutesOfDay(start), minutesOfDay(typed)
	if s < 0 || v < 0 {
		return 0, false
	}
	before := s - v
	if before < 0 {
		before += 24 * 60 // doors the evening before a show after midnight
	}
	if before <= 0 || before > maxDoorsMin {
		return 0, false
	}
	return before, true
}

// salesEndLabel is how a date's sales end reads in the wizard.
func salesEndLabel(t func(string, map[string]any) string, s *DraftSession) string {
	if s.SalesEndMin == 0 {
		return t("bot.wz.sales_end_at_start", map[string]any{"Time": s.Time})
	}
	return clockAt(s.Time, s.SalesEndMin)
}

// doorsLabel is how a date's doors-open time reads, "" when not given.
func doorsLabel(s *DraftSession) string {
	if s.DoorsMin <= 0 {
		return ""
	}
	return clockAt(s.Time, -s.DoorsMin)
}

// saleTimesOnWire converts the offsets into the bundle's RFC 3339 instants
// for a start in loc. doors is nil when the date has no doors time.
func saleTimesOnWire(start time.Time, s DraftSession) (sellEnd string, doors *string) {
	sellEnd = start.Add(time.Duration(s.SalesEndMin) * time.Minute).Format(time.RFC3339)
	if s.DoorsMin > 0 {
		v := start.Add(-time.Duration(s.DoorsMin) * time.Minute).Format(time.RFC3339)
		doors = &v
	}
	return sellEnd, doors
}

// offsetData is a button payload for an offset in minutes.
func offsetData(prefix string, minutes int) string { return prefix + strconv.Itoa(minutes) }

// parseOffsetData reads a payload made by offsetData.
func parseOffsetData(prefix, data string) (int, bool) {
	if len(data) <= len(prefix) || data[:len(prefix)] != prefix {
		return 0, false
	}
	n, err := strconv.Atoi(data[len(prefix):])
	if err != nil {
		return 0, false
	}
	return n, true
}
