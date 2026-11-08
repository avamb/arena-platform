package salesnotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
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
	OK          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
	Parameters  struct {
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
		RetryAfter      int   `json:"retry_after"`
	} `json:"parameters"`
}

const maxAttempts = 3

// Send delivers text (Telegram HTML) to chatID.
func (t *TelegramSender) Send(ctx context.Context, chatID, text string) error {
	_, err := t.post(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	return err
}

// SendDocument uploads a file to chatID through sendDocument. caption is
// plain text (cut to Telegram's 1024 characters by the caller). Like the
// other methods it never puts the token or the file into an error.
func (t *TelegramSender) SendDocument(ctx context.Context, chatID, filename, contentType string, data []byte, caption string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", chatID)
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="document"; filename="%s"`, safeFilename(filename)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("telegram: build upload failed")
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("telegram: build upload failed")
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("telegram: build upload failed")
	}
	_, err = t.postBody(ctx, "sendDocument", buf.Bytes(), mw.FormDataContentType())
	return err
}

// inlineKeyboard is the reply_markup of rows of buttons. Empty rows still
// send an empty keyboard, which removes the buttons of an edited message.
func inlineKeyboard(rows [][]Button) map[string]any {
	kb := make([][]map[string]string, 0, len(rows))
	for _, row := range rows {
		r := make([]map[string]string, 0, len(row))
		for _, b := range row {
			r = append(r, map[string]string{"text": b.Text, "callback_data": b.Data})
		}
		kb = append(kb, r)
	}
	return map[string]any{"inline_keyboard": kb}
}

// SendCard delivers text with inline buttons and answers the message id, so
// the card can be edited later (CardSender).
func (t *TelegramSender) SendCard(ctx context.Context, chatID, text string, rows [][]Button) (int64, error) {
	res, err := t.post(ctx, "sendMessage", map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             inlineKeyboard(rows),
	})
	if err != nil {
		return 0, err
	}
	var m struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(res, &m); err != nil || m.MessageID == 0 {
		return 0, fmt.Errorf("telegram: sendMessage answered no message id")
	}
	return m.MessageID, nil
}

// EditCard replaces the text and the buttons of a card (CardSender). Empty
// rows remove the buttons.
func (t *TelegramSender) EditCard(ctx context.Context, chatID string, messageID int64, text string, rows [][]Button) error {
	_, err := t.post(ctx, "editMessageText", map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             inlineKeyboard(rows),
	})
	return err
}

// post calls one Bot API method with retries and answers its result.
func (t *TelegramSender) post(ctx context.Context, method string, payload map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("telegram: marshal: %w", err)
	}
	return t.postBody(ctx, method, body, "application/json")
}

// postBody is post with a ready body and its content type (sendDocument is
// multipart/form-data). The retry and error rules are the same.
func (t *TelegramSender) postBody(ctx context.Context, method string, body []byte, contentType string) (json.RawMessage, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, wait, err := t.attempt(ctx, method, body, contentType)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if wait < 0 {
			return nil, err // not worth retrying
		}
		if attempt < maxAttempts {
			t.sleep(ctx, wait)
		}
	}
	return nil, lastErr
}

// attempt returns (result, retry delay, error); a negative delay means final.
func (t *TelegramSender) attempt(ctx context.Context, method string, body []byte, contentType string) (json.RawMessage, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, -1, fmt.Errorf("telegram: build request failed")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := t.client.Do(req)
	if err != nil {
		// *url.Error embeds the URL, and with it the token: never return it.
		return nil, time.Second, fmt.Errorf("telegram: request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	var r tgResponse
	_ = json.NewDecoder(resp.Body).Decode(&r)
	if resp.StatusCode == http.StatusOK && r.OK {
		return r.Result, 0, nil
	}
	if r.Parameters.MigrateToChatID != 0 {
		return nil, -1, &MigratedError{NewChatID: strconv.FormatInt(r.Parameters.MigrateToChatID, 10)}
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		d := time.Duration(r.Parameters.RetryAfter) * time.Second
		if d <= 0 || d > 10*time.Second {
			d = 10 * time.Second
		}
		return nil, d, fmt.Errorf("telegram 429: %s", r.Description)
	case http.StatusBadRequest, http.StatusForbidden:
		return nil, -1, &PermanentError{Code: resp.StatusCode, Description: r.Description}
	case http.StatusUnauthorized, http.StatusNotFound:
		// A wrong or revoked token: every chat will fail the same way.
		return nil, -1, fmt.Errorf("telegram %d: bot token rejected", resp.StatusCode)
	default:
		return nil, time.Second, fmt.Errorf("telegram %d: %s", resp.StatusCode, r.Description)
	}
}

// safeFilename drops what could break out of the quoted filename.
func safeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == 92 || r < 0x20 {
			return -1
		}
		return r
	}, name)
}
