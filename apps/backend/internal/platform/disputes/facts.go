package disputes

// facts.go — everything arena knows about a disputed order, read once when
// the package is built (spec 51.3 §7, spec 38 §5). Each part says plainly
// when it has nothing: a gap is shown, never hidden.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Facts is the evidence of one order.
type Facts struct {
	Org     OrgFacts
	Order   *OrderFacts
	Payment *PaymentFacts
	Session *SessionFacts
	Items   []ItemFacts
	Tickets []TicketFacts
	Letters []LetterFacts
	Opens   []OpenFacts
	Access  []AccessFacts
	// AccessLogKnown: the ticket access log exists in this database
	// (ticket_access_events, PR #352); false means "not recorded", never "none".
	AccessLogKnown bool
	Scans          []ScanFacts
	Consent        *ConsentFacts
	Refunds        []RefundFacts
	Changes        []ChangeFacts
	// PriorOrders: earlier paid orders of the same buyer at this
	// organization that were never disputed (for a fraud claim).
	PriorOrders int
	// Charge is what the provider adds (3-D Secure, card, descriptor).
	Charge *ChargeInfo
}

// OrgFacts is the seller.
type OrgFacts struct {
	Name, LegalName, TaxID, Registration string
	Address, Country, ContactEmail       string
	Website                              string
}

// OrderFacts is the order.
type OrderFacts struct {
	ID                                uuid.UUID
	Number                            int64
	Status, Source, Currency          string
	Subtotal, Discount, Charge, Total int64
	BuyerName, BuyerEmail, BuyerPhone string
	CustomerID                        *uuid.UUID
	PaymentMethod                     string
	CreatedAt                         time.Time
	PaidAt                            *time.Time
	CheckoutSessionID                 uuid.UUID
}

// PaymentFacts is the payment at the provider.
type PaymentFacts struct {
	Provider, ProviderPaymentID, ChargeRef string
	Amount                                 int64
	Currency, State                        string
	SucceededAt                            *time.Time
}

// SessionFacts is the event date.
type SessionFacts struct {
	EventName, Status             string
	StartAt                       time.Time
	EndAt                         *time.Time
	VenueName, VenueAddress, City string
	Timezone                      string
}

// ItemFacts is one line of the order.
type ItemFacts struct {
	Ordinal                            int
	Category                           string
	UnitPrice, Discount, Charge, Total int64
	TicketNumber                       string
}

// TicketFacts is one ticket.
type TicketFacts struct {
	Number, Category, Seat, Status string
	Barcode                        string
	UsedAt                         *time.Time
	IssuedAt                       *time.Time
}

// LetterFacts is one letter to the buyer.
type LetterFacts struct {
	Kind, Recipient, Subject, Status              string
	SentAt                                        time.Time
	DeliveredAt, OpenedAt, ProxyOpenedAt, Clicked *time.Time
	FailedAt                                      *time.Time
	ReceiptMissing                                bool
	TicketNumbers                                 []string
	BodySHA256, TemplateVersion                   string
	Attachments                                   int
}

// OpenFacts is one open or click of a letter.
type OpenFacts struct {
	Event     string
	At        time.Time
	UserAgent string
}

// AccessFacts is one download or view of the tickets.
type AccessFacts struct {
	Action, Source, IP, UserAgent, TicketNumber string
	At                                          time.Time
}

// ScanFacts is one scan at the door.
type ScanFacts struct {
	TicketNumber, Result, Source, Gate string
	At                                 time.Time
}

// ConsentFacts is the buyer's consent at checkout (P2) and the terms they saw (P3).
type ConsentFacts struct {
	AcceptedAt                            time.Time
	IP, UserAgent, Origin, TermsVersion   string
	TermsURL, PrivacyURL, SellerLegalName string
	Locale                                string
	WithdrawalShown                       bool
	TermsSHA256, TermsHTML, RefundMode    string
}

// RefundFacts is one refund.
type RefundFacts struct {
	Amount                    int64
	Currency, State           string
	At                        time.Time
	TicketNumber, ProviderRef string
}

// ChangeFacts is a move or a cancellation of the date.
type ChangeFacts struct {
	Kinds []string
	At    time.Time
}

// ChargeInfo is what the provider adds.
type ChargeInfo struct {
	ThreeDSecure, CardBrand, Last4, StatementDescriptor string
}

