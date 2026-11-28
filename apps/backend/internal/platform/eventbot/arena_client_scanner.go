package eventbot

// arena_client_scanner.go — the calls behind the bot's controller-pass and
// "Control app" screens (spec 69, MA-12). The rules live in arena-api
// (hscannerapp): the bot only shows them. The pass answers carry the closing
// fields (state, closed_at, upload_until) once the server has them; the bot
// reads them when they are there and never needs them.

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// ScannerPass is a controller pass as the bot reads it.
type ScannerPass struct {
	openapi.ScannerPass
	State       *string    `json:"state,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	UploadUntil *time.Time `json:"upload_until,omitempty"`
}

// ScannerPassCreated is the answer of a new pass: the pass and its link.
type ScannerPassCreated struct {
	Pass    ScannerPass                `json:"pass"`
	Pairing openapi.ScannerPairingLink `json:"pairing"`
}

// ScannerPassCreate is the body of a new pass. The window is never sent: the
// server works out the default from the dates.
type ScannerPassCreate struct {
	Label      string
	SessionIDs []uuid.UUID
	MaxDevices int
}

func scannerBase(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/scanner"
}

func scannerPassPath(orgID, passID uuid.UUID) string {
	return scannerBase(orgID) + "/passes/" + passID.String()
}

// ListScannerPasses lists the passes of the organization, newest first; a
// non-nil eventID keeps the passes that have a date of that event.
func (c *ArenaClient) ListScannerPasses(ctx context.Context, jwt string, orgID uuid.UUID, eventID *uuid.UUID) ([]ScannerPass, error) {
	path := scannerBase(orgID) + "/passes"
	if eventID != nil {
		path += "?" + url.Values{"event_id": {eventID.String()}}.Encode()
	}
	var out struct {
		Passes []ScannerPass `json:"passes"`
	}
	err := c.do(ctx, http.MethodGet, path, jwt, nil, &out)
	return out.Passes, err
}

// GetScannerPass reads one pass.
func (c *ArenaClient) GetScannerPass(ctx context.Context, jwt string, orgID, passID uuid.UUID) (ScannerPass, error) {
	var out ScannerPass
	err := c.do(ctx, http.MethodGet, scannerPassPath(orgID, passID), jwt, nil, &out)
	return out, err
}

// CreateScannerPass makes a pass and returns it with its pairing link.
func (c *ArenaClient) CreateScannerPass(ctx context.Context, jwt string, orgID uuid.UUID, in ScannerPassCreate) (ScannerPassCreated, error) {
	sessions := make([]map[string]any, 0, len(in.SessionIDs))
	for _, s := range in.SessionIDs {
		sessions = append(sessions, map[string]any{"session_id": s})
	}
	body := map[string]any{"label": in.Label, "sessions": sessions, "max_devices": in.MaxDevices}
	var out ScannerPassCreated
	err := c.do(ctx, http.MethodPost, scannerBase(orgID)+"/passes", jwt, body, &out)
	return out, err
}

// ExtendScannerPass moves the end of the window of a pass.
func (c *ArenaClient) ExtendScannerPass(ctx context.Context, jwt string, orgID, passID uuid.UUID, until time.Time) (ScannerPass, error) {
	var out ScannerPass
	err := c.do(ctx, http.MethodPatch, scannerPassPath(orgID, passID), jwt, map[string]any{"valid_until": until.UTC()}, &out)
	return out, err
}

// RevokeScannerPass turns a pass off.
func (c *ArenaClient) RevokeScannerPass(ctx context.Context, jwt string, orgID, passID uuid.UUID) error {
	return c.do(ctx, http.MethodDelete, scannerPassPath(orgID, passID), jwt, nil, nil)
}

// LinkScannerPass makes a new link of a pass (the old one stops, and a turned-off
// pass is turned on again).
func (c *ArenaClient) LinkScannerPass(ctx context.Context, jwt string, orgID, passID uuid.UUID) (openapi.ScannerPairingLink, error) {
	var out openapi.ScannerPairingLink
	err := c.do(ctx, http.MethodPost, scannerPassPath(orgID, passID)+"/link", jwt, nil, &out)
	return out, err
}

// CreateScannerPairingCode makes the link and the code of the person's own phone.
func (c *ArenaClient) CreateScannerPairingCode(ctx context.Context, jwt string, orgID uuid.UUID) (openapi.ScannerPairingLink, error) {
	var out openapi.ScannerPairingLink
	err := c.do(ctx, http.MethodPost, scannerBase(orgID)+"/pairing-codes", jwt, nil, &out)
	return out, err
}

// ListScannerDevices lists the live devices the person may see.
func (c *ArenaClient) ListScannerDevices(ctx context.Context, jwt string, orgID uuid.UUID) ([]openapi.ScannerDevice, error) {
	var out openapi.ScannerDeviceList
	err := c.do(ctx, http.MethodGet, scannerBase(orgID)+"/devices", jwt, nil, &out)
	return out.Devices, err
}

// RevokeScannerDevice removes one device.
func (c *ArenaClient) RevokeScannerDevice(ctx context.Context, jwt string, orgID, deviceID uuid.UUID) error {
	return c.do(ctx, http.MethodDelete, scannerBase(orgID)+"/devices/"+deviceID.String(), jwt, nil, nil)
}
