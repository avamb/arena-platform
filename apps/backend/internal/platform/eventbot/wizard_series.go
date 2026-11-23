package eventbot

// wizard_series.go - the optional "Tour" step of the "+ Event" wizard (spec 63,
// L-2). It sits after the brand question and is asked only when the
// organization has active tours; "Skip" leaves the event without one. The tour
// is sent as series_id on PATCH right after the first date is saved
// (Wizard.Save), because the event does not exist before that.

import (
	"context"
	"strings"
)

// seriesSource is the wizard's tour list, nil when its reference source has none.
func (w *Wizard) seriesSource() SeriesSource {
	src, _ := w.refs.(SeriesSource)
	return src
}

// activeSeriesFor lists the tours the wizard may offer. A failed call is the
// same as "no tours": the step is optional and must never block a new event.
func (w *Wizard) activeSeriesFor(ctx context.Context, ws WizSession) []SeriesItem {
	src := w.seriesSource()
	if src == nil {
		return nil
	}
	items, err := src.ActiveSeries(ctx, ws.JWT, ws.OrgID)
	if err != nil {
		return nil
	}
	return items
}

// afterPromoter moves on from the brand question: to the tour question of a NEW
// event whose organization has tours, otherwise straight to the poster.
func (w *Wizard) afterPromoter(ctx context.Context, ws WizSession, d *Draft) {
	if d.Mode == ModeCreate && !d.singleEdit() && len(w.activeSeriesFor(ctx, ws)) > 0 {
		d.goTo(stEvSeries)
		return
	}
	d.Event.SeriesID, d.Event.SeriesName = "", ""
	d.next(stEvPoster)
}

// applySeries answers the tour question: "ser:<id>" binds, "skip" does not.
func (w *Wizard) applySeries(ctx context.Context, ws WizSession, d *Draft, data string) error {
	switch {
	case data == "skip":
		d.Event.SeriesID, d.Event.SeriesName = "", ""
		d.next(stEvPoster)
	case strings.HasPrefix(data, "ser:"):
		id := strings.TrimPrefix(data, "ser:")
		for _, s := range w.activeSeriesFor(ctx, ws) {
			if s.ID == id {
				d.Event.SeriesID, d.Event.SeriesName = s.ID, s.Name
				d.next(stEvPoster)
				return nil
			}
		}
	}
	return nil
}

// seriesButtons are the tour question's rows: one button per tour, then Skip.
func seriesButtons(items []SeriesItem, current, skipLabel string) [][]Button {
	rows := make([][]Button, 0, len(items)+1)
	for _, s := range items {
		mark := ""
		if s.ID == current {
			mark = "✔ "
		}
		rows = append(rows, []Button{{Label: mark + truncate(s.Name, 48), Data: "ser:" + s.ID}})
	}
	return append(rows, []Button{{Label: skipLabel, Data: "skip"}})
}

// linkNewEventSeries binds a just-created event to the draft's tour. A refusal
// is not fatal: the event exists, so the organizer is told to bind the tour on
// the event card instead. It is tried once per draft (Saved.SeriesDone).
func (w *Wizard) linkNewEventSeries(ctx context.Context, io SaveIO, ws WizSession, d *Draft) {
	if d.Mode != ModeCreate || d.Event.SeriesID == "" || d.Saved.SeriesDone || d.Saved.EventID == "" {
		return
	}
	linker, ok := io.(SeriesLinker)
	if !ok {
		return
	}
	d.Saved.SeriesDone = true
	if err := linker.LinkEventSeries(ctx, ws.JWT, ws.OrgID, d.Saved.EventID, d.Event.SeriesID); err != nil {
		if text := w.texts.T(ws.Locale, "bot.ser.warn_not_linked", nil); text != "" && !contains(d.Saved.Warnings, text) {
			d.Saved.Warnings = append(d.Saved.Warnings, text)
		}
	}
}
