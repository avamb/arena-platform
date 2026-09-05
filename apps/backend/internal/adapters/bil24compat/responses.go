// responses.go — named Bil24 wire response structs (feature #476, spec §7).
//
// Every command response documented in spec section 7 gets a named Go struct
// here. The structs are the AUTHORITATIVE Bil24 wire contract for feature
// wave 1: all ID fields are int64 (never UUID strings), money fields are
// numbers with ≤2 decimals (float64), and `omitempty` is applied only where
// the spec text explicitly says a key is optional.
//
// The types intentionally live in the adapter package (not the httpserver
// hbil24 package) so:
//   - The wire contract has exactly one definition (§2 layout rule).
//   - The layout sentinel test in tests/staticanalysis/bil24compat_layout_188_test.go
//     continues to see the adapter package own the wire surface.
//   - Handlers in httpserver depend downward on the adapter, not sideways.
//
// Handlers construct these structs and pass them to WriteResponse (in wire.go)
// or embed them in the flat Bil24 envelope via Response.Data. The envelope
// keys (resultCode/description/command) live on Response; per-command payload
// fields live on the structs below.

package bil24compat

// GetAllActionsResponse is the payload for GET_ALL_ACTIONS (spec §7.1).
// Contains the catalog tree: countries → cities → venues, plus the flat
// actionList with per-event actionEventList sessions. All entity IDs are
// int64 sourced from compatibility_id_map or *.display_number.
type GetAllActionsResponse struct {
	CountryList []GetAllActionsCountry `json:"countryList"`
	CityList    []GetAllActionsCity    `json:"cityList"`
	ActionList  []GetAllActionsAction  `json:"actionList"`
}

// GetAllActionsCountry is one country row in GET_ALL_ACTIONS.countryList.
type GetAllActionsCountry struct {
	CountryID   int64  `json:"countryId"`
	CountryName string `json:"countryName"`
}

// GetAllActionsCity is one city row in GET_ALL_ACTIONS.cityList, containing
// the venues that belong to it.
type GetAllActionsCity struct {
	CityID    int64                `json:"cityId"`
	CityName  string               `json:"cityName"`
	CountryID int64                `json:"countryId"`
	VenueList []GetAllActionsVenue `json:"venueList"`
}

// GetAllActionsVenue is one venue row nested inside a city entry.
type GetAllActionsVenue struct {
	VenueID   int64   `json:"venueId"`
	VenueName string  `json:"venueName"`
	Address   string  `json:"address"`
	GeoLat    float64 `json:"geoLat"`
	GeoLon    float64 `json:"geoLon"`
}

// GetAllActionsAction is one event ("action" in Bil24 parlance) with its
// list of sessions (actionEventList). Poster URLs and description are
// omitempty because the spec allows missing media/descriptions.
type GetAllActionsAction struct {
	ActionID        int64                      `json:"actionId"`
	ActionName      string                     `json:"actionName"`
	FullActionName  string                     `json:"fullActionName,omitempty"`
	Description     string                     `json:"description,omitempty"`
	SmallPosterURL  string                     `json:"smallPosterUrl,omitempty"`
	BigPosterURL    string                     `json:"bigPosterUrl,omitempty"`
	MinPrice        float64                    `json:"minPrice"`
	MaxPrice        float64                    `json:"maxPrice"`
	Age             string                     `json:"age,omitempty"`
	OrganizerID     int64                      `json:"organizerId"`
	OrganizerName   string                     `json:"organizerName,omitempty"`
	FirstEventDate  string                     `json:"firstEventDate,omitempty"`
	LastEventDate   string                     `json:"lastEventDate,omitempty"`
	ActionEventList []GetAllActionsActionEvent `json:"actionEventList"`
}

// GetAllActionsActionEvent is one session inside an event's actionEventList.
// SeatingPlanID is 0 for pure GA sessions (spec §7.1). CategoryLimitList
// carries GA tiers only; tariffPlanList is always empty in wave 1 (arena has
// no tariff plans; the site synthesises a default variation).
type GetAllActionsActionEvent struct {
	ActionEventID     int64                             `json:"actionEventId"`
	CityID            int64                             `json:"cityId"`
	VenueID           int64                             `json:"venueId"`
	Day               string                            `json:"day"`
	Time              string                            `json:"time"`
	Currency          string                            `json:"currency"`
	SellEndTime       string                            `json:"sellEndTime"`
	SeatingPlanID     int64                             `json:"seatingPlanId"`
	SeatingPlanName   string                            `json:"seatingPlanName,omitempty"`
	ETicket           bool                              `json:"eTicket"`
	Availability      int                               `json:"availability"`
	MinPrice          float64                           `json:"minPrice"`
	ChargePercent     int                               `json:"chargePercent"`
	CategoryLimitList []GetAllActionsCategoryLimitEntry `json:"categoryLimitList"`
	TariffPlanList    []any                             `json:"tariffPlanList"`
}

