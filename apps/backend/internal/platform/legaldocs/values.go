package legaldocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/sellerinfo"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/withdrawal"
)

// RendererVersion is part of every inputs hash. Raise it whenever Render or
// BuildValues changes the HTML for the same data (a new markup rule, a new
// refund text): the next request then stores a new rendering instead of
// serving the old one under an unchanged hash.
const RendererVersion = 1

// Seller is the seller's data as stored (organizations + the channel).
type Seller struct {
	LegalName          string
	LegalForm          string
	RegistrationNumber string
	TaxID              string
	TaxScheme          string
	AddressLine1       string
	AddressLine2       string
	PostalCode         string
	City               string
	// Country is the ISO 3166-1 alpha-2 code of the seller (legal-address
	// country, else the organization's country).
	Country string
	// Phone is the channel's published phone (settings.hosted_page.public_contact).
	// The public e-mail is deliberately absent: the page never publishes it.
	Phone string
	// IsPlatform: the channel is merchant_of_record, the platform sells.
	IsPlatform bool
}

// Promoter is the promoter an event is presented under.
type Promoter struct {
	Name    string
	LegalID string
	Address string
	Website string
}

// Data is everything a template may print, before it is turned into Values.
type Data struct {
	// Locale is the language of the document version being rendered.
	Locale          string
	DocumentVersion int
	Seller          Seller
	Promoter        *Promoter
	RefundMode      string
}

// CountryName is the name of an ISO 3166-1 alpha-2 country in the given
// language, the code itself when it is unknown, "" for an empty code.
func CountryName(code, locale string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return ""
	}
	region, err := language.ParseRegion(code)
	if err != nil {
		return code
	}
	tag, err := language.Parse(locale)
	if err != nil {
		tag = language.English
	}
	if n := display.Regions(tag).Name(region); n != "" {
		return n
	}
	if n := display.English.Regions().Name(region); n != "" {
		return n
	}
	return code
}

// BuildValues resolves every placeholder of the allowlist from the data.
func BuildValues(d Data) Values {
	s := d.Seller
	trim := strings.TrimSpace
	countryName := CountryName(s.Country, d.Locale)

	var addr []string
	for _, p := range []string{s.AddressLine1, s.AddressLine2, trim(trim(s.PostalCode) + " " + trim(s.City))} {
		if p = trim(p); p != "" {
			addr = append(addr, p)
		}
	}
	if len(addr) > 0 && countryName != "" {
		addr = append(addr, countryName)
	}

	mode := d.RefundMode
	if !KnownRefundMode(mode) {
		mode = DefaultRefundMode
	}

	v := Values{
		"document.version": strconv.Itoa(d.DocumentVersion),

		"seller.legal_name":          trim(s.LegalName),
		"seller.legal_form":          trim(s.LegalForm),
		"seller.registration_number": trim(s.RegistrationNumber),
		"seller.tax_id":              trim(s.TaxID),
		"seller.ids_line":            sellerinfo.IDsLine(d.Locale, s.RegistrationNumber, s.TaxID, s.TaxScheme),
		"seller.address":             strings.Join(addr, ", "),
		"seller.country":             countryName,
		"seller.phone":               trim(s.Phone),
		"seller.is_platform":         s.IsPlatform,

		"promoter.name":     "",
		"promoter.legal_id": "",
		"promoter.address":  "",
		"promoter.website":  "",

		"refund.mode":            mode,
		"refund.mode_text":       RefundModeText(mode, d.Locale, countryName),
		"refund.no_refund":       mode == RefundNone,
		"refund.until_sales_end": mode == RefundUntilSalesEnd,
		"refund.by_law":          mode == RefundByLaw,

		"law.country":       countryName,
		"law.country_code":  strings.ToUpper(trim(s.Country)),
		"law.no_withdrawal": withdrawal.NoticeApplies(s.Country),
	}
	if p := d.Promoter; p != nil {
		v["promoter.name"] = trim(p.Name)
		v["promoter.legal_id"] = trim(p.LegalID)
		v["promoter.address"] = trim(p.Address)
		v["promoter.website"] = trim(p.Website)
	}
	return v
}

// refundModeTexts are the sentences {{refund.mode_text}} prints, per
// language. ENGLISH DRAFTS waiting for the lawyer (ST-3): every language
// falls back to English until a translation is approved. %s is the seller's
// country name.
var refundModeTexts = map[string]map[string][2]string{
	"en": {
		RefundNone:          {"You cannot return a ticket or exchange it for another date after you buy it.", ""},
		RefundUntilSalesEnd: {"You may ask the seller for a refund of a ticket until ticket sales for its session end.", ""},
		RefundByLaw: {
			"You may return a ticket only in the cases and within the time limits set by the law of %s.",
			"You may return a ticket only in the cases and within the time limits set by the law that applies to this sale.",
		},
	},
}

// RefundModeText is the sentence that states a refund mode in a language;
// country fills the by-law sentence (another sentence is used without it).
func RefundModeText(mode, locale, country string) string {
	texts, ok := refundModeTexts[locale]
	if !ok {
		texts = refundModeTexts[DefaultLocale]
	}
	t, ok := texts[mode]
	if !ok {
		t = refundModeTexts[DefaultLocale][DefaultRefundMode]
	}
	if strings.Contains(t[0], "%s") {
		if strings.TrimSpace(country) == "" {
			return t[1]
		}
		return strings.Replace(t[0], "%s", country, 1)
	}
	return t[0]
}

// Context names where a rendering was made for: the seller organization,
// the channel and (when known) the event. It is part of the inputs, so two
// channels of one organization never share a rendering row.
type Context struct {
	OrgID     uuid.UUID
	ChannelID *uuid.UUID
	EventID   *uuid.UUID
}

// Inputs is the canonical JSON of what a rendering was made from, and its
// SHA-256 (hex). The same context and values always give the same bytes:
// json.Marshal writes map keys sorted.
func Inputs(c Context, v Values) ([]byte, string, error) {
	doc := struct {
		Renderer  int     `json:"renderer"`
		OrgID     string  `json:"org_id"`
		ChannelID *string `json:"channel_id"`
		EventID   *string `json:"event_id"`
		Values    Values  `json:"values"`
	}{Renderer: RendererVersion, OrgID: c.OrgID.String(), Values: v}
	if c.ChannelID != nil {
		s := c.ChannelID.String()
		doc.ChannelID = &s
	}
	if c.EventID != nil {
		s := c.EventID.String()
		doc.EventID = &s
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	return raw, SHA256Hex(raw), nil
}

// SHA256Hex is the lower-case hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
