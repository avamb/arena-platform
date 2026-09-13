//go:build integration

// create_user_race_test.go — live-server coverage for the LOW-severity
// concurrency defect fixed 2026-09-14: two concurrent CREATE_USER calls
// for the same brand-new email used to race customer_identities_strong_uq
// (migration 0091, GLOBAL unique index) — the loser got SQLSTATE 23505,
// hbil24/cmd_user.go logged "customer resolve failed" at ERROR and
// answered resultCode -1 (transient), and the WordPress site simply
// retried. customers.Resolve is now race-safe at the source (see
// AGENTS.md and internal/platform/customers/resolve.go), so this test
// drives the real gateway wire protocol with real concurrent HTTP
// requests and asserts every CREATE_USER answers resultCode 0 with the
// SAME userId — no retry needed.
package compat_bil24_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// postBil24Concurrent is postBil24's goroutine-safe twin: t.Fatalf/t.Helper
// are unsafe to call from a goroutine other than the test's own, so this
// variant returns (response, error) instead and lets the caller assert
// once every goroutine has joined.
func postBil24Concurrent(base string, body map[string]any) (map[string]interface{}, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := http.Post(base+"/compat/bil24/json", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("POST /compat/bil24/json: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST /compat/bil24/json: status %d, body %s", resp.StatusCode, payload)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("parse response %s: %w", payload, err)
	}
	return out, nil
}

func TestBil24_CreateUser_ConcurrentSameNewEmail_AllSucceedSameUserID(t *testing.T) {
	st := setupHarness(t)
	base := startHarnessServer(t, st)

	// Unique per run — customer_identities_strong_uq is a GLOBAL index
	// (AGENTS.md), so a fixed literal would collide with a leftover row
	// from an interrupted prior run against the shared dev stand.
	email := "race-" + uuid.New().String()[:8] + "@harness.test"

	const n = 20
	var wg sync.WaitGroup
	responses := make([]map[string]interface{}, n)
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			responses[i], errs[i] = postBil24Concurrent(base, map[string]any{
				"command":   "CREATE_USER",
				"fid":       st.ChannelFID,
				"token":     st.ChannelToken,
				"locale":    "en-US",
				"email":     email,
				"firstName": "Race",
				"lastName":  "Harness",
			})
		}(i)
	}
	wg.Wait()

	userIDs := map[float64]int{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: CREATE_USER request failed: %v", i, errs[i])
		}
		resp := responses[i]
		code, _ := resp["resultCode"].(float64)
		if code != 0 {
			t.Fatalf("goroutine %d: resultCode = %v, want 0 (description %v)", i, resp["resultCode"], resp["description"])
		}
		sessionID, _ := resp["sessionId"].(string)
		if sessionID == "" {
			t.Fatalf("goroutine %d: CREATE_USER returned no sessionId: %v", i, resp)
		}
		userID, _ := resp["userId"].(float64)
		userIDs[userID]++
	}
	if len(userIDs) != 1 {
		t.Fatalf("distinct userIds across %d concurrent CREATE_USER calls for the same new email = %d, want 1: %v",
			n, len(userIDs), userIDs)
	}

	// Exactly one customer row backs this email — no orphan from a losing
	// race's InsertCustomer.
	var customerCount int
	if err := st.Pool.QueryRow(t.Context(),
		`SELECT count(DISTINCT customer_id) FROM customer_identities WHERE kind = 'email' AND value_normalized = $1`,
		email).Scan(&customerCount); err != nil {
		t.Fatalf("count customers for %s: %v", email, err)
	}
	if customerCount != 1 {
		t.Fatalf("distinct customers for email %s = %d, want 1", email, customerCount)
	}
}