// GetAllActionsCategoryLimitEntry wraps the GA-only categoryList per session.
// The nested structure matches the legacy wire shape the WP plugin parses
// (bil24-acf-sync.php:434-446 — presence of categoryLimitList[0].categoryList
// with placement=false distinguishes combined from pure-seated).
type GetAllActionsCategoryLimitEntry struct {
	CategoryList []GetAllActionsCategory `json:"categoryList"`
}

// GetAllActionsCategory is one GA tier row inside categoryLimitList.
type GetAllActionsCategory struct {
	CategoryPriceID   int64          `json:"categoryPriceId"`
	CategoryPriceName string         `json:"categoryPriceName"`
	Placement         bool           `json:"placement"`
	Price             float64        `json:"price"`
	Availability      int            `json:"availability"`
	TariffIDMap       map[string]any `json:"tariffIdMap"`
}

// GetSeatListResponse is the payload for GET_SEAT_LIST (spec §7.2). Contains
// the full category list plus the per-seat inventory (for seated sessions)
// or GA-unit pseudo-seats (for hybrid). seatId is session_seats.system_seat_id.
type GetSeatListResponse struct {
	Currency     string                `json:"currency"`
	CategoryList []GetSeatListCategory `json:"categoryList"`
	SeatList     []GetSeatListSeat     `json:"seatList"`
}

// GetSeatListCategory is one category row in GET_SEAT_LIST.categoryList.
// Placement is a three-state per spec: true for seated tiers, false for GA
// tiers, absent for pure-GA sessions (see wire.go comment on placement).
// Because a Go bool is not tristate we use *bool with omitempty; the handler
// sets it to nil to omit the key entirely.
type GetSeatListCategory struct {
	CategoryPriceID   int64          `json:"categoryPriceId"`
	CategoryPriceName string         `json:"categoryPriceName"`
	Price             float64        `json:"price"`
	Availability      int            `json:"availability"`
	Placement         *bool          `json:"placement,omitempty"`
	TariffIDMap       map[string]any `json:"tariffIdMap"`
}

// GetSeatListSeat is one seat (or GA pseudo-seat) row in
// GET_SEAT_LIST.seatList. TariffPlanID is always null in wave 1 (spec §7.2)
// so the field is *int64 with omitempty removed on purpose: legacy clients
// look up the key by name and expect the explicit null.
type GetSeatListSeat struct {
	SeatID          int64               `json:"seatId"`
	CategoryPriceID int64               `json:"categoryPriceId"`
	TariffPlanID    *int64              `json:"tariffPlanId"`
	Price           float64             `json:"price"`
	Available       bool                `json:"available"`
	Location        GetSeatListLocation `json:"location"`
}

// GetSeatListLocation is the `location` sub-object of a seat entry.
type GetSeatListLocation struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
	Number string `json:"number"`
}

// CreateUserResponse is the payload for CREATE_USER (spec §7.3).
type CreateUserResponse struct {
	UserID    int64  `json:"userId"`
	SessionID string `json:"sessionId"`
}

// ReservationResponse is the payload for RESERVATION and UN_RESERVE
// (spec §7.4). Financial fields are numbers with ≤2 decimals. seatList
// is the entire cart across all sessions of the gateway session (a GA
// unit appears as a pseudo-seat with its system_seat_id).
type ReservationResponse struct {
	CartTimeout int                       `json:"cartTimeout"`
	Currency    string                    `json:"currency"`
	Sum         float64                   `json:"sum"`
	Discount    float64                   `json:"discount"`
	Charge      float64                   `json:"charge"`
	TotalSum    float64                   `json:"totalSum"`
	SeatList    []ReservationSeatListItem `json:"seatList"`
}

// ReservationSeatListItem is one row in RESERVATION.seatList. All ID fields
// are int64: seatId is session_seats.system_seat_id; actionEventId and
// categoryPriceId come from compatibility_id_map.
type ReservationSeatListItem struct {
	SeatID          int64   `json:"seatId"`
	ActionEventID   int64   `json:"actionEventId"`
	CategoryPriceID int64   `json:"categoryPriceId"`
	TariffPlanID    *int64  `json:"tariffPlanId"`
	Price           float64 `json:"price"`
	Discount        float64 `json:"discount"`
}

