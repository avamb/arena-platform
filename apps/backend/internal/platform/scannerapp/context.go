package scannerapp

import "context"

type ctxKey struct{}

// WithDevice returns a context carrying the authenticated device. Only the
// device-token middleware of the /v1/scanner-app routes calls it.
func WithDevice(ctx context.Context, d Device) context.Context {
	return context.WithValue(ctx, ctxKey{}, d)
}

// DeviceFromContext returns the device stored by WithDevice; false when the
// request was not authenticated by a device token.
func DeviceFromContext(ctx context.Context) (Device, bool) {
	if ctx == nil {
		return Device{}, false
	}
	d, ok := ctx.Value(ctxKey{}).(Device)
	return d, ok
}
