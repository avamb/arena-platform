// resolve.go — the core spec §12.2 resolver plus the small helpers
// (Touch, MarkVerified, LinkOrg) exposed alongside it.
//
// The resolver operates against a Store abstraction so unit tests can run
// with an in-memory fake — the real adapter lives in postgres_store.go.
// Every call takes a context.Context; callers that need to run inside a
// pgx.Tx build a per-transaction Store via NewStoreFromQueries(qtx).

package customers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Store is the narrow persistence contract the resolver depends on. Each
// method operates on whatever DBTX the concrete implementation was
// constructed with — pool or tx.
type Store interface {
	// GetIdentityByStrong looks up a strong identity (email/phone/telegram)
	// by (kind, value). Returns ErrNotFound when absent.
	GetIdentityByStrong(ctx context.Context, kind IdentityKind, value string) (Identity, error)

	// GetIdentityByWeak looks up a weak identity within a specific channel.
	// Returns ErrNotFound when absent.
	GetIdentityByWeak(ctx context.Context, kind IdentityKind, value string, channelID uuid.UUID) (Identity, error)

	// InsertCustomer creates a new customer row. displayName / locale are
	// optional — pass "" to leave NULL.
	InsertCustomer(ctx context.Context, displayName, locale string) (Customer, error)

	// InsertIdentity attaches a new identity. channelID is required for
	// weak kinds and MUST be nil for strong kinds. verifiedAt is optional.
	InsertIdentity(ctx context.Context, customerID uuid.UUID, kind IdentityKind, value string, channelID *uuid.UUID, source string, verifiedAt *time.Time) (Identity, error)

	// UpdateDisplayName overwrites customers.display_name. Callers apply
	// the §12.2 non-empty rule before invoking this.
	UpdateDisplayName(ctx context.Context, customerID uuid.UUID, displayName string) error

	// TouchIdentity bumps last_seen_at on an identity row.
	TouchIdentity(ctx context.Context, id uuid.UUID) error

	// MarkIdentityVerified sets verified_at if currently NULL and touches
	// last_seen_at. Idempotent.
	MarkIdentityVerified(ctx context.Context, id uuid.UUID, at time.Time) error

	// UpsertOrgLink ensures (customer_id, org_id) exists in
	// customer_org_links. Counter maintenance belongs to the orders write
	// path, not this method.
	UpsertOrgLink(ctx context.Context, customerID, orgID uuid.UUID, source string) error

	// InsertMergeCandidate queues a suspected duplicate. The gateway NEVER
	// auto-merges — the operator resolves candidates from the admin UI.
	InsertMergeCandidate(ctx context.Context, a, b uuid.UUID, reason string) error

	// InsertAttribute writes a platform- or org-scoped attribute; used by
	// the resolver to stash an invalid raw phone (spec §3.2 fallback).
	InsertAttribute(ctx context.Context, customerID uuid.UUID, orgID *uuid.UUID, key, valueJSON, source string) error

	// GetCustomer loads the display_name / locale for the §12.2 non-empty
	// name rule ("не перезаписывать непустое имя пустым"). Returns
	// ErrNotFound when the id is unknown.
	GetCustomer(ctx context.Context, id uuid.UUID) (Customer, error)
}

// ResolveInput is the caller-supplied §12.2 payload. Any subset may be
// empty; the resolver picks up whichever identities the site provided on
// the current wire call. RawPhone is set when the caller wants the raw
// pre-normalised string preserved as an attribute in the invalid-phone
// fallback path (§3.2). If it is empty the resolver reuses the phone
// argument as the raw value.
type ResolveInput struct {
	Email         string
	Phone         string
	RawPhone      string // optional; used only when Phone fails to normalise
	Name          string
	ChannelID     uuid.UUID
	DeviceToken   string
	WCCustomerID  string
	DefaultRegion string // ISO 3166-1 alpha-2 for NormalizePhone
	// OrgID / Source are used by LinkOrg — the resolver does NOT link on
	// its own; callers invoke LinkOrg once they know the org the current
	// operation belongs to.
	Source string // 'live' by default when empty
	Now    time.Time
}

// ResolveResult tells the caller which customer won, whether it was newly
// minted, and which identities were freshly attached this call. It also
// carries the id of the merge candidate row when a strong-key conflict
// was queued — useful for logs and integration tests.
type ResolveResult struct {
	Customer           Customer
	Created            bool
	AttachedIdentities []Identity
	// PhoneWasInvalid is true when the input Phone did not normalise; the
	// raw value has been written to customer_attributes as
	// AttrKeyInvalidPhone / source=SourceLive.
	PhoneWasInvalid bool
	// MergeCandidateQueued is true when a strong-key conflict was
	// detected and a customer_merge_candidates row was inserted.
	MergeCandidateQueued bool
}

