package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// Options are the run's settings.
type Options struct {
	OrgID         uuid.UUID
	Currency      string
	DryRun        bool
	IncludeDrafts bool
	// OnlyNew leaves a product or page that already exists untouched.
	OnlyNew bool
	// SyncStock overwrites the stock of an existing variant with the CSV's.
	// Off by default: once the shop sells, its own stock is the truth.
	SyncStock     bool
	SkipImages    bool
	SiteURL       string
	DefaultRegion string
	Locale        string
}

// SettingsDefaults are the shop settings written when the shop has none.
type SettingsDefaults struct {
	Currency         string
	ShippingFeeMinor int64
	ShippingLabel    string
	Country          string
	AgeGate          int32
	Announcement     string
	ContactEmail     string
}

// Importer loads a Shopify export into one organization's shop.
type Importer struct {
	opt    Options
	pool   *pgxpool.Pool // nil: offline dry run
	svc    *shop.Service
	q      *gen.Queries
	media  mediaStore
	images *imageSource
	rep    *Report
	log    *slog.Logger
	now    func() time.Time
}

// NewImporter builds the importer. pool may be nil only for a dry run.
func NewImporter(opt Options, pool *pgxpool.Pool, media mediaStore, images *imageSource, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.Default()
	}
	im := &Importer{opt: opt, pool: pool, media: media, images: images, log: log, now: func() time.Time { return time.Now().UTC() }}
	im.rep = &Report{DryRun: opt.DryRun, Currency: opt.Currency, Offline: pool == nil}
	if pool != nil {
		im.svc = shop.New(pool, log)
		im.q = gen.New(pool)
	}
	if im.images == nil {
		im.images = newImageSource("")
	}
	return im
}

// Report returns the report built so far.
func (im *Importer) Report() *Report { return im.rep }

func (im *Importer) offline() bool { return im.pool == nil }

// CheckOrg makes sure the organization exists and remembers its name.
func (im *Importer) CheckOrg(ctx context.Context) error {
	if im.offline() {
		return nil
	}
	var name string
	err := im.pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, im.opt.OrgID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("organization %s does not exist in this database", im.opt.OrgID)
	}
	if err != nil {
		return fmt.Errorf("read organization: %w", err)
	}
	im.rep.OrgName = name
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Settings
// ─────────────────────────────────────────────────────────────────────────────