// CollectFacts reads the evidence of a dispute's order. A dispute without
// an order (should not happen) yields the seller only.
func CollectFacts(ctx context.Context, q querier, d Dispute) (Facts, error) {
	var f Facts
	if err := q.QueryRow(ctx, `SELECT name, COALESCE(legal_name,''), COALESCE(tax_id,''), COALESCE(registration_number,''),
			concat_ws(', ', NULLIF(legal_address_line1,''), NULLIF(legal_address_line2,''), NULLIF(legal_address_postal_code,''), NULLIF(legal_address_city,'')),
			COALESCE(NULLIF(legal_address_country,''), country, ''), COALESCE(contact_email,''), COALESCE(website_url,'')
		FROM organizations WHERE id = $1`, d.OrgID).Scan(&f.Org.Name, &f.Org.LegalName, &f.Org.TaxID, &f.Org.Registration,
		&f.Org.Address, &f.Org.Country, &f.Org.ContactEmail, &f.Org.Website); err != nil {
		return f, fmt.Errorf("disputes: organization: %w", err)
	}
	if d.PaymentIntentID != nil {
		var p PaymentFacts
		var ppid, ref *string
		err := q.QueryRow(ctx, `SELECT provider, provider_payment_id, provider_charge_ref, amount, currency, state, succeeded_at
			FROM payment_intents WHERE id = $1 AND org_id = $2`, *d.PaymentIntentID, d.OrgID).
			Scan(&p.Provider, &ppid, &ref, &p.Amount, &p.Currency, &p.State, &p.SucceededAt)
		if err == nil {
			p.ProviderPaymentID, p.ChargeRef = deref(ppid), deref(ref)
			f.Payment = &p
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return f, fmt.Errorf("disputes: payment: %w", err)
		}
	}
	if d.OrderID == nil {
		return f, nil
	}
	o := OrderFacts{}
	var name, email, phone, method *string
	var sessionID uuid.UUID
	err := q.QueryRow(ctx, `SELECT id, system_id, status, source, currency, subtotal, discount, charge, total,
			buyer_name, buyer_email, buyer_phone, customer_id, payment_method, created_at, paid_at, checkout_session_id, session_id
		FROM orders WHERE id = $1 AND org_id = $2`, *d.OrderID, d.OrgID).
		Scan(&o.ID, &o.Number, &o.Status, &o.Source, &o.Currency, &o.Subtotal, &o.Discount, &o.Charge, &o.Total,
			&name, &email, &phone, &o.CustomerID, &method, &o.CreatedAt, &o.PaidAt, &o.CheckoutSessionID, &sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("disputes: order: %w", err)
	}
	o.BuyerName, o.BuyerEmail, o.BuyerPhone, o.PaymentMethod = deref(name), deref(email), deref(phone), deref(method)
	f.Order = &o

	s := SessionFacts{}
	var venueAddr, city, tz *string
	err = q.QueryRow(ctx, `SELECT e.name, s.status, s.start_at, s.end_at, COALESCE(v.name,''),
			NULLIF(concat_ws(', ', NULLIF(COALESCE(NULLIF(v.address_line1,''), v.address),''), NULLIF(v.postal_code,'')), ''),
			COALESCE(t_en.value, ci.slug), v.timezone
		FROM sessions s
		JOIN events e ON e.id = s.event_id
		LEFT JOIN venues v ON v.id = s.venue_id
		LEFT JOIN cities ci ON ci.id = v.city_id
		LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.cities' AND t_en.key = ci.slug AND t_en.locale = 'en'
		WHERE s.id = $1`, sessionID).Scan(&s.EventName, &s.Status, &s.StartAt, &s.EndAt, &s.VenueName, &venueAddr, &city, &tz)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, fmt.Errorf("disputes: session: %w", err)
	}
	if err == nil {
		s.VenueAddress, s.City, s.Timezone = deref(venueAddr), deref(city), deref(tz)
		f.Session = &s
	}

	if err := collectRows(ctx, q, `SELECT oi.ordinal, COALESCE(tt.name,''), oi.unit_price, oi.discount, oi.charge, oi.total,
			COALESCE(t.system_ticket_id::text, '')
		FROM order_items oi
		LEFT JOIN ticket_tiers tt ON tt.id = oi.tier_id
		LEFT JOIN tickets t ON t.id = oi.ticket_id
		WHERE oi.order_id = $1 ORDER BY oi.ordinal`, []any{o.ID}, func(r pgx.Rows) error {
		var it ItemFacts
		if err := r.Scan(&it.Ordinal, &it.Category, &it.UnitPrice, &it.Discount, &it.Charge, &it.Total, &it.TicketNumber); err != nil {
			return err
		}
		f.Items = append(f.Items, it)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: items: %w", err)
	}

	if err := collectRows(ctx, q, `SELECT COALESCE(t.system_ticket_id::text, left(t.id::text, 8)), COALESCE(tt.name,''),
			concat_ws(' / ', NULLIF(t.seat_sector,''), NULLIF(t.seat_row,''), NULLIF(t.seat_number,'')),
			t.status, COALESCE(tc.payload,''), t.used_at, t.issued_at
		FROM tickets t
		LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
		LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13' AND tc.revoked_at IS NULL
		WHERE t.order_id = $1 OR (t.order_id IS NULL AND t.checkout_session_id = $2)
		ORDER BY t.system_ticket_id NULLS LAST, t.id`, []any{o.ID, o.CheckoutSessionID}, func(r pgx.Rows) error {
		var t TicketFacts
		if err := r.Scan(&t.Number, &t.Category, &t.Seat, &t.Status, &t.Barcode, &t.UsedAt, &t.IssuedAt); err != nil {
			return err
		}
		f.Tickets = append(f.Tickets, t)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: tickets: %w", err)
	}

	if err := collectRows(ctx, q, `SELECT kind, recipient, COALESCE(subject,''), status, sent_at, delivered_at, opened_at,
			proxy_opened_at, clicked_at, failed_at, receipt_missing, ticket_numbers, COALESCE(body_sha256,''),
			COALESCE(template_version,''), jsonb_array_length(attachments)
		FROM email_letters WHERE order_id = $1 AND status <> 'sending'
		ORDER BY sent_at, id`, []any{o.ID}, func(r pgx.Rows) error {
		var l LetterFacts
		if err := r.Scan(&l.Kind, &l.Recipient, &l.Subject, &l.Status, &l.SentAt, &l.DeliveredAt, &l.OpenedAt,
			&l.ProxyOpenedAt, &l.Clicked, &l.FailedAt, &l.ReceiptMissing, &l.TicketNumbers, &l.BodySHA256,
			&l.TemplateVersion, &l.Attachments); err != nil {
			return err
		}
		f.Letters = append(f.Letters, l)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: letters: %w", err)
	}

	if err := collectRows(ctx, q, `SELECT i.event, i.occurred_at, COALESCE(i.user_agent,'')
		FROM email_letter_interactions i JOIN email_letters l ON l.id = i.letter_id
		WHERE l.order_id = $1 ORDER BY i.occurred_at LIMIT 200`, []any{o.ID}, func(r pgx.Rows) error {
		var x OpenFacts
		if err := r.Scan(&x.Event, &x.At, &x.UserAgent); err != nil {
			return err
		}
		f.Opens = append(f.Opens, x)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: letter interactions: %w", err)
	}

	// The ticket access log comes from another change (PR #352); read it
	// only where the table exists.
	var accessTable bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.ticket_access_events') IS NOT NULL`).Scan(&accessTable); err != nil {
		return f, err
	}
	f.AccessLogKnown = accessTable
	if accessTable {
		if err := collectRows(ctx, q, `SELECT a.action, a.source, COALESCE(host(a.ip),''), COALESCE(a.user_agent,''),
				COALESCE(t.system_ticket_id::text,''), a.occurred_at
			FROM ticket_access_events a LEFT JOIN tickets t ON t.id = a.ticket_id
			WHERE a.checkout_session_id = $1 ORDER BY a.occurred_at LIMIT 300`, []any{o.CheckoutSessionID}, func(r pgx.Rows) error {
			var a AccessFacts
			if err := r.Scan(&a.Action, &a.Source, &a.IP, &a.UserAgent, &a.TicketNumber, &a.At); err != nil {
				return err
			}
			f.Access = append(f.Access, a)
			return nil
		}); err != nil {
			return f, fmt.Errorf("disputes: ticket access: %w", err)
		}
	}

	if err := collectRows(ctx, q, `SELECT COALESCE(t.system_ticket_id::text,''), se.result, COALESCE(se.source,'arena_scanner'),
			COALESCE(NULLIF(se.gate,''), NULLIF(se.scanner_label,''), ''), se.scanned_at
		FROM scan_events se JOIN tickets t ON t.id = se.ticket_id
		WHERE se.org_id = $1 AND (t.order_id = $2 OR (t.order_id IS NULL AND t.checkout_session_id = $3))
		ORDER BY se.scanned_at LIMIT 200`, []any{d.OrgID, o.ID, o.CheckoutSessionID}, func(r pgx.Rows) error {
		var sc ScanFacts
		if err := r.Scan(&sc.TicketNumber, &sc.Result, &sc.Source, &sc.Gate, &sc.At); err != nil {
			return err
		}
		f.Scans = append(f.Scans, sc)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: scans: %w", err)
	}

	var c ConsentFacts
	var ip, ua, origin, termsURL, privacyURL, seller, locale, sha, html, mode *string
	err = q.QueryRow(ctx, `SELECT cc.accepted_at, host(cc.client_ip), cc.user_agent, cc.origin_host, cc.terms_version,
			cc.terms_url, cc.privacy_url, cc.seller_legal_name, cc.locale, cc.withdrawal_notice_shown,
			COALESCE(cc.terms_sha256, lr.body_sha256), lr.body_html, lr.refund_mode
		FROM checkout_consents cc
		LEFT JOIN legal_renderings lr ON lr.id = cc.legal_rendering_id
		WHERE cc.checkout_session_id = $1 ORDER BY cc.accepted_at DESC LIMIT 1`, o.CheckoutSessionID).
		Scan(&c.AcceptedAt, &ip, &ua, &origin, &c.TermsVersion, &termsURL, &privacyURL, &seller, &locale, &c.WithdrawalShown,
			&sha, &html, &mode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, fmt.Errorf("disputes: consent: %w", err)
	}
	if err == nil {
		c.IP, c.UserAgent, c.Origin = deref(ip), deref(ua), deref(origin)
		c.TermsURL, c.PrivacyURL, c.SellerLegalName, c.Locale = deref(termsURL), deref(privacyURL), deref(seller), deref(locale)
		c.TermsSHA256, c.TermsHTML, c.RefundMode = deref(sha), deref(html), deref(mode)
		f.Consent = &c
	}

	if err := collectRows(ctx, q, `SELECT r.amount, r.currency, r.state, COALESCE(r.succeeded_at, r.created_at),
			COALESCE(t.system_ticket_id::text,''), COALESCE(r.provider_refund_id,'')
		FROM refunds r LEFT JOIN tickets t ON t.id = r.ticket_id
		WHERE r.org_id = $1 AND (r.order_id = $2 OR ($3::uuid IS NOT NULL AND r.payment_intent_id = $3))
		  AND r.state NOT IN ('rejected')
		ORDER BY r.created_at`, []any{d.OrgID, o.ID, d.PaymentIntentID}, func(r pgx.Rows) error {
		var rf RefundFacts
		if err := r.Scan(&rf.Amount, &rf.Currency, &rf.State, &rf.At, &rf.TicketNumber, &rf.ProviderRef); err != nil {
			return err
		}
		f.Refunds = append(f.Refunds, rf)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: refunds: %w", err)
	}

	if err := collectRows(ctx, q, `SELECT kinds, created_at FROM session_changes
		WHERE session_id = $1 ORDER BY created_at`, []any{sessionID}, func(r pgx.Rows) error {
		var ch ChangeFacts
		if err := r.Scan(&ch.Kinds, &ch.At); err != nil {
			return err
		}
		f.Changes = append(f.Changes, ch)
		return nil
	}); err != nil {
		return f, fmt.Errorf("disputes: session changes: %w", err)
	}

	if err := q.QueryRow(ctx, `SELECT count(*) FROM orders o2
		WHERE o2.org_id = $1 AND o2.id <> $2 AND o2.paid_at IS NOT NULL AND o2.paid_at < $3
		  AND (($4::uuid IS NOT NULL AND o2.customer_id = $4) OR ($5 <> '' AND lower(o2.buyer_email) = lower($5)))
		  AND NOT EXISTS (SELECT 1 FROM payment_disputes pd WHERE pd.order_id = o2.id)`,
		d.OrgID, o.ID, o.CreatedAt, o.CustomerID, o.BuyerEmail).Scan(&f.PriorOrders); err != nil {
		return f, fmt.Errorf("disputes: prior orders: %w", err)
	}
	return f, nil
}

func collectRows(ctx context.Context, q querier, sql string, args []any, each func(pgx.Rows) error) error {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
