package disputes

// package.go — turns Facts into the evidence package: the PDF files (one per
// evidence field, inside the provider's page and size budget), the text
// fields of the answer, the list of gaps and, for an e-mail answer, the
// letter.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Budget is a provider's limit on all evidence files together.
type Budget struct {
	MaxBytes int
	MaxPages int
}

// StripeBudget keeps a margin under Stripe's 4.5 MB and Mastercard's 19
// pages for any card (spec 51.3 §5.3).
var StripeBudget = Budget{MaxBytes: 4_000_000, MaxPages: 19}

// EmailBudget is Flitt's stricter limit, used for every e-mail answer.
var EmailBudget = Budget{MaxBytes: 2_000_000, MaxPages: 19}

// Evidence file kinds (Stripe's field names; "full" is the organizer's copy).
const (
	FileReceipt       = "receipt"
	FileService       = "service_documentation"
	FileCommunication = "customer_communication"
	FilePolicy        = "refund_policy"
	FileFull          = "full"
)

// RenderedFile is one PDF of a package.
type RenderedFile struct {
	Kind        string
	Filename    string
	Content     []byte
	Pages       int
	SHA256      string
	ForProvider bool
}

// Built is the package before it is stored.
type Built struct {
	Locale     string
	Fields     map[string]string
	Gaps       []string
	Files      []RenderedFile
	EmailDraft *EmailDraft
	TotalBytes int
	TotalPages int
}

// filePriority orders the provider files by what the reason needs most; the
// last one is dropped first when the budget is exceeded.
func filePriority(group string) []string {
	switch group {
	case ReasonFraud:
		return []string{FileCommunication, FileReceipt, FileService, FilePolicy}
	case ReasonNotReceived:
		return []string{FileService, FileCommunication, FileReceipt, FilePolicy}
	case ReasonRefund, ReasonQuality:
		return []string{FilePolicy, FileReceipt, FileCommunication, FileService}
	}
	return []string{FileReceipt, FileService, FileCommunication, FilePolicy}
}

// pageCaps share the 19 pages between the four files.
var pageCaps = map[string]int{FileReceipt: 2, FileService: 5, FileCommunication: 6, FilePolicy: 6}

// BuildPackage renders the package. orgLocale picks the language of the
// organizer's full copy; the provider files are English.
func BuildPackage(d Dispute, f Facts, orgLocale string, budget Budget, emailTo string) (Built, error) {
	en := labelsFor("en")
	b := Built{Locale: packageLocale(orgLocale)}
	b.Gaps = gapsOf(f)
	b.Fields = textFields(d, f)

	caps := map[string]int{}
	for k, v := range pageCaps {
		caps[k] = v
	}
	order := filePriority(d.ReasonGroup)
	var files []RenderedFile
	// Each round takes one page or one file away, so the loop ends: at worst
	// every provider file is dropped and only the organizer's copy is left.
	for {
		files = files[:0]
		total, pages := 0, 0
		for _, kind := range order {
			if caps[kind] <= 0 {
				continue
			}
			rf, err := renderFile(kind, d, f, en, caps[kind])
			if err != nil {
				return Built{}, err
			}
			files = append(files, rf)
			total += len(rf.Content)
			pages += rf.Pages
		}
		if total <= budget.MaxBytes && pages <= budget.MaxPages {
			b.TotalBytes, b.TotalPages = total, pages
			break
		}
		// Over the budget: take a page from the least important file that
		// still has more than one, else drop that file.
		shrunk := false
		for i := len(order) - 1; i >= 0 && !shrunk; i-- {
			k := order[i]
			if caps[k] > 1 {
				caps[k]--
				shrunk = true
			}
		}
		if !shrunk {
			dropped := false
			for i := len(order) - 1; i >= 0; i-- {
				if caps[order[i]] > 0 {
					caps[order[i]] = 0
					dropped = true
					break
				}
			}
			if !dropped {
				files = files[:0]
				break
			}
		}
	}
	full, err := renderFile(FileFull, d, f, labelsFor(b.Locale), 0)
	if err != nil {
		return Built{}, err
	}
	b.Files = append(append(b.Files, files...), full)
	if d.Channel != ChannelAPI {
		b.EmailDraft = emailDraft(d, f, emailTo, files)
	}
	return b, nil
}

