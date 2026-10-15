package eventmaterials

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// ErrInvalidVideoURL is returned for a link that cannot be stored: not an
// https URL, empty, longer than MaxURLLength, carrying blanks or credentials.
var ErrInvalidVideoURL = errors.New("eventmaterials: video link is not a valid https URL")

// VideoRef is a parsed video link: the provider, the id the page builds an
// embed from (empty for ProviderHTTPS) and the cleaned URL that is stored.
type VideoRef struct {
	Provider string
	VideoID  string
	URL      string
}

var (
	youtubeIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoIDRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
	rutubeIDRe  = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
)

// ParseVideoURL validates a pasted video link and recognizes YouTube, Vimeo
// and Rutube. Any other https URL is kept as a plain link (ProviderHTTPS, no
// id), and so is a link of a known site that names no video (a channel page,
// a malformed id): the organizer still gets a working link. Only the link is
// kept, never the video bytes.
func ParseVideoURL(raw string) (VideoRef, error) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > MaxURLLength {
		return VideoRef{}, ErrInvalidVideoURL
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return VideoRef{}, ErrInvalidVideoURL
		}
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return VideoRef{}, ErrInvalidVideoURL
	}
	// The scheme is case-insensitive in a URL but the stored value must satisfy
	// the case-sensitive CHECK url ~ '^https://' of migration 0153: store it
	// in lower case (a pasted "HTTPS://..." used to pass here and then fail
	// the insert with 23514, a 500).
	s = "https" + s[len("https"):]
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(strings.TrimPrefix(host, "www."), "m.")
	segs := splitPath(u.Path)

	provider, id := "", ""
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com":
		id = youtubeIDFromPath(u, segs)
		provider = ProviderYouTube
	case "youtu.be":
		if len(segs) >= 1 {
			id = segs[0]
		}
		provider = ProviderYouTube
	case "vimeo.com":
		id = vimeoIDFromPath(segs)
		provider = ProviderVimeo
	case "player.vimeo.com":
		if len(segs) >= 2 && segs[0] == "video" {
			id = segs[1]
		}
		provider = ProviderVimeo
	case "rutube.ru":
		id = rutubeIDFromPath(segs)
		provider = ProviderRutube
	}

	if provider != "" && validVideoID(provider, id) {
		if provider == ProviderRutube {
			id = strings.ToLower(id)
		}
		return VideoRef{Provider: provider, VideoID: id, URL: s}, nil
	}
	return VideoRef{Provider: ProviderHTTPS, URL: s}, nil
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

func youtubeIDFromPath(u *url.URL, segs []string) string {
	if len(segs) == 0 {
		return ""
	}
	switch segs[0] {
	case "watch":
		return u.Query().Get("v")
	case "shorts", "embed", "live", "v":
		if len(segs) >= 2 {
			return segs[1]
		}
	}
	return ""
}

func vimeoIDFromPath(segs []string) string {
	// vimeo.com/<id>, vimeo.com/<id>/<unlisted hash>, vimeo.com/channels/<c>/<id>,
	// vimeo.com/groups/<g>/videos/<id>, vimeo.com/video/<id>
	if len(segs) == 0 {
		return ""
	}
	if vimeoIDRe.MatchString(segs[0]) {
		return segs[0]
	}
	switch segs[0] {
	case "video":
		if len(segs) >= 2 {
			return segs[1]
		}
	case "channels":
		if len(segs) >= 3 {
			return segs[2]
		}
	case "groups":
		if len(segs) >= 4 && segs[2] == "videos" {
			return segs[3]
		}
	}
	return ""
}

func rutubeIDFromPath(segs []string) string {
	// rutube.ru/video/<id>/ and rutube.ru/play/embed/<id>
	if len(segs) >= 2 && segs[0] == "video" {
		return segs[1]
	}
	if len(segs) >= 3 && segs[0] == "play" && segs[1] == "embed" {
		return segs[2]
	}
	return ""
}

func validVideoID(provider, id string) bool {
	if len(id) > MaxVideoIDLength {
		return false
	}
	switch provider {
	case ProviderYouTube:
		return youtubeIDRe.MatchString(id)
	case ProviderVimeo:
		return vimeoIDRe.MatchString(id)
	case ProviderRutube:
		return rutubeIDRe.MatchString(id)
	}
	return false
}

// EmbedURL is the address a page puts in an iframe for a stored video: the
// privacy-friendly YouTube domain, Vimeo's player and Rutube's embed. It is ""
// for ProviderHTTPS (a plain link has no embed) and for an id that no longer
// matches its provider's format, so a row the page cannot embed safely is
// shown as its plain link.
func EmbedURL(provider, videoID string) string {
	if !validVideoID(provider, videoID) {
		return ""
	}
	switch provider {
	case ProviderYouTube:
		return "https://www.youtube-nocookie.com/embed/" + videoID
	case ProviderVimeo:
		return "https://player.vimeo.com/video/" + videoID
	case ProviderRutube:
		return "https://rutube.ru/play/embed/" + videoID
	}
	return ""
}
