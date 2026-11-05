package hsaleschats

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orgmodules"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// Outcomes of a press (StepAnswer.Outcome).
const (
	// OutcomeApplied: the step was set.
	OutcomeApplied = "applied"
	// OutcomeUnchanged: the order already stood at that step.
	OutcomeUnchanged = "unchanged"
	// OutcomeStale: somebody moved the order on (in the group or in the event
	// center) and the pressed step no longer follows; the card shows the
	// current one.
	OutcomeStale = "stale"
	// OutcomeClosed: the order was refunded or cancelled: do not ship.
	OutcomeClosed = "closed"
)

// StepAnswer is the answer of the step and card routes: what happened, the
// order's step now and the card as it must look in the group.
type StepAnswer struct {
	Outcome     string                   `json:"outcome"`
	Step        *string                  `json:"fulfillment_step"`
	OrderNumber int64                    `json:"order_number"`
	Locale      string                   `json:"locale"`
	Card        salesnotify.DeliveryCard `json:"card"`
}

// errNotFound is the one answer for an unknown order, a chat that may not
// press its buttons, and an organization without a shop: the bot cannot tell
// them apart, and neither can someone probing ids.
func errNotFound(w http.ResponseWriter, r *http.Request) {
	writeErr(w, r, http.StatusNotFound, "shop_delivery.not_found",
		"this chat is not a delivery group of the order's organization, or the order does not exist")
}

// deliveryOrder resolves the order and checks that chatID may press its
// buttons; ok is false when the answer was written.
func (h *Handler) deliveryOrder(w http.ResponseWriter, r *http.Request, chatID, orderID string) (orgID, id uuid.UUID, ok bool) {
	id, err := uuid.Parse(strings.TrimSpace(orderID))
	if err != nil || strings.TrimSpace(chatID) == "" {
		errNotFound(w, r)
		return uuid.Nil, uuid.Nil, false
	}
	ctx := r.Context()
	err = h.pool.QueryRow(ctx, `SELECT org_id FROM shop_orders WHERE id = $1`, id).Scan(&orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		errNotFound(w, r)
		return uuid.Nil, uuid.Nil, false
	}
	if err != nil {
		h.fail(w, r, "read order", err)
		return uuid.Nil, uuid.Nil, false
	}
	if _, err := salesnotify.DeliveryChat(ctx, h.pool, chatID, orgID); err != nil {
		if errors.Is(err, salesnotify.ErrDeliveryGroup) {
			errNotFound(w, r)
		} else {
			h.fail(w, r, "check chat", err)
		}
		return uuid.Nil, uuid.Nil, false
	}
	mode, err := orgmodules.ShopMode(ctx, gen.New(h.pool), orgID, h.now())
	if err != nil {
		h.fail(w, r, "shop mode", err)
		return uuid.Nil, uuid.Nil, false
	}
	if mode == orgmodules.ModeNone {
		errNotFound(w, r)
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, id, true
}

// answer renders the card as it is now and writes it with outcome.
func (h *Handler) answer(w http.ResponseWriter, r *http.Request, orgID, orderID uuid.UUID, outcome string) {
	st, err := h.cards.CardState(r.Context(), orgID.String(), orderID.String())
	if err != nil {
		h.fail(w, r, "card state", err)
		return
	}
	out := StepAnswer{Outcome: outcome, OrderNumber: st.Shop.Order.Number, Locale: st.Shop.Locale,
		Card: salesnotify.RenderDeliveryCard(st)}
	if s := shop.CurrentStep(st.Shop.Order); s != "" {
		out.Step = &s
	}
	if outcome == OutcomeApplied && shop.OrderClosed(st.Shop.Order) {
		out.Outcome = OutcomeClosed
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

type stepBody struct {
	ChatID         string  `json:"chat_id"`
	TelegramUserID int64   `json:"telegram_user_id"`
	TelegramName   string  `json:"telegram_name"`
	OrderID        string  `json:"order_id"`
	Step           string  `json:"step"`
	Tracking       *string `json:"tracking"`
	Reason         string  `json:"reason"`
}

// HandleStep serves POST /v1/bot/shop-delivery/steps: a member of a delivery
// group pressed a step of a card (any member may, V-16). The step goes through
// shop.SetFulfillmentStep with the Telegram account as the actor and via
// delivery_group. The answer is 200 with the outcome and the card to show
// (applied, unchanged, stale, closed); 404 shop_delivery.not_found when the
// chat may not press the order's buttons; a shop refusal that is not about the
// step (a bad body) keeps its own status and code.
func (h *Handler) HandleStep(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var b stepBody
	if !decodeBody(w, r, &b) {
		return
	}
	if b.TelegramUserID == 0 {
		writeErr(w, r, http.StatusBadRequest, "sales_chat.invalid_body", "telegram_user_id is required")
		return
	}
	orgID, orderID, ok := h.deliveryOrder(w, r, b.ChatID, b.OrderID)
	if !ok {
		return
	}
	tg := b.TelegramUserID
	in := shop.StepInput{Tracking: b.Tracking, Reason: b.Reason, ActorTelegramID: &tg,
		ActorName: strings.TrimSpace(b.TelegramName), Via: shop.ViaDeliveryGroup}
	_, res, err := h.shop.ApplyFulfillmentStep(r.Context(), orgID, orderID, b.Step, in)
	if err != nil {
		if e, ok := shop.AsError(err); ok {
			switch e.Code {
			case "shop.fulfillment_order_closed":
				h.answer(w, r, orgID, orderID, OutcomeClosed)
				return
			case "shop.fulfillment_invalid_step", "shop.fulfillment_order_unpaid", "shop.fulfillment_order_in_review", "shop.order_not_shippable":
				h.answer(w, r, orgID, orderID, OutcomeStale)
				return
			}
			httputil.WriteJSON(w, e.Status, httputil.ErrorEnvelopeWithDetails(e.Code, e.Message, r, e.Details))
			return
		}
		h.fail(w, r, "set step", err)
		return
	}
	outcome := OutcomeApplied
	if !res.Changed {
		outcome = OutcomeUnchanged
	}
	h.answer(w, r, orgID, orderID, outcome)
}

type cardBody struct {
	ChatID  string `json:"chat_id"`
	OrderID string `json:"order_id"`
}

// HandleCard serves POST /v1/bot/shop-delivery/card: the card of an order as
// it is now, for a chat that may press its buttons ("back" on the card).
func (h *Handler) HandleCard(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var b cardBody
	if !decodeBody(w, r, &b) {
		return
	}
	orgID, orderID, ok := h.deliveryOrder(w, r, b.ChatID, b.OrderID)
	if !ok {
		return
	}
	h.answer(w, r, orgID, orderID, OutcomeUnchanged)
}

type connectBody struct {
	Code           string `json:"code"`
	ChatID         string `json:"chat_id"`
	ChatTitle      string `json:"chat_title"`
	TelegramUserID int64  `json:"telegram_user_id"`
	TelegramName   string `json:"telegram_name"`
}

// HandleConnect serves POST /v1/bot/sales-chats/connect: "/connect <code>"
// was written in a group. The code's chat is created or revived for this
// chat; the answer names the organization ("" for an operator chat) and the
// kinds. An unknown, used or expired code is 404 sales_chat.code_invalid.
func (h *Handler) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var b connectBody
	if !decodeBody(w, r, &b) {
		return
	}
	var out salesnotify.Connected
	err := h.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		out, err = salesnotify.ConnectChat(r.Context(), tx, b.Code, b.ChatID, b.ChatTitle, h.now())
		if err != nil {
			return err
		}
		return h.auditBot(r, tx, AuditConnected, out.Chat.ID, map[string]any{
			"org_id": out.Chat.OrgID, "chat_id": out.Chat.ChatID, "kinds": out.Chat.Kinds,
			"telegram_user_id": b.TelegramUserID, "telegram_name": b.TelegramName,
		})
	})
	if errors.Is(err, salesnotify.ErrCodeInvalid) {
		writeErr(w, r, http.StatusNotFound, "sales_chat.code_invalid", "this code is unknown, used or expired")
		return
	}
	if err != nil {
		h.fail(w, r, "connect chat", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"chat": out.Chat, "org_name": out.OrgName, "locale": out.Locale, "operator": out.Chat.OrgID == nil,
	})
}

