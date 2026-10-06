package hiam

// admin_user_names.go — the optional first and last name of a user (migration
// 0123). Both are never required: an invited user has only an e-mail until
// somebody fills the name in, and the admin console shows the e-mail either way.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const maxUserNameRunes = 100

// cleanUserName trims a name and turns an empty one into nil. It reports false
// for a name longer than maxUserNameRunes.
func cleanUserName(raw *string) (*string, bool) {
	if raw == nil {
		return nil, true
	}
	v := strings.Join(strings.Fields(*raw), " ")
	if v == "" {
		return nil, true
	}
	if utf8.RuneCountInString(v) > maxUserNameRunes {
		return nil, false
	}
	return &v, true
}

// adminUserNamePatch is the body of PATCH /v1/admin/users/{user_id}: a key that
// is absent leaves that name as it is, null or "" clears it.
type adminUserNamePatch struct {
	FirstName   *string
	FirstNameIn bool
	LastName    *string
	LastNameIn  bool
}

func (p *adminUserNamePatch) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	read := func(key string, dst **string, in *bool) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		*in = true
		if string(v) == "null" {
			return nil
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return err
		}
		*dst = &s
		return nil
	}
	if err := read("first_name", &p.FirstName, &p.FirstNameIn); err != nil {
		return err
	}
	return read("last_name", &p.LastName, &p.LastNameIn)
}

// HandleAdminUpdateUser serves PATCH /v1/admin/users/{user_id}: sets or clears
// the user's optional first and last name.
func (h *Handler) HandleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	if h.membershipQueries == nil || h.pool == nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	reason, ok := requireAdminReason(w, r)
	if !ok {
		return
	}
	userID, ok := httputil.UUIDPathParam(w, r, "user_id")
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil || len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.empty_body", "request body is required", r,
		))
		return
	}
	var req adminUserNamePatch
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.invalid_json", "request body is not valid JSON", r,
		))
		return
	}
	if !req.FirstNameIn && !req.LastNameIn {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"admin_user.nothing_to_update", "send first_name and/or last_name", r,
		))
		return
	}
	ctx := r.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Read the current names first so an absent key keeps its value.
	var curFirst, curLast *string
	if err := tx.QueryRow(ctx, `SELECT first_name, last_name FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&curFirst, &curLast); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"admin_user.not_found", "user not found", r,
			))
			return
		}
		h.logger.Error("admin_user: read name failed", slog.String("error", err.Error()))
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	first, last := curFirst, curLast
	valid := true
	if req.FirstNameIn {
		first, valid = cleanUserName(req.FirstName)
	}
	if valid && req.LastNameIn {
		last, valid = cleanUserName(req.LastName)
	}
	if !valid {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"admin_user.name_too_long", "a name may be at most 100 characters", r,
		))
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET first_name = $2, last_name = $3 WHERE id = $1`, userID, first, last); err != nil {
		h.logger.Error("admin_user: update name failed", slog.String("error", err.Error()))
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	if !h.writeAdminUserLifecycleAudit(r, tx, "v1.admin.user.update_name", userID, reason) {
		h.writeAdminGlobalRoleError(w, r, http.StatusInternalServerError, "admin_user.audit_failed", "failed to write audit event")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.adminRoleDependencyUnavailable(w, r)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id": userID.String(), "first_name": first, "last_name": last,
	})
}
