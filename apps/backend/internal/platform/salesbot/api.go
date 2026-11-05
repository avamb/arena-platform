package salesbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

// StepRequest is the body of POST /v1/bot/shop-delivery/steps.
type StepRequest struct {
	ChatID         string  `json:"chat_id"`
	TelegramUserID int64   `json:"telegram_user_id"`
	TelegramName   string  `json:"telegram_name"`
	OrderID        string  `json:"order_id"`
	Step           string  `json:"step"`
	Tracking       *string `json:"tracking,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

// Answer is what the step and card routes answer.
type Answer struct {
	Outcome     string                   `json:"outcome"`
	Step        *string                  `json:"fulfillment_step"`
	OrderNumber int64                    `json:"order_number"`
	Locale      string                   `json:"locale"`
	Card        salesnotify.DeliveryCard `json:"card"`
}

// Outcomes of a press (hsaleschats).
const (
	OutcomeApplied   = "applied"
	OutcomeUnchanged = "unchanged"
	OutcomeStale     = "stale"
	OutcomeClosed    = "closed"
)

// ConnectRequest is the body of POST /v1/bot/sales-chats/connect.
type ConnectRequest struct {
	Code           string `json:"code"`
	ChatID         string `json:"chat_id"`
	ChatTitle      string `json:"chat_title"`
	TelegramUserID int64  `json:"telegram_user_id"`
	TelegramName   string `json:"telegram_name"`
}

// ConnectAnswer is what the connect route answers.
type ConnectAnswer struct {
	Chat struct {
		Kinds []string `json:"kinds"`
	} `json:"chat"`
	OrgName  string `json:"org_name"`
	Locale   string `json:"locale"`
	Operator bool   `json:"operator"`
}

// API is what the bot asks arena-api. Status is the HTTP status of the answer
// (the answer is only decoded on 200).
type API interface {
	Step(ctx context.Context, req StepRequest) (Answer, int, error)
	Card(ctx context.Context, chatID, orderID string) (Answer, int, error)
	Connect(ctx context.Context, req ConnectRequest) (ConnectAnswer, int, error)
	Removed(ctx context.Context, chatID string) error
	Migrated(ctx context.Context, from, to string) error
}

// HTTPAPI calls arena-api with the bot's service token (BOT_SERVICE_TOKEN).
type HTTPAPI struct {
	base   string
	token  string
	client *http.Client
}

// NewHTTPAPI builds the client; client nil uses a 15-second timeout.
func NewHTTPAPI(baseURL, serviceToken string, client *http.Client) *HTTPAPI {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &HTTPAPI{base: strings.TrimRight(baseURL, "/"), token: serviceToken, client: client}
}

func (a *HTTPAPI) post(ctx context.Context, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("salesbot: %s: request failed", path)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("salesbot: %s: decode: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// Step implements API.
func (a *HTTPAPI) Step(ctx context.Context, req StepRequest) (Answer, int, error) {
	var out Answer
	st, err := a.post(ctx, "/v1/bot/shop-delivery/steps", req, &out)
	return out, st, err
}

// Card implements API.
func (a *HTTPAPI) Card(ctx context.Context, chatID, orderID string) (Answer, int, error) {
	var out Answer
	st, err := a.post(ctx, "/v1/bot/shop-delivery/card", map[string]string{"chat_id": chatID, "order_id": orderID}, &out)
	return out, st, err
}

// Connect implements API.
func (a *HTTPAPI) Connect(ctx context.Context, req ConnectRequest) (ConnectAnswer, int, error) {
	var out ConnectAnswer
	st, err := a.post(ctx, "/v1/bot/sales-chats/connect", req, &out)
	return out, st, err
}

// Removed implements API.
func (a *HTTPAPI) Removed(ctx context.Context, chatID string) error {
	st, err := a.post(ctx, "/v1/bot/sales-chats/removed", map[string]string{"chat_id": chatID}, nil)
	if err == nil && st != http.StatusOK {
		err = fmt.Errorf("salesbot: removed: status %d", st)
	}
	return err
}

// Migrated implements API.
func (a *HTTPAPI) Migrated(ctx context.Context, from, to string) error {
	st, err := a.post(ctx, "/v1/bot/sales-chats/migrated", map[string]string{"from_chat_id": from, "to_chat_id": to}, nil)
	if err == nil && st != http.StatusOK {
		err = fmt.Errorf("salesbot: migrated: status %d", st)
	}
	return err
}
