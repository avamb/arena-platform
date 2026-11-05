package salesnotify

// chats.go — the sales bot's chats as an operator manages them (spec
// 08_architecture/61 section 7a.4, V-14): the list per organization (and the
// operator's own, org_id NULL), one-time connection codes, the binding a code
// makes when "/connect <code>" is written in a group, the edits of the admin
// screen and the soft delete, and the mark Telegram's my_chat_member leaves
// when the bot is removed from a group.
//
// The functions take a gen.DBTX, so the admin routes run them on the
// transaction that also carries their audit row.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ConnectCodeTTL is how long a connection code works.
const ConnectCodeTTL = 30 * time.Minute

// Errors of the chat functions.
var (
	ErrChatNotFound  = errors.New("salesnotify: chat not found")
	ErrCodeInvalid   = errors.New("salesnotify: connect code unknown, used or expired")
	ErrInvalidKinds  = errors.New("salesnotify: kinds must be a non-empty list of tickets, shop_sale, shop_delivery")
	ErrInvalidName   = errors.New("salesnotify: name must be 1 to 200 characters")
	ErrDeliveryGroup = errors.New("salesnotify: the chat is not a delivery group of this organization")
)

// Chat is one row of sales_notification_subscriptions as the admin screen
// shows it.
type Chat struct {
	ID               string     `json:"id"`
	OrgID            *string    `json:"org_id"`
	Name             string     `json:"name"`
	ChatID           string     `json:"chat_id"`
	ChatTitle        *string    `json:"chat_title"`
	Kinds            []string   `json:"kinds"`
	Allowed          bool       `json:"allowed"`
	OnOrderPaid      bool       `json:"on_order_paid"`
	OnTicketRefunded bool       `json:"on_ticket_refunded"`
	LastError        *string    `json:"last_error"`
	BotRemovedAt     *time.Time `json:"bot_removed_at"`
	ConnectedAt      *time.Time `json:"connected_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

const chatColumns = `id::text, org_id::text, name, chat_id, chat_title, kinds, allowed, on_order_paid,
       on_ticket_refunded, last_error, bot_removed_at, connected_at, created_at, updated_at`

func scanChat(row interface{ Scan(dest ...any) error }) (Chat, error) {
	var c Chat
	err := row.Scan(&c.ID, &c.OrgID, &c.Name, &c.ChatID, &c.ChatTitle, &c.Kinds, &c.Allowed, &c.OnOrderPaid,
		&c.OnTicketRefunded, &c.LastError, &c.BotRemovedAt, &c.ConnectedAt, &c.CreatedAt, &c.UpdatedAt)
	if len(c.Kinds) == 0 {
		c.Kinds = []string{KindTickets}
	}
	return c, err
}

// NormalizeKinds validates a kinds list: known values only, at least one,
// without repeats, in a fixed order.
func NormalizeKinds(kinds []string) ([]string, error) {
	seen := map[string]bool{}
	for _, k := range kinds {
		k = strings.TrimSpace(k)
		switch k {
		case KindTickets, KindShopSale, KindShopDelivery:
			seen[k] = true
		default:
			return nil, ErrInvalidKinds
		}
	}
	if len(seen) == 0 {
		return nil, ErrInvalidKinds
	}
	out := make([]string, 0, len(seen))
	for _, k := range []string{KindTickets, KindShopSale, KindShopDelivery} {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n < 1 || n > 200 {
		return "", ErrInvalidName
	}
	return name, nil
}

// ListChats lists the chats of an organization (orgID nil: the operator's,
// org_id NULL), deleted ones left out, oldest first.
func ListChats(ctx context.Context, db gen.DBTX, orgID *uuid.UUID) ([]Chat, error) {
	rows, err := db.Query(ctx, `SELECT `+chatColumns+`
		  FROM sales_notification_subscriptions
		 WHERE deleted_at IS NULL AND org_id IS NOT DISTINCT FROM $1
		 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChat reads one chat that is not deleted, locking it when forUpdate.
func GetChat(ctx context.Context, db gen.DBTX, id uuid.UUID, forUpdate bool) (Chat, error) {
	sql := `SELECT ` + chatColumns + ` FROM sales_notification_subscriptions WHERE id = $1 AND deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	c, err := scanChat(db.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrChatNotFound
	}
	return c, err
}

// ChatPatch is an edit of the admin screen; nil fields stay.
type ChatPatch struct {
	Name    *string
	Kinds   []string
	Allowed *bool
}

// PatchChat applies an edit to a chat that is not deleted.
func PatchChat(ctx context.Context, db gen.DBTX, id uuid.UUID, p ChatPatch) (Chat, error) {
	var name *string
	if p.Name != nil {
		n, err := normalizeName(*p.Name)
		if err != nil {
			return Chat{}, err
		}
		name = &n
	}
	var kinds []string
	if p.Kinds != nil {
		k, err := NormalizeKinds(p.Kinds)
		if err != nil {
			return Chat{}, err
		}
		kinds = k
	}
	c, err := scanChat(db.QueryRow(ctx, `
		UPDATE sales_notification_subscriptions
		   SET name = COALESCE($2, name),
		       kinds = COALESCE($3::text[], kinds),
		       allowed = COALESCE($4, allowed),
		       updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+chatColumns, id, name, kinds, p.Allowed))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrChatNotFound
	}
	return c, err
}