func renderFile(kind string, d Dispute, f Facts, l labels, maxPages int) (RenderedFile, error) {
	orderNo := "—"
	if f.Order != nil {
		orderNo = strconv.FormatInt(f.Order.Number, 10)
	}
	payment := d.Provider
	if f.Payment != nil && f.Payment.ChargeRef != "" {
		payment = d.Provider + " " + f.Payment.ChargeRef
	}
	subtitle := fmt.Sprintf(l.get("subtitle"), sellerName(f), payment, deref(d.ProviderDisputeID))
	var r *report
	switch kind {
	case FileReceipt:
		r = newReport(fmt.Sprintf(l.get("title_receipt"), orderNo), l, maxPages, subtitle)
		writeSeller(r, f)
		writeOrder(r, f)
		writeRefunds(r, f)
	case FileService:
		r = newReport(l.get("title_service"), l, maxPages, subtitle)
		writeEvent(r, f)
		writeTickets(r, f)
		writeScans(r, f)
	case FileCommunication:
		r = newReport(l.get("title_comm"), l, maxPages, subtitle)
		writeAccess(r, f)
		writeLetters(r, f)
		writeOpens(r, f)
	case FilePolicy:
		r = newReport(l.get("title_policy"), l, maxPages, subtitle)
		writeConsent(r, f)
		writeTerms(r, f)
	default:
		r = newReport(fmt.Sprintf(l.get("title_full"), orderNo), l, 0, subtitle)
		writeDispute(r, d, f)
		writeSeller(r, f)
		writeOrder(r, f)
		writeEvent(r, f)
		writeTickets(r, f)
		writeScans(r, f)
		writeAccess(r, f)
		writeLetters(r, f)
		writeOpens(r, f)
		writeConsent(r, f)
		writeRefunds(r, f)
		writeTerms(r, f)
	}
	content, pages, err := r.bytes()
	if err != nil {
		return RenderedFile{}, fmt.Errorf("disputes: render %s: %w", kind, err)
	}
	sum := sha256.Sum256(content)
	return RenderedFile{
		Kind: kind, Filename: fmt.Sprintf("order-%s-%s.pdf", orderNo, strings.ReplaceAll(kind, "_", "-")),
		Content: content, Pages: pages, SHA256: hex.EncodeToString(sum[:]), ForProvider: kind != FileFull,
	}, nil
}

// ─── sections ────────────────────────────────────────────────────────────

func sellerName(f Facts) string {
	if f.Org.LegalName != "" {
		return f.Org.LegalName
	}
	return f.Org.Name
}

func writeDispute(r *report, d Dispute, f Facts) {
	if !r.heading(r.lbl.get("sec_dispute")) {
		return
	}
	due := ""
	if d.DueBy != nil {
		due = fmtTime(*d.DueBy, "Europe/Madrid") + " (Madrid)"
	}
	rows := [][2]string{
		{r.lbl.get("provider"), d.Provider},
		{r.lbl.get("reason"), d.Reason},
		{r.lbl.get("amount"), money(d.AmountMinor, d.Currency)},
		{r.lbl.get("due_by"), due},
	}
	if f.Charge != nil {
		rows = append(rows,
			[2]string{r.lbl.get("three_ds"), f.Charge.ThreeDSecure},
			[2]string{r.lbl.get("card"), strings.TrimSpace(f.Charge.CardBrand + " " + f.Charge.Last4)},
			[2]string{r.lbl.get("descriptor"), f.Charge.StatementDescriptor})
	}
	if f.PriorOrders > 0 {
		rows = append(rows, [2]string{r.lbl.get("prior_orders"), strconv.Itoa(f.PriorOrders)})
	}
	r.kv(rows)
}

func writeSeller(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_seller")) {
		return
	}
	r.kv([][2]string{
		{r.lbl.get("legal_name"), sellerName(f)},
		{r.lbl.get("name"), f.Org.Name},
		{r.lbl.get("tax_id"), f.Org.TaxID},
		{r.lbl.get("registration"), f.Org.Registration},
		{r.lbl.get("address"), f.Org.Address},
		{r.lbl.get("country"), f.Org.Country},
		{r.lbl.get("email"), f.Org.ContactEmail},
		{r.lbl.get("website"), f.Org.Website},
	})
}

