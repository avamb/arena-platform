package hsaleschats

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// chatErr answers a refusal of the chat functions with its code; anything
// else is a logged 500.
func (h *Handler) chatErr(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, salesnotify.ErrChatNotFound):
		writeErr(w, r, http.StatusNotFound, "sales_chat.not_found", "this chat does not exist or was deleted")
	case errors.Is(err, salesnotify.ErrInvalidKinds):
		writeErr(w, r, http.StatusUnprocessableEntity, "sales_chat.invalid_kinds",
			"kinds must list at least one of tickets, shop_sale, shop_delivery")
	case errors.Is(err, salesnotify.ErrInvalidName):
		writeErr(w, r, http.StatusUnprocessableEntity, "sales_chat.invalid_name", "name must be 1 to 200 characters")
	default:
		h.fail(w, r, what, err)
	}
}

// orgExists answers 404 sales_chat.organization_not_found for an unknown
// organization.
func (h *Handler) orgExists(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) bool {
	var ok bool
	if err := h.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1)`, orgID).Scan(&ok); err != nil {
		h.fail(w, r, "check organization", err)
		return false
	}
	if !ok {
		writeErr(w, r, http.StatusNotFound, "sales_chat.organization_not_found", "this organization does not exist")
	}
	return ok
}

// HandleListOrg serves GET /v1/admin/organizations/{org_id}/sales-chats.
func (h *Handler) HandleListOrg(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !h.orgExists(w, r, orgID) {
		return
	}
	h.list(w, r, &orgID)
}

// HandleListOperator serves GET /v1/admin/sales-chats/operator: the chats
// that hear every organization (org_id NULL).
func (h *Handler) HandleListOperator(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	h.list(w, r, nil)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID) {
	items, err := salesnotify.ListChats(r.Context(), h.pool, orgID)
	if err != nil {
		h.fail(w, r, "list chats", err)
		return
	}
	var org any
	if orgID != nil {
		org = orgID.String()
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"org_id": org, "items": items})
}

type codeBody struct {
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
}

// HandleCreateOrgCode serves POST
// /v1/admin/organizations/{org_id}/sales-chats/connect-codes {name, kinds}.
func (h *Handler) HandleCreateOrgCode(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok || !h.orgExists(w, r, orgID) {
		return
	}
	h.createCode(w, r, &orgID, reason)
}

// HandleCreateOperatorCode serves POST
// /v1/admin/sales-chats/operator/connect-codes {name, kinds}.
func (h *Handler) HandleCreateOperatorCode(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	h.createCode(w, r, nil, reason)
}

// createCode stores a one-time code; the answer carries the code once (only
// its hash is kept) and the command to write in the group.
func (h *Handler) createCode(w http.ResponseWriter, r *http.Request, orgID *uuid.UUID, reason string) {
	var b codeBody
	if !decodeBody(w, r, &b) {
		return
	}
	var out salesnotify.ConnectCode
	err := h.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = salesnotify.CreateConnectCode(r.Context(), tx, orgID, b.Name, b.Kinds, actorUUID(r), h.now())
		if err != nil {
			return err
		}
		var org *string
		if orgID != nil {
			s := orgID.String()
			org = &s
		}
		// The code itself never goes into the audit trail.
		return h.auditTx(r, tx, AuditConnectCode, out.ID, org, reason, map[string]any{"name": out.Name, "kinds": out.Kinds})
	})
	if err != nil {
		h.chatErr(w, r, "create connect code", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusCreated, out)
}

type patchBody struct {
	Name    *string  `json:"name"`
	Kinds   []string `json:"kinds"`
	Allowed *bool    `json:"allowed"`
}

// HandlePatch serves PATCH /v1/admin/sales-chats/{id} {name?, kinds?,
// allowed?}.
func (h *Handler) HandlePatch(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var b patchBody
	if !decodeBody(w, r, &b) {
		return
	}
	if b.Name == nil && b.Kinds == nil && b.Allowed == nil {
		writeErr(w, r, http.StatusBadRequest, "sales_chat.invalid_body", "send name, kinds or allowed")
		return
	}
	var out salesnotify.Chat
	err := h.inTx(r.Context(), func(tx pgx.Tx) error {
		before, err := salesnotify.GetChat(r.Context(), tx, id, true)
		if err != nil {
			return err
		}
		out, err = salesnotify.PatchChat(r.Context(), tx, id, salesnotify.ChatPatch{Name: b.Name, Kinds: b.Kinds, Allowed: b.Allowed})
		if err != nil {
			return err
		}
		return h.auditTx(r, tx, AuditUpdate, id.String(), out.OrgID, reason, map[string]any{
			"before": map[string]any{"name": before.Name, "kinds": before.Kinds, "allowed": before.Allowed},
			"after":  map[string]any{"name": out.Name, "kinds": out.Kinds, "allowed": out.Allowed},
		})
	})
	if err != nil {
		h.chatErr(w, r, "update chat", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// HandleDelete serves DELETE /v1/admin/sales-chats/{id}: the chat is marked
// deleted, nothing is sent there any more and its delivery history stays.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var out salesnotify.Chat
	err := h.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = salesnotify.DeleteChat(r.Context(), tx, id)
		if err != nil {
			return err
		}
		return h.auditTx(r, tx, AuditDelete, id.String(), out.OrgID, reason, map[string]any{"name": out.Name, "chat_id": out.ChatID})
	})
	if err != nil {
		h.chatErr(w, r, "delete chat", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": out.ID})
}