// Resolve implements spec §12.2. See the top-of-file docstring for the
// contract; the numbered comments below match the numbered steps in the
// spec.
func Resolve(ctx context.Context, s Store, in ResolveInput) (ResolveResult, error) {
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}
	source := in.Source
	if source == "" {
		source = SourceLive
	}

	// ── Step 1: normalise. ─────────────────────────────────────────────
	var (
		normEmail       string
		normPhone       string
		phoneWasInvalid bool
	)
	if in.Email != "" {
		e, err := NormalizeEmail(in.Email)
		if err == nil {
			normEmail = e
		}
		// Empty-after-trim email is silently ignored — spec §12.2 lets
		// callers omit an identity; the presence check falls through to
		// the weak-key lookup.
	}
	if in.Phone != "" {
		p, err := NormalizePhone(in.Phone, in.DefaultRegion)
		if err == nil {
			normPhone = p
		} else if errors.Is(err, ErrInvalidPhone) {
			phoneWasInvalid = true
		}
	}

	// ── Step 2: strong-key lookups. ────────────────────────────────────
	// The spec walks email + phone independently, then reconciles.
	var (
		byEmail *Identity
		byPhone *Identity
	)
	if normEmail != "" {
		id, err := s.GetIdentityByStrong(ctx, KindEmail, normEmail)
		if err == nil {
			byEmail = &id
		} else if !errors.Is(err, ErrNotFound) {
			return ResolveResult{}, err
		}
	}
	if normPhone != "" {
		id, err := s.GetIdentityByStrong(ctx, KindPhone, normPhone)
		if err == nil {
			byPhone = &id
		} else if !errors.Is(err, ErrNotFound) {
			return ResolveResult{}, err
		}
	}

	res := ResolveResult{PhoneWasInvalid: phoneWasInvalid}

	// Both strong keys found and agree → return that customer.
	// Both found but disagree → spec §12.2 step 2: return the e-mail's
	// customer, DO NOT reassign the phone, and queue a merge candidate.
	// Only one found → attach the other as a fresh identity.
	// Neither found → fall through to the weak-key lookup.
	switch {
	case byEmail != nil && byPhone != nil && byEmail.CustomerID == byPhone.CustomerID:
		if err := s.TouchIdentity(ctx, byEmail.ID); err != nil {
			return ResolveResult{}, err
		}
		if err := s.TouchIdentity(ctx, byPhone.ID); err != nil {
			return ResolveResult{}, err
		}
		return finishResolve(ctx, s, res, byEmail.CustomerID, in, source /*addEmail*/, false /*addPhone*/, false)

	case byEmail != nil && byPhone != nil && byEmail.CustomerID != byPhone.CustomerID:
		if err := s.InsertMergeCandidate(ctx, byEmail.CustomerID, byPhone.CustomerID, MergeReasonEmailOfAPhoneOfB); err != nil {
			return ResolveResult{}, err
		}
		res.MergeCandidateQueued = true
		if err := s.TouchIdentity(ctx, byEmail.ID); err != nil {
			return ResolveResult{}, err
		}
		// Deliberately do NOT touch byPhone or attach anything else —
		// spec: "телефон не переприсваивать".
		return finishResolve(ctx, s, res, byEmail.CustomerID, in, source /*addEmail*/, false /*addPhone*/, false)

	case byEmail != nil:
		if err := s.TouchIdentity(ctx, byEmail.ID); err != nil {
			return ResolveResult{}, err
		}
		return finishResolve(ctx, s, res, byEmail.CustomerID, in, source, false, normPhone != "")

	case byPhone != nil:
		if err := s.TouchIdentity(ctx, byPhone.ID); err != nil {
			return ResolveResult{}, err
		}
		return finishResolve(ctx, s, res, byPhone.CustomerID, in, source, normEmail != "", false)
	}

	// ── Step 3: weak-key lookup within the channel. ────────────────────
	var byWeak *Identity
	if in.ChannelID != uuid.Nil {
		if in.DeviceToken != "" {
			id, err := s.GetIdentityByWeak(ctx, KindDevice, in.DeviceToken, in.ChannelID)
			if err == nil {
				byWeak = &id
			} else if !errors.Is(err, ErrNotFound) {
				return ResolveResult{}, err
			}
		}
		if byWeak == nil && in.WCCustomerID != "" {
			id, err := s.GetIdentityByWeak(ctx, KindWCCustomer, in.WCCustomerID, in.ChannelID)
			if err == nil {
				byWeak = &id
			} else if !errors.Is(err, ErrNotFound) {
				return ResolveResult{}, err
			}
		}
	}
	if byWeak != nil {
		if err := s.TouchIdentity(ctx, byWeak.ID); err != nil {
			return ResolveResult{}, err
		}
		return finishResolve(ctx, s, res, byWeak.CustomerID, in, source, normEmail != "", normPhone != "")
	}

	// ── Nothing found: create a fresh customer. ────────────────────────
	c, err := s.InsertCustomer(ctx, in.Name, "")
	if err != nil {
		return ResolveResult{}, err
	}
	res.Created = true
	return finishResolve(ctx, s, res, c.ID, in, source, normEmail != "", normPhone != "")
}