func writeOrder(r *report, f Facts) {
	if f.Order == nil {
		r.gap(r.lbl.get("gap_no_order"))
		return
	}
	o := f.Order
	tz := sessionTZ(f)
	if r.heading(r.lbl.get("sec_buyer")) {
		r.kv([][2]string{
			{r.lbl.get("name"), o.BuyerName},
			{r.lbl.get("email"), o.BuyerEmail},
			{r.lbl.get("phone"), o.BuyerPhone},
		})
	}
	if !r.heading(r.lbl.get("sec_order")) {
		return
	}
	paid := ""
	if o.PaidAt != nil {
		paid = fmtTime(*o.PaidAt, tz)
	}
	r.kv([][2]string{
		{r.lbl.get("order_number"), strconv.FormatInt(o.Number, 10)},
		{r.lbl.get("order_status"), o.Status},
		{r.lbl.get("created"), fmtTime(o.CreatedAt, tz)},
		{r.lbl.get("paid"), paid},
		{r.lbl.get("subtotal"), money(o.Subtotal, o.Currency)},
		{r.lbl.get("discount"), moneyIf(o.Discount, o.Currency)},
		{r.lbl.get("service_fee"), moneyIf(o.Charge, o.Currency)},
		{r.lbl.get("total"), money(o.Total, o.Currency)},
	})
	if f.Payment != nil && r.heading(r.lbl.get("sec_payment")) {
		ref := f.Payment.ChargeRef
		if ref == "" {
			ref = f.Payment.ProviderPaymentID
		}
		rows := [][2]string{
			{r.lbl.get("provider"), f.Payment.Provider},
			{r.lbl.get("payment_ref"), ref},
			{r.lbl.get("amount"), money(f.Payment.Amount, f.Payment.Currency)},
		}
		if f.Charge != nil {
			rows = append(rows,
				[2]string{r.lbl.get("three_ds"), f.Charge.ThreeDSecure},
				[2]string{r.lbl.get("card"), strings.TrimSpace(f.Charge.CardBrand + " " + f.Charge.Last4)},
				[2]string{r.lbl.get("descriptor"), f.Charge.StatementDescriptor})
		}
		if rc := f.Payment.Receipt; rc != nil {
			rows = append(rows,
				[2]string{r.lbl.get("receipt_no"), rc.Number},
				[2]string{r.lbl.get("receipt_link"), rc.URL})
			if rc.CopyType != "" && rc.ArchivedAt != nil {
				rows = append(rows, [2]string{r.lbl.get("receipt_copy"), fmt.Sprintf(r.lbl.get("receipt_copy_v"),
					rc.CopyType, rc.CopyBytes, rc.ArchivedAt.UTC().Format("2006-01-02 15:04"), rc.CopySHA256)}) // allow:timeformat
			}
		}
		r.kv(rows)
	}
	if len(f.Items) > 0 && r.heading(r.lbl.get("sec_items")) {
		rows := make([][]string, 0, len(f.Items))
		for _, it := range f.Items {
			rows = append(rows, []string{strconv.Itoa(it.Ordinal), it.Category, it.TicketNumber,
				money(it.UnitPrice, o.Currency), moneyIf(it.Discount, o.Currency), moneyIf(it.Charge, o.Currency), money(it.Total, o.Currency)})
		}
		r.table([]string{r.lbl.get("col_no"), r.lbl.get("col_category"), r.lbl.get("col_ticket"), r.lbl.get("col_price"),
			r.lbl.get("discount"), r.lbl.get("service_fee"), r.lbl.get("total")},
			[]float64{0.06, 0.30, 0.12, 0.13, 0.13, 0.13, 0.13}, rows)
	}
}

