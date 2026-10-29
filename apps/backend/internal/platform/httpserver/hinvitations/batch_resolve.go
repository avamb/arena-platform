package hinvitations

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// PlanSeat is one assigned seat of the session as the upload addresses it.
type PlanSeat struct {
	ID     uuid.UUID
	Key    string
	Sector string
	Row    string
	Seat   string
	Status string
	// TierID is the seat's category; nil means the seat has none and the
	// issue transaction refuses it (invitation.seat_without_category).
	TierID *uuid.UUID
}

// PlanTier is one category of the session. GA is true for a category that
// owns General Admission places (they are addressed by category and qty).
type PlanTier struct {
	ID        uuid.UUID
	Name      string
	GA        bool
	Available int
	Reserve   int
}

// Plan is the session as the upload is resolved against: the plan's seats
// by name and the categories with their General Admission places.
type Plan struct {
	Seats []PlanSeat
	Tiers []PlanTier
	// CapacityLeft is what the session's ledger still has for places (the
	// check ReserveCapacity makes); nil means no limit.
	CapacityLeft *int
}

// RowIssue is one problem of one row of the upload (Row 0: the file as a
// whole). Row is the file line a person finds in a spreadsheet.
type RowIssue struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BatchGuest is one guest of the upload: the rows with the same guest
// number become ONE order and ONE letter.
type BatchGuest struct {
	// Seq is the guest's 1-based position in the file.
	Seq      int
	Lines    []int
	Name     string
	Email    string
	Phone    string
	Locale   string
	Note     string
	Delivery string
	SeatKeys []string
	GA       []GALine
}

// Resolution is an upload resolved against the session's plan. Issues is
// empty exactly when the upload can be issued.
type Resolution struct {
	Guests      []BatchGuest
	Issues      []RowIssue
	Rows        int
	Seats       int
	GAPlaces    int
	FromReserve int
	// SeatLines maps a seat_key to the file line that named it, so a refusal
	// of the transaction can point at the row.
	SeatLines map[string]int
}

