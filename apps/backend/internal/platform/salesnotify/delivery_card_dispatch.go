package salesnotify

// delivery_card_dispatch.go — sending the delivery card with its buttons,
// remembering it per chat and editing it in place (spec 61 section 7b).

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/geotz"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// EventShopOrderFulfillmentChanged: a shop order moved to another delivery
// step, from any surface (shop.SetFulfillmentStep).
const EventShopOrderFulfillmentChanged = shop.EventOrderFulfillmentChanged

// CardRef is one delivery card already in a chat.
type CardRef struct {
	SubscriptionID string
	ChatID         string
	MessageID      int64
}

// CardStore is what the cards need besides Store. PGStore implements it; a
// store without it sends the card as plain text, as before stage M5.
type CardStore interface {
	// CardState loads what the card of one order shows.
	CardState(ctx context.Context, orgID, orderID string) (CardState, error)
	// SaveCard remembers the card's message in a chat.
	SaveCard(ctx context.Context, orderID, subscriptionID, chatID string, messageID int64) error
	// Cards lists the order's cards in chats that still hear it (not
	// deleted, the bot not removed).
	Cards(ctx context.Context, orderID string) ([]CardRef, error)
}

// CardSender sends and edits a message with inline buttons. TelegramSender
// implements it.
type CardSender interface {
	SendCard(ctx context.Context, chatID, text string, rows [][]Button) (int64, error)
	EditCard(ctx context.Context, chatID string, messageID int64, text string, rows [][]Button) error
}

// cardsEnabled: both the store and the sender can do cards.
func (d *Dispatcher) cardsEnabled() (CardStore, CardSender, bool) {
	cs, ok1 := d.store.(CardStore)
	snd, ok2 := d.sender.(CardSender)
	return cs, snd, ok1 && ok2
}

// announceCards sends the delivery card with its buttons to every delivery
// group, claimed once under key, and remembers each message. It answers false
// (nothing sent, nothing claimed) when the buttons are off.
func (d *Dispatcher) announceCards(ctx context.Context, targets []Subscription, key, orgID, orderID string) bool {
	if !d.deliveryButtons {
		// Buttons off: the caller sends the plain text message of before
		// M5. No message id is stored, there is no card to edit later.
		return false
	}
	cs, snd, ok := d.cardsEnabled()
	if !ok || len(targets) == 0 {
		return false
	}
	st, err := cs.CardState(ctx, orgID, orderID)
	if err != nil {
		d.logger.Warn("sales.notify: card state", "order_id", orderID, "error", err.Error())
		return false
	}
	first, err := d.store.Claim(ctx, key)
	if err != nil {
		d.logger.Warn("sales.notify: claim", "key", key, "error", err.Error())
		return true
	}
	if !first {
		return true // an outbox retry
	}
	card := RenderDeliveryCard(st)
	for _, sub := range targets {
		chat, msgID, err := d.deliverCard(ctx, snd, sub, card)
		if err != nil {
			continue
		}
		if err := cs.SaveCard(ctx, orderID, sub.ID, chat, msgID); err != nil {
			d.logger.Warn("sales.notify: could not remember the card", "subscription", sub.Name, "error", err.Error())
		}
	}
	return true
}

// deliverCard is deliver for a card: it follows a supergroup migration once
// and records the outcome. It answers the chat the card landed in.
func (d *Dispatcher) deliverCard(ctx context.Context, snd CardSender, sub Subscription, card DeliveryCard) (string, int64, error) {
	chat := sub.ChatID
	msgID, err := snd.SendCard(ctx, chat, card.Text, card.Rows)
	var mig *MigratedError
	if errors.As(err, &mig) {
		d.logger.Info("sales.notify: chat upgraded to a supergroup", "subscription", sub.Name, "from", sub.ChatID, "to", mig.NewChatID)
		if uerr := d.store.UpdateChatID(ctx, sub.ID, mig.NewChatID); uerr != nil {
			d.logger.Warn("sales.notify: could not store the new chat id", "subscription", sub.Name, "error", uerr.Error())
		}
		chat = mig.NewChatID
		msgID, err = snd.SendCard(ctx, chat, card.Text, card.Rows)
	}
	lastErr := ""
	if err != nil {
		lastErr = err.Error()
		d.logger.Warn("sales.notify: delivery failed", "subscription", sub.Name, "error", lastErr)
	}
	if rerr := d.store.RecordDelivery(ctx, sub.ID, lastErr); rerr != nil {
		d.logger.Warn("sales.notify: could not record delivery", "subscription", sub.Name, "error", rerr.Error())
	}
	return chat, msgID, err
}

