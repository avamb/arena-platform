package salesnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// MigratedError: the group became a supergroup and now lives at NewChatID.
type MigratedError struct{ NewChatID string }

func (e *MigratedError) Error() string {
	return "telegram: chat migrated to " + e.NewChatID
}

// PermanentError: Telegram refused the chat itself (bot not a member, chat
// not found, bot blocked) — retrying the same chat is pointless.
type PermanentError struct {
	Code        int
	Description string
}

func (e *PermanentError) Error() string {
	return fmt.Sprintf("telegram %d: %s", e.Code, e.Description)
}

// TelegramSender posts messages through the Bot API. The token only ever
// appears in the request URL and is never logged or put into an error.
type TelegramSender struct {
	token   string
	baseURL string
	client  *http.Client
	sleep   func(context.Context, time.Duration)
}

// NewTelegramSender builds a sender; baseURL "" uses api.telegram.org.
func NewTelegramSender(token, baseURL string) *TelegramSender {
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return &TelegramSender{
		token:   token,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
		sleep: func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
	}
}

type tgResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
		RetryAfter      int   `json:"retry_after"`
	} `json:"parameters"`
}

const maxAttempts = 3

// Send delivers text (Telegram HTML) to chatID.
func (t *TelegramSender) Send(ctx context.Context, chatID, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return fmt.Errorf("telegram: marshal: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		wait, err := t.attempt(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if wait < 0 {
			return err // not worth retrying
		}
		if attempt < maxAttempts {
			t.sleep(ctx, wait)
		}
	}
	return lastErr
}

// attempt returns (retry delay, error); a negative delay means final.
func (t *TelegramSender) attempt(ctx context.Context, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return -1, fmt.Errorf("telegram: build request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// *url.Error embeds the URL, and with it the token: never return it.
		return time.Second, fmt.Errorf("telegram: request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	var r tgResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode == http.StatusOK && r.OK {
		return 0, nil
	}
	if r.Parameters.MigrateToChatID != 0 {
		return -1, &MigratedError{NewChatID: strconv.FormatInt(r.Parameters.MigrateToChatID, 10)}
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		d := time.Duration(r.Parameters.RetryAfter) * time.Second
		if d <= 0 || d > 10*time.Second {
			d = 10 * time.Second
		}
		return d, fmt.Errorf("telegram 429: %s", r.Description)
	case http.StatusBadRequest, http.StatusForbidden:
		return -1, &PermanentError{Code: resp.StatusCode, Description: r.Description}
	case http.StatusUnauthorized, http.StatusNotFound:
		// A wrong or revoked token: every chat will fail the same way.
		return -1, fmt.Errorf("telegram %d: bot token rejected", resp.StatusCode)
	default:
		return time.Second, fmt.Errorf("telegram %d: %s", resp.StatusCode, r.Description)
	}
}
