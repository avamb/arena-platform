package hsalelinks

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func idPtr(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func strPtr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// auditPatch writes the audit rows of one PATCH inside its transaction: a
// retarget (with the old and the new target), an archive or unarchive, and an
// update for the label, placement or utm. A request that changes nothing a
// reader would care about writes none.
func (h *Handler) auditPatch(ctx context.Context, tx pgx.Tx, r *http.Request, before, after gen.SalesLinkRow,
	params gen.UpdateSalesLinkParams, retarget bool, to resolvedTarget) error {
	if retarget {
		if err := h.writeAudit(ctx, tx, r, "v1.sales_link.retarget", after, map[string]any{
			"from": rowTargetMeta(before), "to": newTargetMeta(to),
		}); err != nil {
			return err
		}
	}
	if params.SetStatus && params.Status != before.Status {
		action := "v1.sales_link.archive"
		if params.Status == "active" {
			action = "v1.sales_link.unarchive"
		}
		if err := h.writeAudit(ctx, tx, r, action, after, map[string]any{"from_status": before.Status, "to_status": params.Status}); err != nil {
			return err
		}
	}
	if params.SetLabel || params.SetPlacementKind || params.SetUTM {
		fields := []string{}
		if params.SetLabel {
			fields = append(fields, "label")
		}
		if params.SetPlacementKind {
			fields = append(fields, "placement_kind")
		}
		if params.SetUTM {
			fields = append(fields, "utm")
		}
		return h.writeAudit(ctx, tx, r, "v1.sales_link.update", after, map[string]any{"fields": fields})
	}
	return nil
}
