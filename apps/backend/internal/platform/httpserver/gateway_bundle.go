package httpserver

import "github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"

// gatewayBundle picks the i18n bundle the Bil24 gateway localizes its wire
// descriptions with (spec §6: answer in the request's locale). Server.bundle
// feeds the gateway alone; Options.Bundle additionally switches on the REST
// locale middleware, so production arena-api passes only
// Options.GatewayBundle. Until 2026-09-25 it passed neither and every
// gateway description was English whatever locale a site sent.
func gatewayBundle(opts Options) *i18n.Bundle {
	if opts.Bundle != nil {
		return opts.Bundle
	}
	return opts.GatewayBundle
}