// DeleteChat marks a chat deleted: nothing is sent there any more, the
// delivery history stays.
func DeleteChat(ctx context.Context, db gen.DBTX, id uuid.UUID) (Chat, error) {
	c, err := scanChat(db.QueryRow(ctx, `
		UPDATE sales_notification_subscriptions SET deleted_at = now(), updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL
		RETURNING `+chatColumns, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrChatNotFound
	}
	return c, err
}

// connectCodeAlphabet is Crockford's base32 without I, L, O, U: nothing to
// confuse when the code is read off a screen and typed into a group.
const connectCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newConnectCode draws a 10-character code (50 bits) shown as XXXXX-XXXXX:
// it cannot be guessed inside its 30 minutes.
func newConnectCode() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 11)
	for i, v := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, connectCodeAlphabet[int(v)%len(connectCodeAlphabet)])
	}
	return string(out), nil
}

// normalizeConnectCode folds what a person typed into the stored form:
// upper case, the dash and spaces dropped, O read as 0 and I/L as 1.
func normalizeConnectCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(code)) {
		switch r {
		case '-', ' ':
		case 'O':
			b.WriteByte('0')
		case 'I', 'L':
			b.WriteByte('1')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hashConnectCode(code string) string {
	sum := sha256.Sum256([]byte("sales-chat-connect:" + normalizeConnectCode(code)))
	return hex.EncodeToString(sum[:])
}

// ConnectCode is a code an operator just created.
type ConnectCode struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
	Name      string    `json:"name"`
	Kinds     []string  `json:"kinds"`
}

// CreateConnectCode stores a one-time code for a chat of orgID (nil: an
// operator chat) with its name and kinds. Only the hash is stored; the code
// itself is answered once.
func CreateConnectCode(ctx context.Context, db gen.DBTX, orgID *uuid.UUID, name string, kinds []string, createdBy *uuid.UUID, now time.Time) (ConnectCode, error) {
	n, err := normalizeName(name)
	if err != nil {
		return ConnectCode{}, err
	}
	k, err := NormalizeKinds(kinds)
	if err != nil {
		return ConnectCode{}, err
	}
	code, err := newConnectCode()
	if err != nil {
		return ConnectCode{}, err
	}
	exp := now.Add(ConnectCodeTTL)
	var id string
	if err := db.QueryRow(ctx, `
		INSERT INTO sales_chat_connect_codes (code_hash, org_id, name, kinds, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`,
		hashConnectCode(code), orgID, n, k, exp, createdBy).Scan(&id); err != nil {
		return ConnectCode{}, err
	}
	return ConnectCode{ID: id, Code: code, Command: "/connect " + code, ExpiresAt: exp, Name: n, Kinds: k}, nil
}

// Connected is what a /connect bound.
type Connected struct {
	Chat    Chat
	OrgName string // "" for an operator chat
	Locale  string // the organization's language, "en" for an operator chat
}

// ConnectChat redeems a code for chatID: the subscription of the code's
// organization and this chat is created, or revived and rewritten (name,
// kinds, switched on, not deleted, bot not removed), and the code is spent.
// An unknown, used or expired code is ErrCodeInvalid. Run it in a
// transaction: the code is locked while it is spent.
func ConnectChat(ctx context.Context, db gen.DBTX, code, chatID, chatTitle string, now time.Time) (Connected, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || normalizeConnectCode(code) == "" {
		return Connected{}, ErrCodeInvalid
	}
	var (
		codeID string
		orgID  *uuid.UUID
		name   string
		kinds  []string
	)
	err := db.QueryRow(ctx, `
		SELECT id::text, org_id, name, kinds FROM sales_chat_connect_codes
		 WHERE code_hash = $1 AND used_at IS NULL AND expires_at > $2
		 FOR UPDATE`, hashConnectCode(code), now).Scan(&codeID, &orgID, &name, &kinds)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connected{}, ErrCodeInvalid
	}
	if err != nil {
		return Connected{}, err
	}
	var title *string
	if t := strings.TrimSpace(chatTitle); t != "" {
		t = truncate(t, 200)
		title = &t
	}
	c, err := scanChat(db.QueryRow(ctx, `
		INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds, chat_title, connected_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT ((COALESCE(org_id::text, '')), chat_id) DO UPDATE
		   SET name = EXCLUDED.name, kinds = EXCLUDED.kinds, chat_title = EXCLUDED.chat_title,
		       connected_at = EXCLUDED.connected_at, allowed = true, deleted_at = NULL,
		       bot_removed_at = NULL, last_error = NULL, updated_at = now()
		RETURNING `+chatColumns, orgID, name, chatID, kinds, title, now))
	if err != nil {
		return Connected{}, err
	}
	if _, err := db.Exec(ctx, `
		UPDATE sales_chat_connect_codes SET used_at = $2, chat_id = $3, subscription_id = $4 WHERE id = $1`,
		codeID, now, chatID, c.ID); err != nil {
		return Connected{}, err
	}
	out := Connected{Chat: c, Locale: "en"}
	if orgID != nil {
		var loc string
		if err := db.QueryRow(ctx, `SELECT name, COALESCE(default_locale, '') FROM organizations WHERE id = $1`, *orgID).Scan(&out.OrgName, &loc); err != nil {
			return Connected{}, err
		}
		if loc != "" {
			out.Locale = shopLocale(loc)
		}
	}
	return out, nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// MarkBotRemoved records that the bot was removed from chatID: every live
// subscription of that chat stops hearing anything until a new /connect. It
// answers how many rows it marked.
func MarkBotRemoved(ctx context.Context, db gen.DBTX, chatID string, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `
		UPDATE sales_notification_subscriptions SET bot_removed_at = $2, updated_at = now()
		 WHERE chat_id = $1 AND deleted_at IS NULL AND bot_removed_at IS NULL`, strings.TrimSpace(chatID), now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// MigrateChat follows a group upgraded to a supergroup on every row of the
// old chat id (the bot hears migrate_to_chat_id as a service message).
func MigrateChat(ctx context.Context, db gen.DBTX, from, to string) error {
	_, err := db.Exec(ctx, `
		UPDATE sales_notification_subscriptions SET chat_id = $2, updated_at = now()
		 WHERE chat_id = $1 AND NOT EXISTS (
		       SELECT 1 FROM sales_notification_subscriptions x
		        WHERE x.chat_id = $2 AND x.org_id IS NOT DISTINCT FROM sales_notification_subscriptions.org_id)`,
		strings.TrimSpace(from), strings.TrimSpace(to))
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE sales_notification_cards SET chat_id = $2, updated_at = now() WHERE chat_id = $1`,
		strings.TrimSpace(from), strings.TrimSpace(to))
	return err
}

// DeliveryChat checks that chatID is a delivery group that may press the
// buttons of orgID's orders: switched on, not deleted, the bot still in it,
// hearing shop_delivery, and the organization's own chat or the operator's.
func DeliveryChat(ctx context.Context, db gen.DBTX, chatID string, orgID uuid.UUID) (Chat, error) {
	c, err := scanChat(db.QueryRow(ctx, `SELECT `+chatColumns+`
		  FROM sales_notification_subscriptions
		 WHERE chat_id = $1 AND allowed AND deleted_at IS NULL AND bot_removed_at IS NULL
		   AND 'shop_delivery' = ANY (kinds) AND (org_id = $2 OR org_id IS NULL)
		 ORDER BY (org_id IS NULL), created_at LIMIT 1`, strings.TrimSpace(chatID), orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chat{}, ErrDeliveryGroup
	}
	return c, err
}

// KindsText names the kinds in the language, for the "chat connected" reply.
func KindsText(locale string, kinds []string) string {
	k := append([]string(nil), kinds...)
	sort.SliceStable(k, func(i, j int) bool { return kindRank(k[i]) < kindRank(k[j]) })
	parts := make([]string, 0, len(k))
	for _, kind := range k {
		parts = append(parts, say(locale, "salesnotify.bot.kind."+kind, nil))
	}
	return strings.Join(parts, ", ")
}

func kindRank(k string) int {
	switch k {
	case KindTickets:
		return 0
	case KindShopSale:
		return 1
	}
	return 2
}