// ImportSettings writes the shop settings, but only when the organization has
// none yet: an organizer's own edits are never overwritten. It must run
// BEFORE the first product is created, because creating a product makes the
// default settings row.
func (im *Importer) ImportSettings(ctx context.Context, d SettingsDefaults) error {
	if !im.offline() {
		cur, err := im.svc.GetSettings(ctx, im.opt.OrgID)
		if err != nil {
			return fmt.Errorf("read settings: %w", err)
		}
		if cur.Configured {
			im.rep.Settings.add(outUnchanged, "settings already exist, left as they are")
			return nil
		}
	}
	if im.opt.DryRun {
		im.rep.Settings.add(outCreated, "")
		return nil
	}
	in := shop.SettingsInput{
		Currency: d.Currency, ShippingFeeMinor: d.ShippingFeeMinor, ShippingLabel: d.ShippingLabel,
		AnnouncementText: d.Announcement,
	}
	if d.Country != "" {
		c := d.Country
		in.CountryISO2 = &c
	}
	if d.AgeGate > 0 {
		a := d.AgeGate
		in.AgeGateMinAge = &a
	}
	if d.ContactEmail != "" {
		e := d.ContactEmail
		in.ContactEmail = &e
	}
	if _, err := im.svc.PutSettings(ctx, im.opt.OrgID, in); err != nil {
		im.rep.Settings.add(outFailed, err.Error())
		return nil
	}
	im.rep.Settings.add(outCreated, "")
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Pages
// ─────────────────────────────────────────────────────────────────────────────

// ImportPages creates or updates the static pages.
func (im *Importer) ImportPages(ctx context.Context, pages []ImportPage) error {
	existing := map[string]gen.ShopPageRow{}
	if !im.offline() {
		rows, err := im.svc.ListPages(ctx, im.opt.OrgID)
		if err != nil {
			return fmt.Errorf("list pages: %w", err)
		}
		for _, r := range rows {
			existing[r.Slug] = r
		}
	}
	for _, p := range pages {
		if p.RemoteImages > 0 {
			im.rep.Pages.add("", fmt.Sprintf("a page picture still points at the old store's CDN (%s)", p.Slug))
		}
		cur, ok := existing[p.Slug]
		outcome := outCreated
		switch {
		case ok && im.opt.OnlyNew:
			im.rep.Pages.add(outSkipped, "page exists and --only-new is set")
			continue
		case ok && cur.Title == p.Title && cur.BodyHTML == p.BodyHTML && cur.Position == p.Position:
			im.rep.Pages.add(outUnchanged, "")
			continue
		case ok:
			outcome = outUpdated
		}
		if im.opt.DryRun {
			im.rep.Pages.add(outcome, "")
			continue
		}
		if _, err := im.svc.PutPage(ctx, im.opt.OrgID, p.Slug, p.Title, p.BodyHTML, p.Position); err != nil {
			im.rep.Pages.add(outFailed, err.Error())
			im.rep.problem("page %q: %v", p.Slug, err)
			continue
		}
		im.rep.Pages.add(outcome, "")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Products
// ─────────────────────────────────────────────────────────────────────────────

// variantPlan is what planVariants decided.
type variantPlan struct {
	Inputs                       []shop.VariantInput
	Created, Updated, Same, Kept int
}

// Dirty reports whether the plan changes anything.
func (p variantPlan) Dirty() bool { return p.Created > 0 || p.Updated > 0 }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optEq(a, b *string) bool { return deref(a) == deref(b) }

func int64PtrEq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// planVariants matches the CSV variants to the stored ones (by SKU, then by
// option values, then by title) and builds the list ReplaceVariants takes.
// A stored variant that no CSV row matches is kept as it is: the importer
// never deletes what an organizer added. Stock of a matched variant stays the
// stored one unless syncStock.
func planVariants(existing []gen.ShopVariantRow, want []ImportVariant, syncStock bool) variantPlan {
	var plan variantPlan
	used := make([]bool, len(existing))
	find := func(w ImportVariant) int {
		if w.SKU != "" {
			for i, e := range existing {
				if !used[i] && deref(e.SKU) == w.SKU {
					return i
				}
			}
		}
		for i, e := range existing {
			if !used[i] && optEq(e.Option1, w.Option1) && optEq(e.Option2, w.Option2) && optEq(e.Option3, w.Option3) &&
				(w.Option1 != nil || w.Option2 != nil || w.Option3 != nil || e.Title == w.Title) {
				return i
			}
		}
		for i, e := range existing {
			if !used[i] && e.Title == w.Title {
				return i
			}
		}
		return -1
	}
	for _, w := range want {
		pos := w.Position
		track := w.TrackStock
		in := shop.VariantInput{
			Title: w.Title, SKU: strPtr(w.SKU), PriceMinor: w.PriceMinor, CompareAtMinor: w.CompareAtMinor,
			Stock: w.Stock, TrackStock: &track, Position: &pos,
			Option1: w.Option1, Option2: w.Option2, Option3: w.Option3,
		}
		i := find(w)
		if i < 0 {
			plan.Inputs = append(plan.Inputs, in)
			plan.Created++
			continue
		}
		used[i] = true
		e := existing[i]
		id := e.ID
		in.ID = &id
		if !syncStock {
			in.Stock = e.Stock
		}
		same := e.Title == in.Title && optEq(e.SKU, in.SKU) && e.PriceMinor == in.PriceMinor &&
			int64PtrEq(e.CompareAtMinor, in.CompareAtMinor) && e.Stock == in.Stock && e.TrackStock == track &&
			e.Position == pos && optEq(e.Option1, in.Option1) && optEq(e.Option2, in.Option2) && optEq(e.Option3, in.Option3)
		if same {
			plan.Same++
		} else {
			plan.Updated++
		}
		plan.Inputs = append(plan.Inputs, in)
	}
	for i, e := range existing {
		if used[i] {
			continue
		}
		id := e.ID
		track := e.TrackStock
		pos := e.Position
		plan.Inputs = append(plan.Inputs, shop.VariantInput{
			ID: &id, Title: e.Title, SKU: e.SKU, PriceMinor: e.PriceMinor, CompareAtMinor: e.CompareAtMinor,
			Stock: e.Stock, TrackStock: &track, Position: &pos, Option1: e.Option1, Option2: e.Option2, Option3: e.Option3,
		})
		plan.Kept++
	}
	return plan
}

func (im *Importer) productInput(p ImportProduct, body string) shop.ProductInput {
	slug, title, status := p.Slug, p.Title, p.Status
	vendor, typ := p.Vendor, p.Type
	sort := p.SortOrder
	return shop.ProductInput{Slug: &slug, Title: &title, BodyHTML: &body, Status: &status, ProductType: &typ, Vendor: &vendor, SortOrder: &sort}
}

// ImportProducts creates or updates every selected product with its variants
// and pictures. One product failing never stops the others.
func (im *Importer) ImportProducts(ctx context.Context, pf *ProductsFile) {
	for _, pr := range pf.Problems {
		im.rep.Products.add(outSkipped, "row could not be read")
		im.rep.problem("products: %s", pr)
	}
	keep, skipped := SelectProducts(pf.Products, im.opt.IncludeDrafts)
	im.rep.SkippedByStatus = skipped
	for n := range skipped {
		im.rep.Products.add(outSkipped, "status "+n+" (use --include-drafts)")
	}
	for _, p := range keep {
		for _, w := range p.Warnings {
			im.rep.Products.add("", p.Handle+": "+w)
		}
		if err := im.importProduct(ctx, p); err != nil {
			im.rep.Products.add(outFailed, "see problems")
			im.rep.problem("product %q: %v", p.Handle, err)
		}
	}
}

func (im *Importer) countMoney(p ImportProduct) {
	for _, v := range p.Variants {
		im.rep.notePrice(v.PriceMinor)
		im.rep.StockValueMinor += v.PriceMinor * int64(v.Stock)
	}
}

func (im *Importer) importProduct(ctx context.Context, p ImportProduct) error {
	body := CleanHTML(p.BodyHTML, cleanOptions{BaseURL: im.opt.SiteURL, KeepImages: true})
	if len(body) > shop.MaxBodyHTMLLen {
		im.rep.Products.add(outSkipped, "description is longer than the shop allows")
		return fmt.Errorf("description is %d characters after cleaning, the shop allows %d", len(body), shop.MaxBodyHTMLLen)
	}
	in := im.productInput(p, body)

	var cur *gen.ShopProductRow
	if !im.offline() {
		row, err := im.q.GetShopProductBySlug(ctx, im.opt.OrgID, p.Slug)
		switch {
		case err == nil:
			cur = &row
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("look up product: %w", err)
		}
	}

	// New product.
	if cur == nil {
		if im.opt.DryRun {
			im.countMoney(p)
			im.rep.Variants.Created += len(p.Variants)
			im.rep.Products.add(outCreated, "")
			im.dryImages(p, nil)
			return nil
		}
		view, err := im.svc.CreateProduct(ctx, im.opt.OrgID, in, planVariants(nil, p.Variants, true).Inputs)
		if err != nil {
			return err
		}
		im.countMoney(p)
		im.rep.Variants.Created += len(p.Variants)
		im.rep.Products.add(outCreated, "")
		return im.syncImages(ctx, view.ID, p, nil)
	}

	// Existing product.
	if im.opt.OnlyNew {
		im.rep.Products.add(outSkipped, "product exists and --only-new is set")
		return nil
	}
	view, err := im.svc.GetProduct(ctx, im.opt.OrgID, cur.ID)
	if err != nil {
		return err
	}
	fieldsSame := cur.Title == p.Title && cur.BodyHTML == body && cur.Status == p.Status &&
		cur.ProductType == p.Type && cur.Vendor == p.Vendor && cur.SortOrder == p.SortOrder
	plan := planVariants(view.Variants, p.Variants, im.opt.SyncStock)
	im.rep.Variants.Created += plan.Created
	im.rep.Variants.Updated += plan.Updated
	im.rep.Variants.Unchanged += plan.Same
	if plan.Kept > 0 {
		im.rep.Variants.add("", fmt.Sprintf("%d stored variants not in the CSV were kept", plan.Kept))
	}
	im.countMoney(p)

	outcome := outUnchanged
	if !fieldsSame || plan.Dirty() {
		outcome = outUpdated
	}
	if im.opt.DryRun {
		im.rep.Products.add(outcome, "")
		im.dryImages(p, view.Images)
		return nil
	}
	if !fieldsSame {
		if _, err := im.svc.UpdateProduct(ctx, im.opt.OrgID, cur.ID, in); err != nil {
			return err
		}
	}
	if plan.Dirty() {
		if _, err := im.svc.ReplaceVariants(ctx, im.opt.OrgID, cur.ID, plan.Inputs); err != nil {
			return err
		}
	}
	im.rep.Products.add(outcome, "")
	return im.syncImages(ctx, cur.ID, p, view.Images)
}

// dryImages estimates the picture work without downloading anything: a new
// product fetches all of its pictures, an existing one the ones it lacks.
func (im *Importer) dryImages(p ImportProduct, have []gen.ShopProductImageRow) {
	if im.opt.SkipImages {
		return
	}
	for i, img := range p.Images {
		if !im.images.available(img.Src) {
			im.rep.Images.add(outFailed, "no local copy and not an http(s) address")
			im.rep.MissingImages = append(im.rep.MissingImages, p.Handle+": "+fileName(img.Src))
			continue
		}
		if i < len(have) {
			im.rep.Images.add(outUnchanged, "")
		} else {
			im.rep.Images.add(outCreated, "")
		}
	}
}

// syncImages makes the product's pictures match the CSV's: a picture is
// recognised by the checksum of its bytes, so a re-run adds nothing twice.
// Pictures the CSV does not list are left attached.
func (im *Importer) syncImages(ctx context.Context, productID uuid.UUID, p ImportProduct, have []gen.ShopProductImageRow) error {
	if im.opt.SkipImages || len(p.Images) == 0 {
		return nil
	}
	if im.media == nil {
		return errors.New("media storage is not configured: set MEDIA_BACKEND (or pass --skip-images)")
	}
	byChecksum := map[string]gen.ShopProductImageRow{}
	for _, h := range have {
		if obj, err := im.media.GetByID(ctx, h.MediaID); err == nil {
			byChecksum[obj.ChecksumSHA256] = h
		}
	}
	imgs := append([]ImportImage(nil), p.Images...)
	sort.SliceStable(imgs, func(i, j int) bool { return imgs[i].Position < imgs[j].Position })
	for _, img := range imgs {
		pos := img.Position - 1
		if pos < 0 {
			pos = 0
		}
		data, err := im.images.load(ctx, img.Src)
		if err != nil {
			im.rep.Images.add(outFailed, "could not be read")
			im.rep.MissingImages = append(im.rep.MissingImages, fmt.Sprintf("%s: %s (%v)", p.Handle, fileName(img.Src), err))
			continue
		}
		if row, ok := byChecksum[data.Checksum]; ok {
			if row.Position == pos && row.Alt == img.Alt {
				im.rep.Images.add(outUnchanged, "")
				continue
			}
			if _, err := im.svc.AddImage(ctx, im.opt.OrgID, productID, row.MediaID, &pos, img.Alt); err != nil {
				return fmt.Errorf("move picture: %w", err)
			}
			im.rep.Images.add(outUpdated, "")
			continue
		}
		mediaID, err := im.upload(ctx, productID, data)
		if err != nil {
			im.rep.Images.add(outFailed, "could not be stored")
			im.rep.MissingImages = append(im.rep.MissingImages, fmt.Sprintf("%s: %s (%v)", p.Handle, fileName(img.Src), err))
			continue
		}
		if _, err := im.svc.AddImage(ctx, im.opt.OrgID, productID, mediaID, &pos, img.Alt); err != nil {
			return fmt.Errorf("attach picture: %w", err)
		}
		byChecksum[data.Checksum] = gen.ShopProductImageRow{ProductID: productID, MediaID: mediaID, Position: pos, Alt: img.Alt}
		im.rep.Images.add(outCreated, "")
	}
	return nil
}

func (im *Importer) upload(ctx context.Context, productID uuid.UUID, d imageData) (uuid.UUID, error) {
	key, err := mediastore.NewStorageKey(shopOwnerType)
	if err != nil {
		return uuid.Nil, err
	}
	sum, size, err := im.media.PutAndStream(ctx, key, d.ContentType, bytes.NewReader(d.Body))
	if err != nil {
		return uuid.Nil, err
	}
	if sum != d.Checksum || size != int64(len(d.Body)) {
		return uuid.Nil, errors.New("storage returned a different checksum than the bytes read")
	}
	org := im.opt.OrgID
	obj, err := im.media.Insert(ctx, mediastore.InsertInput{
		OrgID: &org, OwnerType: shopOwnerType, OwnerID: &productID, StorageBackend: im.media.Backend(),
		StorageKey: key, ContentType: d.ContentType, ByteSize: size, ChecksumSHA256: sum,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return obj.ID, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Customers
// ─────────────────────────────────────────────────────────────────────────────

const importSource = "import:shopify"

// ImportCustomers resolves every customer through customers.Resolve (e-mail
// and phone identities), links it to the organization and records the
// marketing consent when the export says "yes". Each customer is its own
// transaction, so one bad row costs one row.
func (im *Importer) ImportCustomers(ctx context.Context, cf *CustomersFile) error {
	for range cf.Problems {
		im.rep.Customers.add(outSkipped, "no usable e-mail and no phone")
	}
	for i := 0; i < cf.Duplicates; i++ {
		im.rep.Customers.add(outSkipped, "e-mail appears twice in the file")
	}
	if im.offline() {
		for _, c := range cf.Customers {
			im.rep.Customers.add(outCreated, "")
			if c.MarketingEmail {
				im.rep.Consents.add(outCreated, "")
			}
		}
		return nil
	}
	for _, c := range cf.Customers {
		if len(c.Warnings) > 0 {
			im.rep.Customers.add("", "an invalid e-mail was ignored")
		}
		if im.opt.DryRun {
			im.dryCustomer(ctx, c)
			continue
		}
		if err := im.importCustomer(ctx, c); err != nil {
			im.rep.Customers.add(outFailed, "database error")
			im.log.Warn("customer import failed", "line", c.Line, "error", err.Error())
			im.rep.problem("customers: line %d: %v", c.Line, err)
		}
	}
	return nil
}

// dryCustomer classifies one customer by reading the identities.
func (im *Importer) dryCustomer(ctx context.Context, c ImportCustomer) {
	store := customers.NewStoreFromQueries(im.q)
	found := false
	if c.Email != "" {
		if _, err := store.GetIdentityByStrong(ctx, customers.KindEmail, c.Email); err == nil {
			found = true
		}
	}
	if !found && c.Phone != "" {
		if p, err := customers.NormalizePhone(c.Phone, c.Region); err == nil {
			if _, err := store.GetIdentityByStrong(ctx, customers.KindPhone, p); err == nil {
				found = true
			}
		}
	}
	if found {
		im.rep.Customers.add(outUnchanged, "already a customer")
	} else {
		im.rep.Customers.add(outCreated, "")
	}
	if c.MarketingEmail {
		im.rep.Consents.add(outCreated, "")
	}
}

func (im *Importer) importCustomer(ctx context.Context, c ImportCustomer) error {
	tx, err := im.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	store := customers.NewStoreFromQueries(gen.New(tx))
	now := im.now()
	res, err := customers.Resolve(ctx, store, customers.ResolveInput{
		Email: c.Email, Phone: c.Phone, Name: c.Name, DefaultRegion: c.Region,
		Locale: im.opt.Locale, Source: importSource, Now: now,
	})
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}
	if err := customers.LinkOrg(ctx, store, res.Customer.ID, im.opt.OrgID, "import"); err != nil {
		return fmt.Errorf("link to organization: %w", err)
	}
	newConsent := false
	if c.MarketingEmail {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM customer_consents
			WHERE customer_id = $1 AND org_id = $2 AND kind = 'marketing')`, res.Customer.ID, im.opt.OrgID).Scan(&exists); err != nil {
			return fmt.Errorf("read consent: %w", err)
		}
		if !exists {
			// First consent wins; an existing row (even a withdrawn one) is never touched.
			if err := gen.New(tx).InsertCustomerConsent(ctx, res.Customer.ID, im.opt.OrgID, "marketing", now, importSource); err != nil {
				return fmt.Errorf("record consent: %w", err)
			}
			newConsent = true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	switch {
	case res.Created:
		im.rep.Customers.add(outCreated, "")
	case res.MergeCandidateQueued:
		im.rep.Customers.add(outUnchanged, "matched, a possible duplicate was queued for review")
	default:
		im.rep.Customers.add(outUnchanged, "already a customer")
	}
	if newConsent {
		im.rep.Consents.add(outCreated, "")
	} else if c.MarketingEmail {
		im.rep.Consents.add(outUnchanged, "consent row already exists")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Orders
// ─────────────────────────────────────────────────────────────────────────────

// NoteOrders records the parsed orders file in the report. The second wave
// (shop_orders with source shopify) is deliberately not written.
func (im *Importer) NoteOrders(of *OrdersFile) {
	s := SummarizeOrders(of)
	im.rep.Orders = &s
	for _, p := range of.Problems {
		im.rep.problem("orders: %s", p)
	}
	im.rep.note("orders are only parsed and summed in this version: shop_orders with source shopify are the second wave")
}
