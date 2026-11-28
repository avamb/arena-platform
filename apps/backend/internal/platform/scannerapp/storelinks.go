package scannerapp

// Store pages of the ticket-scanner app the door staff use. Both stores
// redirect by the visitor's region, so the links carry no country or language
// segment. The bot and the pairing page share these constants.
const (
	AndroidPackage = "com.arenasoldout.macs"
	AndroidURL     = "https://play.google.com/store/apps/details?id=" + AndroidPackage
	IOSURL         = "https://apps.apple.com/app/arenasoldout/id6770095694"
)