func writeEvent(r *report, f Facts) {
	if f.Session == nil || !r.heading(r.lbl.get("sec_event")) {
		return
	}
	s := f.Session
	venue := strings.Join(nonEmpty(s.VenueName, s.VenueAddress, s.City), ", ")
	r.kv([][2]string{
		{r.lbl.get("event"), s.EventName},
		{r.lbl.get("date"), fmtTime(s.StartAt, s.Timezone) + tzSuffix(s.Timezone)},
		{r.lbl.get("venue"), venue},
		{r.lbl.get("session_status"), s.Status},
	})
	if len(f.Changes) > 0 && r.heading(r.lbl.get("sec_changes")) {
		rows := make([][]string, 0, len(f.Changes))
		for _, c := range f.Changes {
			rows = append(rows, []string{fmtTime(c.At, s.Timezone), strings.Join(c.Kinds, ", ")})
		}
		r.table([]string{r.lbl.get("col_time"), r.lbl.get("col_kind")}, []float64{0.4, 0.6}, rows)
		r.gap(r.lbl.get("gap_reissued"))
	}
}

func writeTickets(r *report, f Facts) {
	if len(f.Tickets) == 0 || !r.heading(r.lbl.get("sec_tickets")) {
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Tickets))
	for _, t := range f.Tickets {
		used := ""
		if t.UsedAt != nil {
			used = fmtTime(*t.UsedAt, tz)
		}
		rows = append(rows, []string{t.Number, t.Category, t.Seat, t.Status, t.Barcode, used})
	}
	r.table([]string{r.lbl.get("col_ticket"), r.lbl.get("col_category"), r.lbl.get("col_seat"), r.lbl.get("col_status"),
		r.lbl.get("col_barcode"), r.lbl.get("col_used")}, []float64{0.11, 0.25, 0.16, 0.12, 0.18, 0.18}, rows)
}

func writeScans(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_scans")) {
		return
	}
	if len(f.Scans) == 0 {
		r.gap(r.lbl.get("gap_no_scans"))
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Scans))
	for _, s := range f.Scans {
		res := s.Result
		switch res {
		case "admitted":
			res = r.lbl.get("scan_admitted")
		case "denied":
			res = r.lbl.get("scan_denied")
		}
		rows = append(rows, []string{fmtTime(s.At, tz), s.TicketNumber, res, s.Source, s.Gate})
	}
	r.table([]string{r.lbl.get("col_time"), r.lbl.get("col_ticket"), r.lbl.get("col_result"), r.lbl.get("col_source"),
		r.lbl.get("col_gate")}, []float64{0.25, 0.15, 0.17, 0.18, 0.25}, rows)
}

func writeAccess(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_access")) {
		return
	}
	if !f.AccessLogKnown {
		r.gap(r.lbl.get("gap_access_off"))
		return
	}
	if len(f.Access) == 0 {
		r.gap(r.lbl.get("gap_no_access"))
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Access))
	for _, a := range f.Access {
		rows = append(rows, []string{fmtTime(a.At, tz), accessAction(r.lbl, a.Action), accessSource(r.lbl, a.Source),
			a.TicketNumber, a.IP, clip(a.UserAgent, 90)})
	}
	r.table([]string{r.lbl.get("col_time"), r.lbl.get("col_action"), r.lbl.get("col_source"), r.lbl.get("col_ticket"),
		r.lbl.get("col_ip"), r.lbl.get("col_device")}, []float64{0.17, 0.13, 0.13, 0.09, 0.14, 0.34}, rows)
}

func writeLetters(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_letters")) {
		return
	}
	if len(f.Letters) == 0 {
		r.gap(r.lbl.get("gap_no_letters"))
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Letters))
	for _, l := range f.Letters {
		sent := fmtTime(l.SentAt, tz)
		if l.ReceiptMissing {
			sent += " (" + r.lbl.get("receipt_missing") + ")"
		}
		rows = append(rows, []string{l.Kind, l.Recipient, clip(l.Subject, 80), sent, fmtTimePtr(l.DeliveredAt, tz),
			fmtTimePtr(firstTimePtr(l.OpenedAt, l.Clicked), tz), strings.Join(l.TicketNumbers, ", ")})
	}
	r.table([]string{r.lbl.get("col_kind"), r.lbl.get("col_to"), r.lbl.get("col_subject"), r.lbl.get("col_sent"),
		r.lbl.get("col_delivered"), r.lbl.get("col_opened"), r.lbl.get("col_tickets")},
		[]float64{0.08, 0.18, 0.22, 0.15, 0.13, 0.13, 0.11}, rows)
}

