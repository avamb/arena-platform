package hauth

// telegram_next.go — where the Mini App goes after it signed in (task S3b-next).
//
// The bot opens the Mini App on a screen of the event center (for example the
// seat manager of one date). The page learns that address in one of two ways:
//
//   - `start_param`, a field of the signed launch data: Telegram only allows
//     A-Z a-z 0-9 _ - there, so the bot writes the path as base64url (no
//     padding). Telegram signs it with the rest of the data, so nobody can
//     change it after the bot chose it.
//   - `next` in the request body, a plain path the page read from its own
//     address. It is NOT trusted: it goes through the same check.
//
// Both go through SafeMiniappNext. The answer carries the path only when it is
// an event-center page of the shape /o/<organization uuid>/..., so the page can
// never be sent to another site (open redirect) or to a path that is not a
// screen of the event center. Whether the person may OPEN that organization is
// decided by the screen and the API, as for any other link: this check only
// keeps the address inside the event center.

import (
	"encoding/base64"
	"regexp"
	"strings"
)

const maxMiniappNextLen = 300

var miniappNextPattern = regexp.MustCompile(
	`^/o/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}` +
		`(/[A-Za-z0-9._~-]+)*/?` +
		`(\?[A-Za-z0-9._~=&-]*)?$`)

// SafeMiniappNext returns raw when it is an event-center path the Mini App may
// open, and "" otherwise (never an error: the sign-in itself is still good).
func SafeMiniappNext(raw string) string {
	if raw == "" || len(raw) > maxMiniappNextLen || !miniappNextPattern.MatchString(raw) {
		return ""
	}
	path, _, _ := strings.Cut(raw, "?")
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return ""
		}
	}
	return raw
}

// MiniappNextFromStartParam reads the path the bot put into `start_param`
// (base64url without padding); "" when it is empty, not base64url or not a
// safe path.
func MiniappNextFromStartParam(startParam string) string {
	if startParam == "" || len(startParam) > 512 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(startParam)
	if err != nil {
		return ""
	}
	return SafeMiniappNext(string(decoded))
}

// miniappNext picks the destination: the signed start_param wins over the
// body's `next`.
func miniappNext(startParam, bodyNext string) string {
	if n := MiniappNextFromStartParam(startParam); n != "" {
		return n
	}
	return SafeMiniappNext(bodyNext)
}
