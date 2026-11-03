package refunds

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// AvailableBalance reads what orgID's own provider account can spend now in
// currency, through the same module (and so the same key) the refund would go
// through. supported is false when the provider offers no balance call (only
// Stripe does), and when the module cannot be built: the caller then sends
// the refund without asking, and the refund itself meets whatever is wrong
// with the configuration, exactly as before the balance check existed. An
// error means the provider did not answer the balance call: the balance is
// not known right now, which is never a refusal.
func (e *Engine) AvailableBalance(ctx context.Context, orgID uuid.UUID, provider, currency string) (amount int64, supported bool, err error) {
	if _, ok := e.modules.Descriptor(provider); !ok {
		return 0, false, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	module, berr := e.modules.Build(callCtx, orgID, provider)
	if berr != nil {
		e.logger.Warn("refunds: balance not read, the module could not be built",
			"org_id", orgID.String(), "provider", provider, "error", berr.Error())
		return 0, false, nil
	}
	reader, ok := module.(payments.BalanceReader)
	if !ok {
		return 0, false, nil
	}
	amount, err = reader.AvailableBalance(callCtx, currency)
	if err != nil {
		return 0, true, fmt.Errorf("refunds: read the %s balance: %w", provider, err)
	}
	return amount, true, nil
}
