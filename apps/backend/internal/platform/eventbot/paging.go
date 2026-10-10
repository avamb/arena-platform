package eventbot

// paging.go — the one paging helper of the bot's lists (spec 35 §4.2): five
// rows a page, "‹ 2/5 ›" under them, and callback payloads that name a row by
// its INDEX ON THE PAGE, never by a UUID. Telegram caps callback_data at 64
// bytes; a UUID plus a prefix already uses 40 of them, and a payload that
// carries one is also a payload a stale message can aim at the wrong row. The
// screen that renders a page therefore stores the ids it showed (in its
// bot_dialogs state) and resolves "open row 3" against them.
//
// The events list (events_list.go) is the first user; the orders screens use
// the same three pieces: PagerRow under the rows, ItemCallback on each row,
// ParseIndex when the press arrives.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
)

// listPageSize is how many rows one page of a list holds (spec 35 §4.2).
const listPageSize = 5

// Pager is where a list stands: Prefix is the callback prefix a page press
// carries ("el:p" gives "el:p:3").
type Pager struct {
	Prefix string
	Page   int // 1-based, already clamped by PageOf
	Pages  int // at least 1
}

// PagerRow is the "‹ 2/5 ›" row. The middle button only shows the position
// (its press is ignored); « and » are present only where there is a page to
// go to, so a one-page list has no row at all (nil).
func PagerRow(p Pager, prevLabel, nextLabel string) []models.InlineKeyboardButton {
	if p.Pages <= 1 {
		return nil
	}
	var row []models.InlineKeyboardButton
	if p.Page > 1 {
		row = append(row, models.InlineKeyboardButton{Text: "‹ " + prevLabel, CallbackData: pageCallback(p.Prefix, p.Page-1)})
	}
	row = append(row, models.InlineKeyboardButton{Text: fmt.Sprintf("%d/%d", p.Page, p.Pages), CallbackData: calNoop})
	if p.Page < p.Pages {
		row = append(row, models.InlineKeyboardButton{Text: nextLabel + " ›", CallbackData: pageCallback(p.Prefix, p.Page+1)})
	}
	return row
}

func pageCallback(prefix string, page int) string {
	return prefix + ":" + strconv.Itoa(page)
}

// ItemCallback is the payload of the button on row i (0-based) of the page.
func ItemCallback(prefix string, i int) string {
	return prefix + ":" + strconv.Itoa(i)
}

// ParseIndex reads the trailing number of a payload ("3" of "el:o:3") and
// checks it is a row of a page that shows n rows.
func ParseIndex(s string, n int) (int, bool) {
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || i < 0 || i >= n {
		return 0, false
	}
	return i, true
}

// ParsePage reads a page number from a payload; anything that is not a
// positive number answers 1.
func ParsePage(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 {
		return 1
	}
	return n
}
