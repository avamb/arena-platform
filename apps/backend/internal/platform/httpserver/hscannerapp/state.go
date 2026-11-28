package hscannerapp

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// RequireActiveDevice is the gate of the scanner-app routes that a device of
// a closed pass may not use (spec 69 B.1): a device in
// scannerapp.StateUploadOnly is answered 403 scanner_app.pass_closed. It runs
// after the device-token middleware; no device in the context is 401.
//
// GET /me, DELETE /device and the token refresh stay open to an "upload
// only" device and must NOT use this gate; the scan routes of MA-13 / MA-14
// mount it on everything except the upload of the offline queue.
func RequireActiveDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, ok := scannerapp.DeviceFromContext(r.Context())
		if !ok {
			writeUnauthorized(w, r)
			return
		}
		if d.State == scannerapp.StateUploadOnly {
			writeErr(w, r, http.StatusForbidden, "scanner_app.pass_closed",
				"the pass is closed; the device may only upload the scans it kept")
			return
		}
		next.ServeHTTP(w, r)
	})
}
