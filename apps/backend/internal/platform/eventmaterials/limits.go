// Package eventmaterials names the limits and allowlists of an event's page
// materials (HP-1, migration 0153): gallery photos, video links, "where else
// to buy" partner links, the map point and the photo report.
//
// The database enforces the same numbers (position CHECK plus
// UNIQUE (event_id, position)), so a race of two inserts cannot exceed them;
// the constants here are for handlers, which answer a readable 409/422 before
// reaching the constraint. limits_test.go reads the migration and fails when
// the two drift.
package eventmaterials

const (
	// MaxGalleryPhotos is the number of gallery photos one event may carry.
	MaxGalleryPhotos = 6
	// MaxVideos is the number of video links one event may carry.
	MaxVideos = 4
	// MaxPartnerLinks is the number of "where else to buy" links per event.
	MaxPartnerLinks = 3
	// MaxReportPhotos is the number of photo-report photos per event.
	MaxReportPhotos = 30

	// MaxCaptionRunes bounds a photo caption.
	MaxCaptionRunes = 200
	// MaxURLLength bounds a stored video or partner URL.
	MaxURLLength = 500
	// MaxSellerNameRunes bounds the seller name shown beside a partner link.
	MaxSellerNameRunes = 120
	// MaxMapLabelRunes bounds the label of the map point.
	MaxMapLabelRunes = 200
	// MaxVideoIDLength bounds a parsed provider video id.
	MaxVideoIDLength = 64
)

// Media owner types (media_objects.owner_type) of the photos.
const (
	OwnerTypeGallery = "event_gallery"
	OwnerTypeReport  = "event_report"
)

// Video providers stored in event_videos.provider. A provider other than
// ProviderHTTPS carries a parsed video id and is embedded by the page;
// ProviderHTTPS is any other https link, shown as a plain link without an id.
const (
	ProviderYouTube = "youtube"
	ProviderVimeo   = "vimeo"
	ProviderRutube  = "rutube"
	ProviderHTTPS   = "https"
)

// VideoProviders lists every allowed provider value.
var VideoProviders = []string{ProviderYouTube, ProviderVimeo, ProviderRutube, ProviderHTTPS}

// Kinds of event_page_stats.kind (HP-2a). A new kind needs a migration.
const (
	StatPageView     = "page_view"
	StatVideoPlay    = "video_play"
	StatPartnerClick = "partner_click"
)

// StatKinds lists every allowed counter kind.
var StatKinds = []string{StatPageView, StatVideoPlay, StatPartnerClick}