func writeOpens(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_opens")) {
		return
	}
	if len(f.Opens) == 0 {
		r.gap(r.lbl.get("gap_no_opens"))
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Opens))
	for _, o := range f.Opens {
		rows = append(rows, []string{fmtTime(o.At, tz), o.Event, clip(o.UserAgent, 120)})
	}
	r.table([]string{r.lbl.get("col_time"), r.lbl.get("col_event"), r.lbl.get("col_device")}, []float64{0.22, 0.14, 0.64}, rows)
}

func writeConsent(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_consent")) {
		return
	}
	if f.Consent == nil {
		r.gap(r.lbl.get("gap_no_consent"))
		return
	}
	c := f.Consent
	yes := r.lbl.get("no")
	if c.WithdrawalShown {
		yes = r.lbl.get("yes")
	}
	r.kv([][2]string{
		{r.lbl.get("accepted_at"), fmtTime(c.AcceptedAt, sessionTZ(f))},
		{r.lbl.get("ip"), c.IP},
		{r.lbl.get("browser"), c.UserAgent},
		{r.lbl.get("site"), c.Origin},
		{r.lbl.get("legal_name"), c.SellerLegalName},
		{r.lbl.get("terms_version"), c.TermsVersion},
		{r.lbl.get("terms_url"), c.TermsURL},
		{r.lbl.get("terms_hash"), c.TermsSHA256},
		{r.lbl.get("withdrawal"), yes},
	})
}

func writeTerms(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_terms")) {
		return
	}
	if f.Consent == nil || strings.TrimSpace(f.Consent.TermsHTML) == "" {
		r.gap(r.lbl.get("gap_no_terms"))
		return
	}
	r.para(htmlToText(f.Consent.TermsHTML))
}

func writeRefunds(r *report, f Facts) {
	if !r.heading(r.lbl.get("sec_refunds")) {
		return
	}
	if len(f.Refunds) == 0 {
		r.gap(r.lbl.get("gap_no_refunds"))
		return
	}
	tz := sessionTZ(f)
	rows := make([][]string, 0, len(f.Refunds))
	for _, x := range f.Refunds {
		rows = append(rows, []string{fmtTime(x.At, tz), money(x.Amount, x.Currency), x.State, x.TicketNumber, x.ProviderRef})
	}
	r.table([]string{r.lbl.get("col_time"), r.lbl.get("col_amount"), r.lbl.get("col_status"), r.lbl.get("col_ticket"),
		r.lbl.get("payment_ref")}, []float64{0.22, 0.16, 0.16, 0.14, 0.32}, rows)
}

// ─── gaps, text fields, letter ───────────────────────────────────────────

// gapsOf lists what the package cannot show (label keys, shown on the card).
func gapsOf(f Facts) []string {
	var g []string
	if f.Order == nil {
		return []string{"gap_no_order"}
	}
	if len(f.Letters) == 0 {
		g = append(g, "gap_no_letters")
	} else if len(f.Opens) == 0 {
		g = append(g, "gap_no_opens")
	}
	if !f.AccessLogKnown {
		g = append(g, "gap_access_off")
	} else if len(f.Access) == 0 {
		g = append(g, "gap_no_access")
	}
	if len(f.Scans) == 0 {
		g = append(g, "gap_no_scans")
	}
	if f.Consent == nil {
		g = append(g, "gap_no_consent")
	} else if strings.TrimSpace(f.Consent.TermsHTML) == "" {
		g = append(g, "gap_no_terms")
	}
	return g
}

// maxFieldRunes bounds one text field (Stripe caps the total at 150 000).
const maxFieldRunes = 18_000

