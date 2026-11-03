package eventpage

import "strings"

// ticketingDomains are the registrable names of ticketing platforms whose
// pages are never read (owner decision 03.11.2026, spec 59 §6): their terms
// forbid automated collection, and the organizer's event there is the
// platform's page, not the organizer's own. A host matches when its last
// labels equal the entry ("www.ticketmaster.es" matches "ticketmaster"
// listed with every public suffix below) — see IsTicketingHost.
//
// The list names the platform, not every country domain: an entry without a
// dot matches any suffix ("eventbrite" matches eventbrite.com, eventbrite.es,
// eventbrite.co.uk), an entry with a dot matches that exact registrable name.
var ticketingDomains = []string{
	// international
	"ticketmaster", "livenation", "eventbrite", "eventim", "seetickets",
	"ticketswap", "viagogo", "stubhub", "dice.fm", "feverup", "fever.co",
	"axs.com", "universe.com", "tickettailor", "ticketleap", "brownpapertickets",
	"eventzilla", "ticketsource", "skiddle", "songkick", "bandsintown",
	"residentadvisor", "ra.co", "tixr", "etix", "showclix", "humanitix",
	"billetto", "weezevent", "shotgun.live", "xceed",
	"fnac.es", "fnacspectacles", "ticketone", "vivaticket", "ticketcorner",
	"starticket", "oeticket", "reservix", "adticket",
	// Spain, Portugal
	"entradas.com", "ticketea", "atrapalo", "giglon", "wegow",
	"taquilla.com", "elcorteingles", "bacantix", "ticketline.pt", "bol.pt",
	// Central and Eastern Europe
	"goout.net", "ticketportal", "ticketstream", "smsticket",
	"kupbilecik", "ebilet", "biletyna", "concert.ua", "karabas",
	"kassir.ru", "ticketland", "afisha.yandex.ru", "afisha.ru", "radario",
	"ponominalu", "intickets",
	// Israel
	"eventer.co.il", "leaan.co.il", "tixwise", "smarticket.co.il", "go-out.co",
	// our own pages and the platform we replace
	"bil24", "arenasoldout",
}

// IsTicketingHost reports whether host (a URL's hostname) belongs to a
// ticketing platform from the list. Case and a trailing dot do not matter.
func IsTicketingHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	labels := strings.Split(h, ".")
	for _, entry := range ticketingDomains {
		if strings.Contains(entry, ".") {
			if h == entry || strings.HasSuffix(h, "."+entry) {
				return true
			}
			continue
		}
		// A bare platform name matches a label anywhere but the public
		// suffix (the last label): ticketmaster.es, www.eventbrite.co.uk.
		for _, l := range labels[:len(labels)-1] {
			if l == entry {
				return true
			}
		}
	}
	return false
}