// finishResolve is the shared tail: attach whichever identities were not
// already present, apply the display-name rule, and stash an invalid raw
// phone as an attribute.
func finishResolve(
	ctx context.Context,
	s Store,
	res ResolveResult,
	customerID uuid.UUID,
	in ResolveInput,
	source string,
	addEmail bool,
	addPhone bool,
) (ResolveResult, error) {
	// Reload the customer to know whether display_name is currently
	// empty — spec §12.2: "не перезаписывать непустое имя пустым;
	// новое непустое — обновить".
	c, err := s.GetCustomer(ctx, customerID)
	if err != nil {
		// A resolver call is inside the caller's transaction; a missing
		// row here is a genuine invariant violation, not a normal
		// ErrNotFound.
		return ResolveResult{}, err
	}
	res.Customer = c

	if in.Name != "" && in.Name != c.DisplayName {
		if err := s.UpdateDisplayName(ctx, customerID, in.Name); err != nil {
			return ResolveResult{}, err
		}
		res.Customer.DisplayName = in.Name
	}

	// Attach strong identities the caller asked for that were not present.
	if addEmail {
		normEmail, err := NormalizeEmail(in.Email)
		if err == nil {
			id, err := s.InsertIdentity(ctx, customerID, KindEmail, normEmail, nil, source, nil)
			if err != nil {
				return ResolveResult{}, err
			}
			res.AttachedIdentities = append(res.AttachedIdentities, id)
		}
	}
	if addPhone {
		normPhone, err := NormalizePhone(in.Phone, in.DefaultRegion)
		if err == nil {
			id, err := s.InsertIdentity(ctx, customerID, KindPhone, normPhone, nil, source, nil)
			if err != nil {
				return ResolveResult{}, err
			}
			res.AttachedIdentities = append(res.AttachedIdentities, id)
		}
	}

	// Attach weak identities when supplied and not already present. We
	// blindly try the insert; a duplicate is fine (someone else raced us).
	if in.ChannelID != uuid.Nil {
		channelID := in.ChannelID
		if in.DeviceToken != "" {
			if _, err := s.GetIdentityByWeak(ctx, KindDevice, in.DeviceToken, channelID); errors.Is(err, ErrNotFound) {
				id, err := s.InsertIdentity(ctx, customerID, KindDevice, in.DeviceToken, &channelID, source, nil)
				if err != nil {
					return ResolveResult{}, err
				}
				res.AttachedIdentities = append(res.AttachedIdentities, id)
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return ResolveResult{}, err
			}
		}
		if in.WCCustomerID != "" {
			if _, err := s.GetIdentityByWeak(ctx, KindWCCustomer, in.WCCustomerID, channelID); errors.Is(err, ErrNotFound) {
				id, err := s.InsertIdentity(ctx, customerID, KindWCCustomer, in.WCCustomerID, &channelID, source, nil)
				if err != nil {
					return ResolveResult{}, err
				}
				res.AttachedIdentities = append(res.AttachedIdentities, id)
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return ResolveResult{}, err
			}
		}
	}

	// Stash raw invalid phone as an attribute (spec §3.2 last paragraph).
	if res.PhoneWasInvalid {
		raw := in.RawPhone
		if raw == "" {
			raw = in.Phone
		}
		valueJSON := jsonString(raw)
		if err := s.InsertAttribute(ctx, customerID, nil, AttrKeyInvalidPhone, valueJSON, source); err != nil {
			return ResolveResult{}, err
		}
	}

	return res, nil
}

// Touch bumps last_seen_at on an identity — used by CREATE_USER / seat-
// hold code paths that already know the identity id.
func Touch(ctx context.Context, s Store, identityID uuid.UUID) error {
	return s.TouchIdentity(ctx, identityID)
}

// MarkVerified sets verified_at (if currently NULL) and refreshes
// last_seen_at. Spec §12.2 step 5: only PAY_ORDER or explicit
// confirmation triggers this.
func MarkVerified(ctx context.Context, s Store, identityID uuid.UUID, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.MarkIdentityVerified(ctx, identityID, at)
}

// LinkOrg ensures a (customer_id, org_id) rollup row exists. The orders
// write path is responsible for maintaining the counters on top of this;
// spec §12.1.
func LinkOrg(ctx context.Context, s Store, customerID, orgID uuid.UUID, source string) error {
	if source == "" {
		source = "order"
	}
	return s.UpsertOrgLink(ctx, customerID, orgID, source)
}

// jsonString encodes raw as a JSON string literal. Kept tiny and local so
// callers of the customers package do not have to import encoding/json for
// what is a single string value; the ONLY user is the invalid-phone
// attribute writer above.
func jsonString(raw string) string {
	// Escape the minimum required by RFC 8259 §7: backslash, quote and
	// the ASCII control range. This is small enough to inline rather
	// than reach for encoding/json.
	var out []byte
	out = append(out, '"')
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		switch {
		case b == '\\' || b == '"':
			out = append(out, '\\', b)
		case b == '\n':
			out = append(out, '\\', 'n')
		case b == '\r':
			out = append(out, '\\', 'r')
		case b == '\t':
			out = append(out, '\\', 't')
		case b < 0x20:
			// \u00XX
			const hex = "0123456789abcdef"
			out = append(out, '\\', 'u', '0', '0', hex[b>>4], hex[b&0x0f])
		default:
			out = append(out, b)
		}
	}
	out = append(out, '"')
	return string(out)
}
