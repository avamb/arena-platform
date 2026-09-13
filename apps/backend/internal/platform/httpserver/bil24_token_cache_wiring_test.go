package httpserver

import "testing"

// TestBil24TokenCache_ServerOwnsOneCache guards the gateway token cache
// wiring: bil24Handler builds a fresh hbil24.Handler per request, so the cache
// must live on the Server, created once by New, or it never hits.
func TestBil24TokenCache_ServerOwnsOneCache(t *testing.T) {
	srv := buildProdServerWithGateway(t)
	if srv.bil24TokenCache == nil {
		t.Fatal("New must build the Server-owned gateway token cache")
	}
	first := srv.bil24TokenCache
	_ = srv.bil24Handler()
	_ = srv.bil24Handler()
	if srv.bil24TokenCache != first {
		t.Fatal("building per-request handlers must not replace the Server-owned token cache")
	}
}
