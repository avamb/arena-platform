package httpserver

// mount_scanner_app.go — the door-control app on the arena API (spec 69,
// MA-10). A scanner device token (`Authorization: Bearer sdt_…`) works on the
// /v1/scanner-app/ routes and NOWHERE else:
//
//   - refuseDeviceTokenOutsideScannerApp runs on the ROOT router: a device
//     token on any other path is answered by handleNotFound (404
//     http.not_found) before routing, without a database read, whatever the
//     token is worth.
//   - s.authMiddleware() (users and API keys) refuses a device token the
//     same way, and both JWT verifiers refuse actor_type "device", so a
//     device actor is built only by s.deviceAuthMiddleware below.
//   - s.deviceAuthMiddleware accepts ONLY a device token, authenticates it
//     against scanner_devices and puts a device actor (fixed permission list,
//     no roles, never the superadmin marker) plus the scannerapp.Device in
//     the context.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hscannerapp"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// scannerAppPathPrefix is the only path prefix on which a device token is
// accepted.
const scannerAppPathPrefix = "/v1/scanner-app/"

// bearerValue returns the value of an `Authorization: Bearer …` header
// (scheme case-insensitive, value trimmed); ok=false for any other header.
func bearerValue(r *http.Request) (string, bool) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
		return "", false
	}
	tok := strings.TrimSpace(parts[1])
	return tok, tok != ""
}

// deviceTokenFromHeader returns the bearer value when it carries the device
// token prefix. It checks nothing else and reads no database.
func deviceTokenFromHeader(r *http.Request) (string, bool) {
	tok, ok := bearerValue(r)
	if !ok || !strings.HasPrefix(tok, scannerapp.TokenWirePrefix) {
		return "", false
	}
	return tok, true
}

// isScannerAppPath reports whether the request is under /v1/scanner-app/,
// in its decoded and its escaped form (the router matches the escaped one
// when the path carries escapes).
func isScannerAppPath(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, scannerAppPathPrefix) &&
		strings.HasPrefix(r.URL.EscapedPath(), scannerAppPathPrefix)
}

// refuseDeviceTokenOutsideScannerApp is a ROOT middleware: a request with a
// device bearer token outside /v1/scanner-app/ is answered 404 http.not_found
// and never reaches routing.
func refuseDeviceTokenOutsideScannerApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, isDevice := deviceTokenFromHeader(r); isDevice && !isScannerAppPath(r) {
			handleNotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountScannerAppRoutes mounts the scanner-app routes. Always mounted (the
// OpenAPI drift test sees them); without a database the device middleware
// answers 503.
func (s *Server) mountScannerAppRoutes(r chi.Router) {
	// A nil *pgxpool.Pool must not become a non-nil interface.
	var db gen.DBTX
	if s.pgxPool != nil {
		db = s.pgxPool
	}
	h := hscannerapp.New(db, s.logger)
	var txdb scannerapp.TxDB
	if s.pgxPool != nil {
		txdb = s.pgxPool
	}
	trusted := 0
	if s.cfg != nil {
		trusted = s.cfg.TrustedProxyCount
	}
	h.Configure(hscannerapp.Deps{
		Pool: txdb, Audit: s.audit, Perms: s.perms, APIBaseURL: s.apiPublicURL(),
		TrustedProxies: trusted, Now: s.scannerNow,
	})

	// MA-11: no authorization at all. The pairing code and the refresh token
	// are the credentials, in the body; the attempt limit of pairing is
	// counted in the database.
	r.Post("/scanner-app/pair", h.HandlePair)
	r.Post("/scanner-app/token/refresh", h.HandleRefresh)

	r.Group(func(pr chi.Router) {
		pr.Use(s.deviceAuthMiddleware)
		pr.Use(permissions.RequirePermission(s.perms, "scan.read", "scanner_app"))
		pr.Get("/scanner-app/me", h.HandleMe)
		pr.Delete("/scanner-app/device", h.HandleUnpair)
	})

	// MA-11 / MA-12: the organizer's routes under a person's JWT.
	if !s.authEnabled() {
		return
	}
	const base = "/organizations/{org_id}/scanner"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "scan.perform", "scanner_devices")
		pr.Post(base+"/pairing-codes", s.orgGated(h.HandleCreatePairingCode))
		pr.Get(base+"/devices", s.orgGated(h.HandleListDevices))
		pr.Delete(base+"/devices/{device_id}", s.orgGated(h.HandleRevokeDevice))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "scanner_pass.manage", "scanner_passes")
		pr.Post(base+"/passes", s.orgGated(h.HandleCreatePass))
		pr.Get(base+"/passes", s.orgGated(h.HandleListPasses))
		pr.Get(base+"/passes/{pass_id}", s.orgGated(h.HandleGetPass))
		pr.Patch(base+"/passes/{pass_id}", s.orgGated(h.HandlePatchPass))
		pr.Delete(base+"/passes/{pass_id}", s.orgGated(h.HandleRevokePass))
		pr.Post(base+"/passes/{pass_id}/link", s.orgGated(h.HandleLinkPass))
	})
}