// textFields are the text fields of the answer, by Stripe's names. Files are
// added by id when uploaded.
func textFields(d Dispute, f Facts) map[string]string {
	out := map[string]string{}
	if f.Order != nil {
		out["customer_name"] = f.Order.BuyerName
		out["customer_email_address"] = f.Order.BuyerEmail
	}
	if f.Consent != nil && f.Consent.IP != "" {
		out["customer_purchase_ip"] = f.Consent.IP
	} else if ip := purchaseTimeIP(f); ip != "" {
		out["customer_purchase_ip"] = ip
	}
	if f.Session != nil {
		out["service_date"] = localDate(f.Session.StartAt, f.Session.Timezone)
	}
	out["product_description"] = productDescription(f)
	if log := accessActivityLog(f); log != "" {
		out["access_activity_log"] = clip(log, maxFieldRunes)
	}
	if f.Consent != nil {
		disclosure := fmt.Sprintf("Before paying, the buyer ticked the box accepting the seller's terms of sale and refund policy (%s) at %s UTC from %s, IP %s. Terms version %s, SHA-256 %s. The terms are attached.",
			nz(f.Consent.TermsURL, "linked on the checkout page"), f.Consent.AcceptedAt.UTC().Format("2006-01-02 15:04"), // allow:timeformat
			nz(f.Consent.Origin, "the checkout page"), nz(f.Consent.IP, "unknown"), f.Consent.TermsVersion, nz(f.Consent.TermsSHA256, "not stored"))
		out["refund_policy_disclosure"] = disclosure
		if d.ReasonGroup == ReasonRefund {
			out["cancellation_policy_disclosure"] = disclosure
		}
	}
	out["uncategorized_text"] = clip(caseSummary(d, f), maxFieldRunes)
	for k, v := range out {
		if strings.TrimSpace(v) == "" {
			delete(out, k)
		}
	}
	return out
}

// purchaseTimeIP is the address of the first access within an hour of the
// payment, when no consent recorded the purchase address.
func purchaseTimeIP(f Facts) string {
	if f.Order == nil || f.Order.PaidAt == nil {
		return ""
	}
	for _, a := range f.Access {
		if a.IP != "" && !a.At.Before(*f.Order.PaidAt) && a.At.Sub(*f.Order.PaidAt) <= time.Hour {
			return a.IP
		}
	}
	return ""
}

func productDescription(f Facts) string {
	if f.Session == nil {
		return "Event tickets sold online."
	}
	s := f.Session
	cats := map[string]int{}
	for _, it := range f.Items {
		cats[it.Category]++
	}
	names := make([]string, 0, len(cats))
	for c, n := range cats {
		names = append(names, fmt.Sprintf("%d × %s", n, nz(c, "ticket")))
	}
	sort.Strings(names)
	desc := fmt.Sprintf("Admission tickets to the event \"%s\" on %s%s at %s.", s.EventName,
		fmtTime(s.StartAt, s.Timezone), tzSuffix(s.Timezone), strings.Join(nonEmpty(s.VenueName, s.VenueAddress, s.City), ", "))
	if len(names) > 0 {
		desc += " Tickets: " + strings.Join(names, ", ") + "."
	}
	if f.Order != nil {
		desc += fmt.Sprintf(" Order %d, total %s. The tickets are electronic and were delivered by e-mail as PDF files with a unique barcode each.",
			f.Order.Number, money(f.Order.Total, f.Order.Currency))
	}
	return desc
}

