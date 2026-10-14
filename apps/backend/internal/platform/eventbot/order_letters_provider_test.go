package eventbot

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

// providerLetter is one letter of letters[] as the e-mail provider reported
// it; tickets is the number of tickets it carries (0 for a refund letter).
func providerLetter(kind, status string, tickets int, sentAt time.Time) openapi.OrderLetter {
	l := openapi.OrderLetter{Kind: kind, Status: status, Recipient: "anna.tickets@example.test", SentAt: &sentAt}
	for i := 0; i < tickets; i++ {
		l.TicketIds = append(l.TicketIds, openapi_types.UUID{byte(i + 1)})
	}
	return l
}

// The card reads letters[] (review 2026-10-14 M2): delivered, opened,
// delayed, bounced and stuck reach the bot.
func TestTicketLetterStatus_FromProviderLetters(t *testing.T) {
	t.Parallel()
	sent := time.Date(2099, 1, 15, 19, 30, 0, 0, time.UTC)
	delivered := sent.Add(time.Minute)
	reason := "550 5.1.1 user unknown"

	cases := []struct {
		name    string
		letters []openapi.OrderLetter
		state   string
		failed  bool
	}{
		{"delivered and opened", func() []openapi.OrderLetter {
			a := providerLetter("ticket", "delivered", 2, sent)
			a.DeliveredAt = &delivered
			b := providerLetter("ticket", "opened", 1, sent)
			b.OpenedAt = &delivered
			return []openapi.OrderLetter{a, b}
		}(), letterDelivered, false},
		{"opened", []openapi.OrderLetter{providerLetter("ticket", "opened", 2, sent)}, letterOpened, false},
		{"delayed", []openapi.OrderLetter{providerLetter("ticket", "delayed", 2, sent)}, letterDelayed, false},
		{"stuck", func() []openapi.OrderLetter {
			l := providerLetter("ticket", "sent", 2, sent)
			l.Stuck = true
			return []openapi.OrderLetter{l}
		}(), letterStuck, true},
		{"bounced after the relay took it", func() []openapi.OrderLetter {
			l := providerLetter("ticket", "failed", 2, sent)
			l.FailureReason = &reason
			return []openapi.OrderLetter{l}
		}(), letterBounced, true},
		{"refused by the relay", func() []openapi.OrderLetter {
			l := providerLetter("ticket", "failed", 1, sent)
			l.SentAt = nil
			return []openapi.OrderLetter{l}
		}(), letterFailed, true},
		{"refund letters do not count as ticket letters", []openapi.OrderLetter{
			providerLetter("refund", "failed", 0, sent), providerLetter("ticket", "delivered", 1, sent),
		}, letterDelivered, false},
	}
	for _, c := range cases {
		var d openapi.OrderDetail
		d.Letters = c.letters
		got := ticketLetterStatus(d)
		if got.State != c.state || got.failed() != c.failed {
			t.Errorf("%s: state %q failed %v, want %q %v", c.name, got.State, got.failed(), c.state, c.failed)
		}
	}

	var d openapi.OrderDetail
	l := providerLetter("ticket", "failed", 1, sent)
	l.FailureReason = &reason
	d.Letters = []openapi.OrderLetter{l}
	if got := ticketLetterStatus(d); got.Why != reason || got.To != "anna.tickets@example.test" {
		t.Errorf("bounced letter = %+v, want its reason and address", got)
	}
}

// A sent refund letter moves on to what the provider reported about the
// newest refund letter.
func TestRefundLetterStatus_FromProviderLetters(t *testing.T) {
	t.Parallel()
	sent := time.Date(2099, 1, 16, 10, 0, 0, 0, time.UTC)
	at := sent
	for status, want := range map[string]string{
		"opened": letterOpened, "delivered": letterDelivered, "delayed": letterDelayed,
		"failed": letterBounced, "sent": letterSent,
	} {
		var d openapi.OrderDetail
		d.RefundLetter = &openapi.OrderRefundLetter{State: "sent", Refunds: 1, Sent: 1, At: &at}
		d.Letters = []openapi.OrderLetter{
			providerLetter("refund", "delivered", 0, sent.Add(-time.Hour)),
			providerLetter("refund", status, 0, sent),
		}
		if got := refundLetterStatus(d).State; got != want {
			t.Errorf("newest refund letter %s -> %q, want %q", status, got, want)
		}
	}
}

// Every new state renders in every language the bot speaks, with its reason.
func TestOrderCard_ProviderLetterLines(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		for _, kind := range []string{"tickets", "refund"} {
			for _, state := range []string{letterDelayed, letterStuck, letterBounced} {
				line := b.letterLine(loc, kind, letterStatus{State: state, To: "anna@example.test", Why: "550 user unknown"}, "Europe/Madrid", true)
				if line == "" || strings.Contains(line, "bot.ord.") {
					t.Errorf("%s %s %s: no text: %q", loc, kind, state, line)
				}
				if state == letterBounced && !strings.Contains(line, "550 user unknown") {
					t.Errorf("%s %s bounced line lacks the reason: %q", loc, kind, line)
				}
			}
		}
	}
}

func TestOrderRowLabel_MarksALetterTheServerFlags(t *testing.T) {
	t.Parallel()
	o := listItem(t, "paid", 4700, "Buyer Name")
	sent := letterSent
	o.DeliveryState = &sent
	o.LetterFailed = true
	if got := OrderRowLabel(o, "en"); !strings.Contains(got, letterFailedMark) {
		t.Errorf("row = %q, want the mark for letter_failed", got)
	}
}

// The "stuck" texts name the threshold in hours: it must be the server's
// emailstatus.StuckAfter, in every language (review 2026-10-14 F6).
func TestOrderCard_StuckTextsNameTheServerThreshold(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	hours := regexp.MustCompile(`\b` + strconv.Itoa(int(emailstatus.StuckAfter.Hours())) + `\b`)
	for _, loc := range SupportedLocales {
		for _, kind := range []string{"tickets", "refund"} {
			line := b.letterLine(loc, kind, letterStatus{State: letterStuck}, "Europe/Madrid", false)
			if !hours.MatchString(line) {
				t.Errorf("%s %s stuck text does not say %v: %q", loc, kind, emailstatus.StuckAfter, line)
			}
		}
	}
}