// GetCartResponse is the payload for GET_CART (spec §7.5). Mirrors the
// aggregated cart across all sessions of the gateway session. Spec is
// explicit: only `totalSum` is emitted (the site's fallback chain reads
// totalAmount→estimatedTotal→estimateTotal→totalSum, we return only the last).
type GetCartResponse struct {
	CartTimeout     int                  `json:"cartTimeout"`
	Currency        string               `json:"currency"`
	Sum             float64              `json:"sum"`
	DiscountAmount  float64              `json:"discountAmount"`
	ChargeAmount    float64              `json:"chargeAmount"`
	TotalSum        float64              `json:"totalSum"`
	ActionEventList []GetCartActionEvent `json:"actionEventList"`
}

// GetCartActionEvent groups cart lines by session for GET_CART.
type GetCartActionEvent struct {
	ActionEventID int64                 `json:"actionEventId"`
	ChargePercent int                   `json:"chargePercent"`
	SeatList      []GetCartSeatListItem `json:"seatList"`
}

// GetCartSeatListItem is one row inside GET_CART's per-session seatList.
type GetCartSeatListItem struct {
	SeatID          int64   `json:"seatId"`
	CategoryPriceID int64   `json:"categoryPriceId"`
	TariffPlanID    *int64  `json:"tariffPlanId"`
	Price           float64 `json:"price"`
	Discount        float64 `json:"discount"`
}

// AddPromoCodesResponse is the payload for ADD_PROMO_CODES (spec §7.6).
// The three code lists partition the input by classification result.
type AddPromoCodesResponse struct {
	NewPromoCodeList   []string `json:"newPromoCodeList"`
	ExistPromoCodeList []string `json:"existPromoCodeList"`
	ErrorPromoCodeList []string `json:"errorPromoCodeList"`
}

// CreateOrderResponse is the payload for CREATE_ORDER_EXT (spec §7.7).
// OrderID is orders.system_id (arena-minted, ≥1e9). ExternalOrderID is the
// client-supplied echo (orderId in the request). Expiration is RFC3339.
type CreateOrderResponse struct {
	OrderID         int64   `json:"orderId"`
	ExternalOrderID string  `json:"externalOrderId"`
	Sum             float64 `json:"sum"`
	Discount        float64 `json:"discount"`
	Charge          float64 `json:"charge"`
	TotalSum        float64 `json:"totalSum"`
	Currency        string  `json:"currency"`
	Expiration      string  `json:"expiration"`
}

// GetTicketsByOrderResponse is the payload for GET_TICKETS_BY_ORDER
// (spec §7.10). TicketList carries per-ticket details; TicketIDList is a
// flat list of ticketIds used by the site as a cheap presence check.
type GetTicketsByOrderResponse struct {
	TicketList   []GetTicketsByOrderTicket `json:"ticketList"`
	TicketIDList []int64                   `json:"ticketIdList"`
}

// GetTicketsByOrderTicket is one row in GET_TICKETS_BY_ORDER.ticketList.
// TicketID is tickets.system_ticket_id; SeatID is
// session_seats.system_seat_id.
type GetTicketsByOrderTicket struct {
	TicketID        int64  `json:"ticketId"`
	PDFURL          string `json:"pdfUrl"`
	DownloadURL     string `json:"downloadUrl"`
	Barcode         string `json:"barcode"`
	SeatID          int64  `json:"seatId"`
	CategoryPriceID int64  `json:"categoryPriceId"`
}

// RefundTicketResponse is the payload for REFUND_TICKET (arena extension,
// spec §7.13). TicketID is echoed; RefundDate is RFC3339.
type RefundTicketResponse struct {
	TicketID   int64  `json:"ticketId"`
	RefundDate string `json:"refundDate"`
}

// GetSchemaResponse is the payload for GET_SCHEMA (spec §7.15). Kept for
// widget/partner compatibility; the site no longer uses it. seatId is
// system_seat_id (not UUID).
type GetSchemaResponse struct {
	SeatList []GetSchemaSeat `json:"seatList"`
}

// GetSchemaSeat is one row in GET_SCHEMA.seatList (coordinate + category
// index; per-seat price/status is served by GET_SEAT_LIST).
type GetSchemaSeat struct {
	SeatID        int64   `json:"seatId"`
	CategoryIndex int     `json:"categoryIndex"`
	X             float64 `json:"x"`
	Y             float64 `json:"y"`
}
