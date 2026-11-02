package httpserver

// disputes_json.go — the JSON shapes of the dispute API (disputes_routes.go).

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
)

// --- JSON ------------------------------------------------------------------

type disputeJSON struct {
	ID                 string  `json:"id"`
	Provider           string  `json:"provider"`
	ProviderDisputeID  *string `json:"provider_dispute_id"`
	Channel            string  `json:"channel"`
	OrderID            *string `json:"order_id"`
	OrderNumber        int64   `json:"order_number"`
	EventName          string  `json:"event_name"`
	SessionStartAt     *string `json:"session_start_at"`
	VenueTimezone      string  `json:"venue_timezone"`
	BuyerName          string  `json:"buyer_name"`
	BuyerEmail         string  `json:"buyer_email"`
	Amount             int64   `json:"amount"`
	Currency           string  `json:"currency"`
	Reason             string  `json:"reason"`
	ReasonGroup        string  `json:"reason_group"`
	ProviderStatus     string  `json:"provider_status"`
	State              string  `json:"state"`
	Outcome            *string `json:"outcome"`
	IsInquiry          bool    `json:"is_inquiry"`
	IsChargeRefundable bool    `json:"is_charge_refundable"`
	DueBy              *string `json:"due_by"`
	DueBySource        string  `json:"due_by_source"`
	RespondTo          string  `json:"respond_to"`
	LowValue           bool    `json:"low_value"`
	FundsWithdrawnAt   *string `json:"funds_withdrawn_at"`
	FundsReinstatedAt  *string `json:"funds_reinstated_at"`
	OpenedAt           string  `json:"opened_at"`
	SubmittedAt        *string `json:"submitted_at"`
	AcceptedAt         *string `json:"accepted_at"`
	ClosedAt           *string `json:"closed_at"`
}

func disputeTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func disputeIDStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	v := id.String()
	return &v
}

func toDisputeJSON(d disputes.Dispute, sm disputes.Summary) disputeJSON {
	j := disputeJSON{
		ID: d.ID.String(), Provider: d.Provider, ProviderDisputeID: shownDisputeRef(d), Channel: d.Channel,
		OrderID: disputeIDStr(d.OrderID), OrderNumber: sm.OrderNumber, EventName: sm.EventName, VenueTimezone: sm.Timezone,
		BuyerName: sm.BuyerName, BuyerEmail: sm.BuyerEmail,
		Amount: d.AmountMinor, Currency: d.Currency, Reason: d.Reason, ReasonGroup: d.ReasonGroup,
		ProviderStatus: d.ProviderStatus, State: d.State, Outcome: d.Outcome, IsInquiry: d.IsInquiry,
		IsChargeRefundable: d.IsChargeRefundable, DueBy: disputeTime(d.DueBy), DueBySource: d.DueBySource,
		RespondTo: d.RespondTo, LowValue: d.LowValue, FundsWithdrawnAt: disputeTime(d.FundsWithdrawnAt),
		FundsReinstatedAt: disputeTime(d.FundsReinstatedAt), OpenedAt: d.OpenedAt.UTC().Format(time.RFC3339),
		SubmittedAt: disputeTime(d.SubmittedAt), AcceptedAt: disputeTime(d.AcceptedAt), ClosedAt: disputeTime(d.ClosedAt),
	}
	if sm.SessionAt != "" {
		v := sm.SessionAt
		j.SessionStartAt = &v
	}
	return j
}

type disputeFileJSON struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	Bytes       int    `json:"bytes"`
	Pages       int    `json:"pages"`
	SHA256      string `json:"sha256"`
	ForProvider bool   `json:"for_provider"`
}

type disputePackageJSON struct {
	ID         string               `json:"id"`
	Version    int                  `json:"version"`
	Locale     string               `json:"locale"`
	Fields     map[string]string    `json:"fields"`
	Gaps       []string             `json:"gaps"`
	Uploaded   bool                 `json:"uploaded"`
	EmailDraft *disputes.EmailDraft `json:"email_draft"`
	TotalBytes int                  `json:"total_bytes"`
	TotalPages int                  `json:"total_pages"`
	BuiltAt    string               `json:"built_at"`
	Files      []disputeFileJSON    `json:"files"`
}

func toPackageJSON(p *disputes.Package) *disputePackageJSON {
	if p == nil {
		return nil
	}
	files := make([]disputeFileJSON, 0, len(p.Files))
	for _, f := range p.Files {
		files = append(files, disputeFileJSON{ID: f.ID.String(), Kind: f.Kind, Filename: f.Filename, Bytes: f.Bytes, Pages: f.Pages, SHA256: f.SHA256, ForProvider: f.ForProvider})
	}
	gaps := p.Gaps
	if gaps == nil {
		gaps = []string{}
	}
	return &disputePackageJSON{
		ID: p.ID.String(), Version: p.Version, Locale: p.Locale, Fields: p.Fields, Gaps: gaps,
		Uploaded: len(p.ProviderFileIDs) > 0, EmailDraft: p.EmailDraft, TotalBytes: p.TotalBytes,
		TotalPages: p.TotalPages, BuiltAt: p.BuiltAt.UTC().Format(time.RFC3339), Files: files,
	}
}

type disputeEventJSON struct {
	Kind           string          `json:"kind"`
	ProviderStatus string          `json:"provider_status"`
	ActorUserID    *string         `json:"actor_user_id"`
	ActorLabel     string          `json:"actor_label"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      string          `json:"created_at"`
}

// shownDisputeRef is the provider's reference as a person reads it: a
// hand-entered one is stored as "manual:<org id>:<reference>".
func shownDisputeRef(d disputes.Dispute) *string {
	if d.ProviderDisputeID == nil {
		return nil
	}
	v := strings.TrimPrefix(*d.ProviderDisputeID, "manual:"+d.OrgID.String()+":")
	return &v
}
