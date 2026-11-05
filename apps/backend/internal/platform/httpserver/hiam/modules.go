package hiam

// modules.go — the paid modules of an organization on the identity answers
// (spec 08_architecture/61 section 4.3): every membership of GET /v1/me and
// every organization of GET /v1/organizations (the Telegram bot's chooser of
// a platform superadmin) carry `modules` and `shop_mode`. The states of all
// the listed organizations are read in ONE query (orgmodules.States).

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
)

// moduleQuerier is the query set the module states are read with: the /v1/me
// store when it is the real *gen.Queries, else the organization queries. Nil
// when neither can (a unit test's fake store): the fields then stay empty.
func (h *Handler) moduleQuerier() orgmodules.Querier {
	if q, ok := h.meQueries.(orgmodules.Querier); ok {
		return q
	}
	if h.orgQueries != nil {
		return h.orgQueries
	}
	return nil
}

// attachModules reads the module states of orgIDs in one query and hands
// each to set. Nothing is read for an empty list or without a store.
func (h *Handler) attachModules(ctx context.Context, orgIDs []uuid.UUID, set func(uuid.UUID, orgmodules.State)) error {
	q := h.moduleQuerier()
	if q == nil || len(orgIDs) == 0 {
		return nil
	}
	states, err := orgmodules.States(ctx, q, orgIDs, time.Now())
	if err != nil {
		return err
	}
	for id, st := range states {
		set(id, st)
	}
	return nil
}

// moduleFields renders a state as the `modules` (never null) and `shop_mode`
// (null for none) fields.
func moduleFields(st orgmodules.State) ([]string, *string) {
	mods := st.Modules
	if mods == nil {
		mods = []string{}
	}
	if st.ShopMode == orgmodules.ModeNone {
		return mods, nil
	}
	m := string(st.ShopMode)
	return mods, &m
}

// fillMembershipModules sets modules and shop_mode on every membership of
// /v1/me (dtos are membershipsToDTO(rows), in the same order).
func (h *Handler) fillMembershipModules(ctx context.Context, rows []gen.MembershipRow, dtos []meMembershipDTO) error {
	states := map[uuid.UUID]orgmodules.State{}
	if err := h.attachModules(ctx, orgIDsOfMemberships(rows), func(id uuid.UUID, st orgmodules.State) {
		states[id] = st
	}); err != nil {
		return err
	}
	for i, m := range rows {
		if st, ok := states[m.OrgID]; ok && i < len(dtos) {
			dtos[i].Modules, dtos[i].ShopMode = moduleFields(st)
		}
	}
	return nil
}

func orgIDsOfMemberships(rows []gen.MembershipRow) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(rows))
	out := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		if !seen[m.OrgID] {
			seen[m.OrgID] = true
			out = append(out, m.OrgID)
		}
	}
	return out
}

// orgListItem is one organization of GET /v1/organizations: the organization
// plus its modules today.
type orgListItem struct {
	OrgResponse
	Modules  []string `json:"modules"`
	ShopMode *string  `json:"shop_mode"`
}