// Places is the number of tickets the upload hands out.
func (r Resolution) Places() int { return r.Seats + r.GAPlaces }

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ResolveBatch resolves every row against the plan: the place named by
// seat_key, by sector/row/seat or by category and qty, the guest data of the
// first row of each guest, and the delivery. defaultDelivery is the
// batch-wide mode for a guest without an e-mail (my_email or pdf_only), or
// guest_email, which makes a missing address a row error. It writes nothing.
func ResolveBatch(rows []csvRow, plan Plan, defaultDelivery string) Resolution {
	res := Resolution{Rows: len(rows), SeatLines: map[string]int{}}
	issue := func(line int, field, code, msg string) {
		res.Issues = append(res.Issues, RowIssue{Row: line, Field: field, Code: code, Message: msg})
	}

	byKey := make(map[string]*PlanSeat, len(plan.Seats))
	byNames := map[[3]string][]*PlanSeat{}
	byRowSeat := map[[2]string][]*PlanSeat{}
	for i := range plan.Seats {
		s := &plan.Seats[i]
		byKey[s.Key] = s
		k3 := [3]string{norm(s.Sector), norm(s.Row), norm(s.Seat)}
		byNames[k3] = append(byNames[k3], s)
		k2 := [2]string{k3[1], k3[2]}
		byRowSeat[k2] = append(byRowSeat[k2], s)
	}
	tierByID := map[uuid.UUID]*PlanTier{}
	tierByName := map[string][]*PlanTier{}
	for i := range plan.Tiers {
		t := &plan.Tiers[i]
		tierByID[t.ID] = t
		tierByName[norm(t.Name)] = append(tierByName[norm(t.Name)], t)
	}
	freeLeft := map[uuid.UUID]int{}
	reserveLeft := map[uuid.UUID]int{}
	for _, t := range plan.Tiers {
		freeLeft[t.ID], reserveLeft[t.ID] = t.Available, t.Reserve
	}

	seatLine := map[uuid.UUID]int{} // plan seat -> the row that took it
	guestAt := map[string]int{}     // guest key -> index in res.Guests
	gaAt := map[string]int{}        // "guest/tier" -> index in that guest's GA

	for _, row := range rows {
		key := guestKey(row.Guest)
		if key == "" {
			key = "line:" + strconv.Itoa(row.Line)
		}
		gi, known := guestAt[key]
		if !known {
			g := BatchGuest{
				Seq: len(res.Guests) + 1, Name: row.Name, Email: row.Email, Phone: row.Phone,
				Locale: normalizeLocale(row.Locale), Note: row.Note,
			}
			switch {
			case g.Name == "":
				issue(row.Line, "name", "name_required", "the guest has no name, it is printed on the ticket")
			case len([]rune(g.Name)) > maxNameRunes:
				issue(row.Line, "name", "name_too_long", "the name is longer than 200 characters")
			}
			if g.Email != "" && (!strings.Contains(g.Email, "@") || strings.ContainsAny(g.Email, " \t")) {
				issue(row.Line, "email", "invalid_email", "the e-mail is not an address")
			}
			if len([]rune(g.Note)) > maxNoteRunes {
				issue(row.Line, "note", "note_too_long", "the note is longer than 1000 characters")
			}
			switch {
			case g.Email != "":
				g.Delivery = DeliveryGuestEmail
			case defaultDelivery == DeliveryGuestEmail:
				issue(row.Line, "email", "email_required", "delivery is by e-mail to the guest, but the guest has no e-mail")
				g.Delivery = DeliveryGuestEmail
			default:
				g.Delivery = defaultDelivery
			}
			res.Guests = append(res.Guests, g)
			gi = len(res.Guests) - 1
			guestAt[key] = gi
		}
		g := &res.Guests[gi]
		g.Lines = append(g.Lines, row.Line)

		hasKey := row.SeatKey != ""
		hasNames := row.Sector != "" || row.Row != "" || row.Seat != ""
		hasCat := row.Category != "" || row.Qty != ""
		n := 0
		for _, b := range []bool{hasKey, hasNames, hasCat} {
			if b {
				n++
			}
		}
		switch {
		case n == 0:
			issue(row.Line, "seat", "place_missing", "the row names no place: give a seat_key, sector/row/seat, or category and qty")
			continue
		case n > 1:
			issue(row.Line, "seat", "place_ambiguous", "the row names its place twice: use only one of seat_key, sector/row/seat, category/qty")
			continue
		}

		if hasCat {
			resolveCategory(row, g, gaAt, tierByID, tierByName, freeLeft, reserveLeft, &res, issue)
			continue
		}

		var seat *PlanSeat
		if hasKey {
			seat = byKey[row.SeatKey]
			if seat == nil {
				issue(row.Line, "seat_key", "unknown_seat", fmt.Sprintf("the session has no seat %q", row.SeatKey))
				continue
			}
		} else {
			switch {
			case row.Row == "":
				issue(row.Line, "row", "row_required", "the row is empty")
				continue
			case row.Seat == "":
				issue(row.Line, "seat", "seat_required", "the seat is empty")
				continue
			}
			var found []*PlanSeat
			if row.Sector != "" {
				found = byNames[[3]string{norm(row.Sector), norm(row.Row), norm(row.Seat)}]
			} else {
				found = byRowSeat[[2]string{norm(row.Row), norm(row.Seat)}]
			}
			switch len(found) {
			case 0:
				issue(row.Line, "seat", "unknown_seat", fmt.Sprintf("the plan has no seat sector %q, row %q, seat %q", row.Sector, row.Row, row.Seat))
				continue
			case 1:
				seat = found[0]
			default:
				issue(row.Line, "sector", "ambiguous_seat", "several sectors have this row and seat, name the sector")
				continue
			}
		}

		if first, dup := seatLine[seat.ID]; dup {
			issue(row.Line, "seat", "duplicate_seat", fmt.Sprintf("seat %s is already on line %d of the file", seat.Key, first))
			continue
		}
		seatLine[seat.ID] = row.Line
		if seat.Status != originAvailable && seat.Status != originUnavailable {
			issue(row.Line, "seat", "seat_taken", fmt.Sprintf("seat %s is %s and cannot be given as an invitation", seat.Key, seat.Status))
			continue
		}
		if seat.TierID == nil {
			issue(row.Line, "seat", "seat_without_category", fmt.Sprintf("seat %s has no category, assign one before issuing an invitation", seat.Key))
			continue
		}
		g.SeatKeys = append(g.SeatKeys, seat.Key)
		res.SeatLines[seat.Key] = row.Line
		res.Seats++
		if seat.Status == originUnavailable {
			res.FromReserve++
		}
	}

	// File-level limits, reported on line 0 once the rows are read.
	if len(res.Guests) > MaxBatchGuests {
		issue(0, "", "too_many_guests", fmt.Sprintf("the file has %d guests, at most %d fit one file: split the list into several files", len(res.Guests), MaxBatchGuests))
	}
	if res.Places() > MaxBatchPlaces {
		issue(0, "", "too_many_places", fmt.Sprintf("the file hands out %d places, at most %d fit one file: split the list into several files", res.Places(), MaxBatchPlaces))
	}
	if plan.CapacityLeft != nil && res.Places() > *plan.CapacityLeft {
		issue(0, "", "capacity_exceeded", fmt.Sprintf("the session has capacity for %d more places, the file hands out %d", max(*plan.CapacityLeft, 0), res.Places()))
	}
	return res
}

