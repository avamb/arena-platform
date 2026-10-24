package attribution

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// LinkQuerier is the one read ResolveLink needs. *gen.Queries satisfies it;
// callers on a money path pass a pool-bound handle and call ResolveLink BEFORE
// they open their transaction.
type LinkQuerier interface {
	GetSalesLinkByCode(ctx context.Context, code string) (gen.SalesLinkRow, error)
}

// ResolveLink turns the ref of a cleaned attribution into the id of the sales
// link the order is attributed to (spec 43 section 3: "ref goes to
// orders.sales_link_id ONLY when the link exists and belongs to the
// organization of the order").
//
//   - no ref: nil, nil;
//   - an unknown code: nil, nil (the json keeps the ref, the order gets no link);
//   - a link of ANOTHER organization: nil, nil (same, nothing about the other
//     organization is revealed);
//   - an archived link of the organization still resolves: it exists and the
//     buyer came through it;
//   - any other lookup failure is returned. Callers treat it as "no link" and
//     log it: a label must never cost a sale.
func ResolveLink(ctx context.Context, q LinkQuerier, orgID uuid.UUID, a Attribution) (*uuid.UUID, error) {
	ref, ok := NormalizeRef(a.Ref)
	if !ok || q == nil {
		return nil, nil
	}
	link, err := q.GetSalesLinkByCode(ctx, ref)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if link.OrgID != orgID {
		return nil, nil
	}
	id := link.ID
	return &id, nil
}