// RefreshCards edits every card of the order to what it shows now. Best
// effort: a card Telegram no longer knows is logged and skipped, and an edit
// that changes nothing ("message is not modified") is no error.
func (d *Dispatcher) RefreshCards(ctx context.Context, orgID, orderID string) {
	cs, snd, ok := d.cardsEnabled()
	if !ok {
		return
	}
	refs, err := cs.Cards(ctx, orderID)
	if err != nil {
		d.logger.Warn("sales.notify: list cards", "order_id", orderID, "error", err.Error())
		return
	}
	if len(refs) == 0 {
		return
	}
	st, err := cs.CardState(ctx, orgID, orderID)
	if err != nil {
		d.logger.Warn("sales.notify: card state", "order_id", orderID, "error", err.Error())
		return
	}
	card := RenderDeliveryCard(st)
	rows := card.Rows
	if !d.deliveryButtons {
		rows = nil // empty rows remove the buttons of a card sent earlier
	}
	for _, ref := range refs {
		if err := snd.EditCard(ctx, ref.ChatID, ref.MessageID, card.Text, rows); err != nil && !IsNotModified(err) {
			d.logger.Warn("sales.notify: card edit failed", "order_id", orderID, "chat_id", ref.ChatID, "error", err.Error())
		}
	}
}

// IsNotModified reports Telegram's answer to an edit that changes nothing.
func IsNotModified(err error) bool {
	return err != nil && strings.Contains(err.Error(), "message is not modified")
}

// handleShopStep reacts to a step change: every card of the order is edited,
// and a failed delivery is told to the sales chats (nothing else of the steps
// goes there).
func (d *Dispatcher) handleShopStep(ctx context.Context, ev outbox.Event) {
	id := payloadString(ev.Payload, "order_id", ev.AggregateID)
	orgID := payloadString(ev.Payload, "org_id", "")
	ok, err := d.shopAllowed(ctx, orgID)
	if err != nil {
		d.logger.Warn("sales.notify: shop mode", "org_id", orgID, "error", err.Error())
		return
	}
	if !ok {
		return
	}
	d.RefreshCards(ctx, orgID, id)
	if payloadString(ev.Payload, "fulfillment_step", "") != shop.StepFailed {
		return
	}
	order, err := d.store.ShopOrder(ctx, orgID, id)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			d.logger.Warn("sales.notify: could not build notification", "event_type", ev.EventType, "error", err.Error())
		}
		return
	}
	subs, err := d.store.Subscriptions(ctx)
	if err != nil {
		d.logger.Warn("sales.notify: load subscriptions", "error", err.Error())
		return
	}
	// The chats that hear the fact of a sale and not the card: a delivery
	// group already sees "not delivered" on the card itself.
	sale, _ := RouteShop(subs, orgID, TriggerShopOrderPaid, true)
	key := "shop_failed:" + payloadString(ev.Payload, "event_id", ev.ID)
	d.announce(ctx, sale, key, FormatShopDeliveryFailed(order, payloadString(ev.Payload, "reason", "")))
}

// CardState implements CardStore.
func (s *PGStore) CardState(ctx context.Context, orgID, orderID string) (CardState, error) {
	so, err := s.ShopOrder(ctx, orgID, orderID)
	if err != nil {
		return CardState{}, err
	}
	q := gen.New(s.pool)
	st := CardState{Shop: so, Now: time.Now()}
	last, ok, err := q.LatestShopOrderEvent(ctx, so.Order.ID)
	if err != nil {
		return CardState{}, err
	}
	if ok {
		st.Last = &last
	}
	st.Zone = s.orgZone(ctx, so.Order.OrgID)
	return st, nil
}

// orgZone is the organization's time zone for the card: the shop's country
// when it has one zone, else the zone of most of its venues, else UTC.
func (s *PGStore) orgZone(ctx context.Context, orgID uuid.UUID) *time.Location {
	var country, venueZone string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT country_iso2 FROM shop_settings WHERE org_id = $1), ''),
		       COALESCE((SELECT timezone FROM venues WHERE org_id = $1 AND timezone <> ''
		                  GROUP BY timezone ORDER BY count(*) DESC, timezone LIMIT 1), '')`, orgID).Scan(&country, &venueZone)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return time.UTC
	}
	for _, z := range []string{geotz.SingleZone(strings.TrimSpace(country)), venueZone} {
		if z == "" {
			continue
		}
		if loc, err := time.LoadLocation(z); err == nil {
			return loc
		}
	}
	return time.UTC
}

// SaveCard implements CardStore.
func (s *PGStore) SaveCard(ctx context.Context, orderID, subscriptionID, chatID string, messageID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sales_notification_cards (order_id, subscription_id, chat_id, message_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (order_id, subscription_id)
		DO UPDATE SET chat_id = EXCLUDED.chat_id, message_id = EXCLUDED.message_id, updated_at = now()`,
		orderID, subscriptionID, chatID, messageID)
	return err
}

// Cards implements CardStore.
func (s *PGStore) Cards(ctx context.Context, orderID string) ([]CardRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.subscription_id::text, c.chat_id, c.message_id
		  FROM sales_notification_cards c
		  JOIN sales_notification_subscriptions s ON s.id = c.subscription_id
		 WHERE c.order_id = $1 AND s.deleted_at IS NULL AND s.bot_removed_at IS NULL
		 ORDER BY c.created_at`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CardRef
	for rows.Next() {
		var r CardRef
		if err := rows.Scan(&r.SubscriptionID, &r.ChatID, &r.MessageID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