// accessActivityLog lists, with times, every proof that the buyer received
// and used the tickets.
func accessActivityLog(f Facts) string {
	type line struct {
		at   time.Time
		text string
	}
	var lines []line
	for _, l := range f.Letters {
		if l.Kind != "ticket" && l.Kind != "change" {
			continue
		}
		lines = append(lines, line{l.SentAt, fmt.Sprintf("E-mail with tickets %s sent to %s", strings.Join(l.TicketNumbers, ", "), l.Recipient)})
		if l.DeliveredAt != nil {
			lines = append(lines, line{*l.DeliveredAt, "E-mail delivered to " + l.Recipient})
		}
	}
	for _, o := range f.Opens {
		what := "E-mail opened"
		switch o.Event {
		case "click":
			what = "Link in the e-mail clicked"
		case "proxy_open":
			what = "E-mail loaded by the mail server (Apple Mail privacy)"
		}
		lines = append(lines, line{o.At, what + userAgentSuffix(o.UserAgent)})
	}
	for _, a := range f.Access {
		what := "Tickets shown on the order page"
		if a.Action == "pdf_download" {
			what = "Ticket PDF downloaded"
			if a.TicketNumber != "" {
				what += " (ticket " + a.TicketNumber + ")"
			}
		}
		if a.Source == "email" {
			what += " from the link in the e-mail"
		}
		if a.IP != "" {
			what += ", IP " + a.IP
		}
		lines = append(lines, line{a.At, what + userAgentSuffix(a.UserAgent)})
	}
	for _, s := range f.Scans {
		if s.Result != "admitted" {
			continue
		}
		where := ""
		if s.Gate != "" {
			where = " at " + s.Gate
		}
		lines = append(lines, line{s.At, "Ticket " + s.TicketNumber + " scanned and admitted at the venue entrance" + where})
	}
	if f.Consent != nil {
		lines = append(lines, line{f.Consent.AcceptedAt, "Terms of sale accepted at checkout, IP " + nz(f.Consent.IP, "unknown") + userAgentSuffix(f.Consent.UserAgent)})
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].at.Before(lines[j].at) })
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.at.UTC().Format("2006-01-02 15:04 UTC")) // allow:timeformat
		b.WriteString(" — ")
		b.WriteString(l.text)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func caseSummary(d Dispute, f Facts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The buyer purchased event admission tickets from %s.", sellerName(f))
	if f.Order != nil {
		fmt.Fprintf(&b, " Order %d of %s was paid", f.Order.Number, money(f.Order.Total, f.Order.Currency))
		if f.Order.PaidAt != nil {
			fmt.Fprintf(&b, " on %s UTC", f.Order.PaidAt.UTC().Format("2006-01-02 15:04")) // allow:timeformat
		}
		b.WriteString(".")
	}
	if f.Payment != nil && f.Payment.Receipt != nil && f.Payment.Receipt.Number != "" {
		fmt.Fprintf(&b, " The payment provider issued its receipt No. %s for this payment.", f.Payment.Receipt.Number)
	}
	delivered := 0
	for _, l := range f.Letters {
		if l.Kind == "ticket" && (l.DeliveredAt != nil || l.Status == "delivered" || l.Status == "opened") {
			delivered++
		}
	}
	if delivered > 0 {
		fmt.Fprintf(&b, " The tickets were delivered by e-mail to %s.", f.Order.BuyerEmail)
	} else if len(f.Letters) > 0 && f.Order != nil {
		fmt.Fprintf(&b, " The tickets were sent by e-mail to %s.", f.Order.BuyerEmail)
	}
	downloads := 0
	for _, a := range f.Access {
		if a.Action == "pdf_download" {
			downloads++
		}
	}
	if downloads > 0 {
		fmt.Fprintf(&b, " The buyer downloaded the ticket PDF %d time(s).", downloads)
	}
	admitted := 0
	for _, s := range f.Scans {
		if s.Result == "admitted" {
			admitted++
		}
	}
	if admitted > 0 {
		fmt.Fprintf(&b, " %d ticket scan(s) admitted the holder at the venue entrance, which shows the service was received.", admitted)
	}
	if f.Consent != nil {
		fmt.Fprintf(&b, " Before paying, the buyer accepted the terms of sale (attached) from IP %s.", nz(f.Consent.IP, "unknown"))
	}
	if f.PriorOrders > 0 && d.ReasonGroup == ReasonFraud {
		fmt.Fprintf(&b, " The same buyer made %d earlier order(s) with this seller that were never disputed.", f.PriorOrders)
	}
	if f.Charge != nil && f.Charge.ThreeDSecure != "" {
		fmt.Fprintf(&b, " The payment passed 3-D Secure (%s).", f.Charge.ThreeDSecure)
	}
	refunded := int64(0)
	for _, x := range f.Refunds {
		if x.State == "succeeded" {
			refunded += x.Amount
		}
	}
	if refunded > 0 && f.Order != nil {
		fmt.Fprintf(&b, " A refund of %s was already made to the buyer; the dispute would pay it twice.", money(refunded, f.Order.Currency))
	}
	if f.Session != nil {
		switch f.Session.Status {
		case "cancelled":
			b.WriteString(" The event date was cancelled.")
		default:
			if len(f.Changes) > 0 {
				b.WriteString(" The event date was changed and the buyer was told by e-mail.")
			}
		}
	}
	return b.String()
}