// guestKey folds the guest column onto one identity: a number is compared as
// a number ("01" is guest 1), anything else case-insensitively.
func guestKey(raw string) string {
	k := strings.ToLower(strings.TrimSpace(raw))
	if k == "" {
		return ""
	}
	for _, r := range k {
		if r < '0' || r > '9' {
			return k
		}
	}
	if t := strings.TrimLeft(k, "0"); t != "" {
		return t
	}
	return "0"
}

func resolveCategory(
	row csvRow,
	g *BatchGuest,
	gaAt map[string]int,
	tierByID map[uuid.UUID]*PlanTier,
	tierByName map[string][]*PlanTier,
	freeLeft, reserveLeft map[uuid.UUID]int,
	res *Resolution,
	issue func(line int, field, code, msg string),
) {
	var tier *PlanTier
	if id, err := uuid.Parse(row.Category); err == nil {
		tier = tierByID[id]
	} else if cands := tierByName[norm(row.Category)]; len(cands) == 1 {
		tier = cands[0]
	} else if len(cands) > 1 {
		issue(row.Line, "category", "ambiguous_category", "several categories have this name, use the category id")
		return
	}
	switch {
	case row.Category == "":
		issue(row.Line, "category", "category_required", "the category is empty")
		return
	case tier == nil:
		issue(row.Line, "category", "unknown_category", fmt.Sprintf("the session has no category %q", row.Category))
		return
	case !tier.GA:
		issue(row.Line, "category", "category_not_ga", fmt.Sprintf("category %q has seats on the plan, name the seats instead", tier.Name))
		return
	}
	qty, err := strconv.Atoi(row.Qty)
	if err != nil || qty < 1 || qty > MaxBatchPlaces {
		issue(row.Line, "qty", "invalid_qty", "qty must be a whole number from 1 to 1000")
		return
	}
	if qty > freeLeft[tier.ID]+reserveLeft[tier.ID] {
		issue(row.Line, "qty", "not_enough_places", fmt.Sprintf("category %q has %d places left for this file, %d asked",
			tier.Name, freeLeft[tier.ID]+reserveLeft[tier.ID], qty))
		return
	}
	fromFree := min(qty, freeLeft[tier.ID])
	freeLeft[tier.ID] -= fromFree
	reserveLeft[tier.ID] -= qty - fromFree
	res.GAPlaces += qty
	res.FromReserve += qty - fromFree

	k := fmt.Sprintf("%d/%s", g.Seq, tier.ID)
	if i, ok := gaAt[k]; ok {
		g.GA[i].Quantity += int32(qty) //nolint:gosec // bounded by MaxBatchPlaces
		return
	}
	gaAt[k] = len(g.GA)
	g.GA = append(g.GA, GALine{TierID: tier.ID, Quantity: int32(qty)}) //nolint:gosec // bounded by MaxBatchPlaces
}
