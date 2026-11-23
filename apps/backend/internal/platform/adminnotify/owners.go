package adminnotify

// owners.go - personal notices to the OWNERS of one organization (not to
// platform admins): the same e-mail and Telegram jobs, the same handlers, the
// same "the caller owns de-duplication" rule.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// KindMetaConnectionBroken is the notice that an organization's Meta
// connection stopped working (Graph code 190, spec 66 section 4.4).
const KindMetaConnectionBroken = "meta_connection_broken"

// OwnerRecipients lists the owners of orgID: users with an ACTIVE membership
// whose membership role holds org.update, not deactivated, of a live
// organization, with their live Telegram links. A global role does not make
// a person an owner of the organization.
func OwnerRecipients(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, orgID uuid.UUID) ([]Recipient, error) {
	rows, err := q.Query(ctx, `
SELECT u.id, u.email, coalesce((SELECT array_agg(l.telegram_user_id ORDER BY l.telegram_user_id)
                                FROM bot_telegram_links l WHERE l.user_id = u.id AND l.revoked_at IS NULL), '{}')
FROM memberships m
JOIN users u         ON u.id = m.user_id AND u.deactivated_at IS NULL
JOIN organizations o ON o.id = m.org_id AND o.deleted_at IS NULL
WHERE m.org_id = $1 AND m.status = 'active'
  AND EXISTS (SELECT 1 FROM roles r
              JOIN role_permissions rp ON rp.role_id = r.id
              JOIN permissions p       ON p.id = rp.permission_id AND p.name = 'org.update'
              WHERE r.org_id IS NULL AND r.name = m.role)
ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		var a Recipient
		if err := rows.Scan(&a.UserID, &a.Email, &a.Chats); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MetaConnectionBrokenMessage is the owner's notice of a broken Meta
// connection. It names the organization and the page to connect again and
// carries no token and no text of Meta. An empty eventCenterURL leaves the
// link out.
func MetaConnectionBrokenMessage(orgName, eventCenterURL string, orgID uuid.UUID) Message {
	msg := Message{
		Kind:        KindMetaConnectionBroken,
		Icon:        "⚠️",
		Title:       "Подключение Meta организации " + strings.TrimSpace(orgName) + " перестало работать",
		Details:     []string{"Покупки больше не уходят в Meta с сервера."},
		ButtonLabel: "Подключить Meta",
	}
	if base := strings.TrimRight(strings.TrimSpace(eventCenterURL), "/"); base != "" {
		msg.WhatToDo = "Подключите Meta заново:"
		msg.Link = base + "/o/" + orgID.String() + "/ads/meta"
	} else {
		msg.WhatToDo = "Подключите Meta заново в разделе «Реклама» ивент-центра."
	}
	return msg
}

// NotifyMetaConnectionBroken queues the notice of a broken Meta connection
// for every owner of orgID in the caller's transaction. Call it ONLY in the
// transaction that moved the connection from active to error, so the owners
// are told once per break. A deleted organization gets nothing. It answers
// how many owners were told.
func NotifyMetaConnectionBroken(ctx context.Context, tx interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, orgID uuid.UUID, eventCenterURL string, opts Options) (int, error) {
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1 AND deleted_at IS NULL`, orgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("adminnotify: load organization: %w", err)
	}
	owners, err := OwnerRecipients(ctx, tx, orgID)
	if err != nil {
		return 0, fmt.Errorf("adminnotify: load owners: %w", err)
	}
	return FanoutTo(ctx, tx, owners, MetaConnectionBrokenMessage(name, eventCenterURL, orgID), opts)
}