// scannerNow is the clock of the scanner routes.
func (s *Server) scannerNow() time.Time {
	if s.clk != nil {
		return s.clk.Now()
	}
	return time.Now()
}

// deviceAuthMiddleware authenticates a scanner device token. Anything that is
// not a device token (no header, a JWT, an API key) is 401; every refusal of
// scannerapp.Authenticate is the same 401 auth.invalid_token, the reason is
// logged at WARN and the token never is.
func (s *Server) deviceAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, present := bearerValue(r); !present {
			writeDeviceUnauthorized(w, r, "auth.missing_token", "Sign in first. Send a valid device token with the request.")
			return
		}
		raw, isDevice := deviceTokenFromHeader(r)
		if !isDevice {
			s.logger.Warn("scannerapp: authentication rejected", "reason", "not a device token")
			writeDeviceUnauthorized(w, r, "auth.invalid_token", "the presented device token is not valid")
			return
		}
		if s.pgxPool == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is unavailable", r))
			return
		}
		now := time.Now()
		if s.clk != nil {
			now = s.clk.Now()
		}
		device, err := scannerapp.Authenticate(r.Context(), s.pgxPool, raw, now)
		if err != nil {
			if isDeviceRejection(err) {
				s.logger.Warn("scannerapp: authentication rejected", "reason", err.Error())
				writeDeviceUnauthorized(w, r, "auth.invalid_token", "the presented device token is not valid")
				return
			}
			s.logger.Error("scannerapp: authentication failed", "error", err.Error())
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"auth.device_check_failed", "failed to verify the device token", r))
			return
		}
		if _, err := scannerapp.TouchLastSeen(r.Context(), s.pgxPool, device.ID, now); err != nil {
			// Bookkeeping, not authorization: log and go on.
			s.logger.Warn("scannerapp: last_seen_at update failed", "error", err.Error())
		}
		if !device.PairConfirmed {
			// The first use of the access token of a refreshed pair confirms
			// it: the previous refresh token stops working (spec 69 A.3).
			if _, err := scannerapp.ConfirmPair(r.Context(), s.pgxPool, device.ID, raw, now); err != nil {
				s.logger.Warn("scannerapp: confirm token pair failed", "device_id", device.ID.String(), "error", err.Error())
			} else {
				device.PairConfirmed = true
			}
		}
		ctx := auth.WithActor(r.Context(), auth.NewDeviceActor(device.ID.String(), device.OrgID.String()))
		ctx = scannerapp.WithDevice(ctx, device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isDeviceRejection reports whether err is a credential refusal (401), not an
// infrastructure failure (500).
func isDeviceRejection(err error) bool {
	return errors.Is(err, scannerapp.ErrMalformed) ||
		errors.Is(err, scannerapp.ErrUnknown) ||
		errors.Is(err, scannerapp.ErrRevoked) ||
		errors.Is(err, scannerapp.ErrExpired) ||
		errors.Is(err, scannerapp.ErrAccessEnded)
}

func writeDeviceUnauthorized(w http.ResponseWriter, r *http.Request, code, message string) {
	w.Header().Set(auth.HeaderWWWAuthenticate, auth.WWWAuthenticateBearer)
	httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(code, message, r))
}