type removedBody struct {
	ChatID string `json:"chat_id"`
}

// HandleRemoved serves POST /v1/bot/sales-chats/removed: Telegram said the bot
// left or was kicked from the chat; nothing is sent there any more.
func (h *Handler) HandleRemoved(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var b removedBody
	if !decodeBody(w, r, &b) {
		return
	}
	n, err := salesnotify.MarkBotRemoved(r.Context(), h.pool, b.ChatID, h.now())
	if err != nil {
		h.fail(w, r, "mark removed", err)
		return
	}
	if n > 0 {
		_ = h.auditBot(r, nil, AuditBotRemoved, strings.TrimSpace(b.ChatID), map[string]any{"chat_id": b.ChatID, "rows": n})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"marked": n})
}

type migratedBody struct {
	FromChatID string `json:"from_chat_id"`
	ToChatID   string `json:"to_chat_id"`
}

// HandleMigrated serves POST /v1/bot/sales-chats/migrated: a group became a
// supergroup, so presses now come from the new id.
func (h *Handler) HandleMigrated(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	var b migratedBody
	if !decodeBody(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.FromChatID) == "" || strings.TrimSpace(b.ToChatID) == "" {
		writeErr(w, r, http.StatusBadRequest, "sales_chat.invalid_body", "from_chat_id and to_chat_id are required")
		return
	}
	if err := salesnotify.MigrateChat(r.Context(), h.pool, b.FromChatID, b.ToChatID); err != nil {
		h.fail(w, r, "migrate chat", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"migrated": true})
}