func emailDraft(d Dispute, f Facts, to string, files []RenderedFile) *EmailDraft {
	orderNo := "—"
	if f.Order != nil {
		orderNo = strconv.FormatInt(f.Order.Number, 10)
	}
	ref := deref(d.ProviderDisputeID)
	subject := fmt.Sprintf("Chargeback response — order %s, %s", orderNo, money(d.AmountMinor, d.Currency))
	if ref != "" {
		subject += ", " + strings.TrimPrefix(ref, "manual:")
	}
	var b strings.Builder
	b.WriteString("Hello,\n\n")
	fmt.Fprintf(&b, "Please find the evidence for the chargeback on order %s (%s) of the merchant %s.\n\n",
		orderNo, money(d.AmountMinor, d.Currency), sellerName(f))
	b.WriteString(caseSummary(d, f))
	b.WriteString("\n\nAttached:\n")
	for _, file := range files {
		fmt.Fprintf(&b, "- %s\n", file.Filename)
	}
	fmt.Fprintf(&b, "\nPlease reply to this e-mail; the merchant is in copy.\n\nKind regards,\n%s\n", sellerName(f))
	return &EmailDraft{To: to, Subject: subject, Body: b.String()}
}

// ─── formatting ─────────────────────────────────────────────────────────

var currencyExponent = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "JPY": 0, "KMF": 0, "KRW": 0, "MGA": 0, "PYG": 0, "RWF": 0,
	"UGX": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "JOD": 3, "KWD": 3, "OMR": 3, "TND": 3,
}

func money(minor int64, currency string) string {
	exp, ok := currencyExponent[strings.ToUpper(currency)]
	if !ok {
		exp = 2
	}
	neg := minor < 0
	if neg {
		minor = -minor
	}
	s := strconv.FormatInt(minor, 10)
	if exp > 0 {
		for len(s) <= exp {
			s = "0" + s
		}
		s = s[:len(s)-exp] + "." + s[len(s)-exp:]
	}
	if neg {
		s = "-" + s
	}
	return s + " " + strings.ToUpper(currency)
}

func moneyIf(minor int64, currency string) string {
	if minor == 0 {
		return ""
	}
	return money(minor, currency)
}

func loadLoc(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

func fmtTime(t time.Time, tz string) string {
	return t.In(loadLoc(tz)).Format("2006-01-02 15:04") // allow:timeformat
}

func fmtTimePtr(t *time.Time, tz string) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t, tz)
}

func localDate(t time.Time, tz string) string {
	return t.In(loadLoc(tz)).Format("2006-01-02") // allow:timeformat
}

func tzSuffix(tz string) string {
	if tz == "" {
		return " UTC"
	}
	return " (" + tz + ")"
}

func sessionTZ(f Facts) string {
	if f.Session != nil && f.Session.Timezone != "" {
		return f.Session.Timezone
	}
	return "UTC"
}

func firstTimePtr(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

func accessAction(l labels, a string) string {
	if a == "pdf_download" {
		return l.get("action_pdf")
	}
	return l.get("action_view")
}

func accessSource(l labels, s string) string {
	if s == "email" {
		return l.get("source_email")
	}
	return l.get("source_web")
}

func userAgentSuffix(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return ""
	}
	return " (" + clip(ua, 120) + ")"
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

func nz(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + truncateMarker
}

var (
	reBlock = regexp.MustCompile(`(?i)</?(p|div|h[1-6]|li|ul|ol|br|tr|table)[^>]*>`)
	reTag   = regexp.MustCompile(`<[^>]*>`)
	reBlank = regexp.MustCompile(`\n{3,}`)
)

// htmlToText flattens the stored terms HTML for the PDF.
func htmlToText(h string) string {
	s := reBlock.ReplaceAllString(h, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
