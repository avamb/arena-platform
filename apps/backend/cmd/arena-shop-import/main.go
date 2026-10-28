// Package main is arena-shop-import, a one-shot operator tool that loads a
// Shopify export into the merchandise shop of ONE organization (spec
// 08_architecture/47_merch_shop_module_ru.md section 8).
//
// What it reads (all optional, give the ones you have):
//
//	--products  Shopify Products CSV  -> shop products, variants, pictures
//	--customers Shopify Customers CSV -> customers (customers.Resolve), org
//	                                     link, marketing consent
//	--pages     folder of rendered theme HTML + --pages-map JSON -> shop pages
//	--orders    Shopify Orders CSV    -> parsed and summed only (second wave)
//
// It is idempotent. A product is keyed by its slug, which is the Shopify
// handle; a variant by SKU, then option values, then title; a picture by the
// checksum of its bytes; a customer by e-mail and phone identity; a page by
// slug. Running it again updates, never duplicates. The shop settings are
// written only when the organization has none yet. Stock of an existing
// variant is NOT overwritten (once the shop sells, its own stock is the
// truth) unless --sync-stock is given, and stored variants the CSV does not
// list are kept.
//
// Usage:
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x MEDIA_BACKEND=... \
//	arena-shop-import --org <uuid> --products products.csv \
//	  --customers customers.csv --pages theme_live/pages --pages-map pages.json
//
// Add --dry-run to see what would change: it reads the database (when
// DATABASE_URL is set) but writes nothing and downloads no picture.
//
// BUILD ISOLATION: operator tool only, never imported by arena-api or
// arena-worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "arena-shop-import: %v\n", err)
		os.Exit(1)
	}
}

type cliFlags struct {
	org           string
	products      string
	customers     string
	orders        string
	pagesDir      string
	pagesMap      string
	imagesDir     string
	dbURL         string
	currency      string
	siteURL       string
	defaultRegion string
	locale        string
	dryRun        bool
	includeDrafts bool
	onlyNew       bool
	syncStock     bool
	skipImages    bool
	skipSettings  bool
	skipCustomers bool

	shippingFee  string
	country      string
	ageGate      int
	announcement string
	contactEmail string
}

func parseFlags(args []string) (*cliFlags, error) {
	f := &cliFlags{}
	fs := flag.NewFlagSet("arena-shop-import", flag.ContinueOnError)
	fs.StringVar(&f.org, "org", "", "UUID of the organization that owns the shop (required)")
	fs.StringVar(&f.products, "products", "", "Shopify Products CSV")
	fs.StringVar(&f.customers, "customers", "", "Shopify Customers CSV")
	fs.StringVar(&f.orders, "orders", "", "Shopify Orders CSV (parsed and summed only)")
	fs.StringVar(&f.pagesDir, "pages", "", "folder with the rendered theme pages (html)")
	fs.StringVar(&f.pagesMap, "pages-map", "", "JSON file mapping page files to slugs (required with --pages)")
	fs.StringVar(&f.imagesDir, "images-dir", "", "folder with local copies of the pictures (by file name); otherwise they are downloaded from the CSV url")
	fs.StringVar(&f.dbURL, "db-url", "", "PostgreSQL DSN (overrides DATABASE_URL)")
	fs.StringVar(&f.currency, "currency", "ILS", "currency of the prices in the export")
	fs.StringVar(&f.siteURL, "site-url", "", "address that relative links in imported texts are resolved against (default: left relative)")
	fs.StringVar(&f.defaultRegion, "default-region", "IL", "country that reads a national phone number written without +")
	fs.StringVar(&f.locale, "locale", "ru", "language recorded for customers created by this run")
	fs.BoolVar(&f.dryRun, "dry-run", false, "report what would change, write nothing, download nothing")
	fs.BoolVar(&f.includeDrafts, "include-drafts", false, "also import draft, unlisted and archived products (default: only active)")
	fs.BoolVar(&f.onlyNew, "only-new", false, "leave products and pages that already exist untouched")
	fs.BoolVar(&f.syncStock, "sync-stock", false, "overwrite the stock of existing variants with the CSV's")
	fs.BoolVar(&f.skipImages, "skip-images", false, "do not import pictures")
	fs.BoolVar(&f.skipCustomers, "skip-customers", false, "do not import customers (by default customers_export.csv next to --products is imported with all marketing consents)")
	fs.BoolVar(&f.skipSettings, "skip-settings", false, "do not write the shop settings")
	fs.StringVar(&f.shippingFee, "shipping-fee", "50.00", "flat shipping fee for the new settings, in major units")
	fs.StringVar(&f.country, "country", "IL", "shop country for the new settings")
	fs.IntVar(&f.ageGate, "age-gate", 18, "age gate for the new settings, 0 = none")
	fs.StringVar(&f.announcement, "announcement", "Special discount 15% on gift sets", "announcement bar text for the new settings")
	fs.StringVar(&f.contactEmail, "contact-email", "sales@einatwinery.com", "contact e-mail for the new settings")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *cliFlags) validate() (uuid.UUID, error) {
	org, err := uuid.Parse(strings.TrimSpace(f.org))
	if err != nil {
		return uuid.Nil, errors.New("--org is required and must be the organization's UUID")
	}
	if f.products == "" && f.customers == "" && f.orders == "" && f.pagesDir == "" {
		return uuid.Nil, errors.New("nothing to import: give at least one of --products, --customers, --pages, --orders")
	}
	if f.pagesDir != "" && f.pagesMap == "" {
		return uuid.Nil, errors.New("--pages needs --pages-map, the JSON file that names the page files and slugs")
	}
	f.useSiblingCustomers()
	f.currency = strings.ToUpper(strings.TrimSpace(f.currency))
	if !shop.ValidCurrency(f.currency) {
		return uuid.Nil, fmt.Errorf("--currency %q is not a three-letter code", f.currency)
	}
	return org, nil
}

// customersFileName is the Shopify admin export's own name for the customers.
const customersFileName = "customers_export.csv"

// useSiblingCustomers makes the customers part of a run the default (owner
// decision 2026-10-28: ALL customers move, with ALL their marketing
// consents). When --products is given and --customers is not, the
// customers_export.csv that Shopify wrote into the same folder is used;
// --skip-customers opts out.
func (f *cliFlags) useSiblingCustomers() {
	if f.customers != "" || f.skipCustomers || f.products == "" {
		return
	}
	cand := filepath.Join(filepath.Dir(f.products), customersFileName)
	if st, err := os.Stat(cand); err == nil && !st.IsDir() { //nolint:gosec // sibling of the operator-given --products path
		f.customers = cand
	}
}

func run(args []string, out io.Writer) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	org, err := f.validate()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()

	// ── Read the files first: a bad file must fail before anything is written.
	var (
		pf    *ProductsFile
		cf    *CustomersFile
		of    *OrdersFile
		pages []ImportPage
		pageP []string
	)
	if f.products != "" {
		if pf, err = ParseProductsFile(f.products, f.currency); err != nil {
			return fmt.Errorf("--products: %w", err)
		}
	}
	if f.customers != "" && f.skipCustomers {
		return errors.New("--customers and --skip-customers contradict each other")
	}
	if f.customers != "" {
		if cf, err = ParseCustomersFile(f.customers, f.defaultRegion); err != nil {
			return fmt.Errorf("--customers: %w", err)
		}
	}
	if f.orders != "" {
		if of, err = ParseOrdersFile(f.orders); err != nil {
			return fmt.Errorf("--orders: %w", err)
		}
	}
	if f.pagesDir != "" {
		maps, err := LoadPageMapping(f.pagesMap)
		if err != nil {
			return fmt.Errorf("--pages-map: %w", err)
		}
		pages, pageP = LoadPages(f.pagesDir, maps, f.siteURL)
	}
	fee, err := ParseMinor(f.shippingFee, f.currency)
	if err != nil {
		return fmt.Errorf("--shipping-fee: %w", err)
	}

	// ── Database.
	dsn := f.dbURL
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	var pool *pgxpool.Pool
	switch {
	case dsn != "":
		pool, err = pgxpool.New(ctx, dsn)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping database: %w", err)
		}
	case f.dryRun:
		// Offline dry run: counts only.
	default:
		return errors.New("DATABASE_URL (or --db-url) is required unless --dry-run")
	}

	// ── Pictures need a media store, except in a dry run.
	var media mediaStore
	if !f.dryRun && !f.skipImages && pf != nil && hasImages(pf, f.includeDrafts) {
		repo, err := buildMedia(pool, log)
		if err != nil {
			return fmt.Errorf("media storage: %w (set MEDIA_BACKEND and its settings, or pass --skip-images)", err)
		}
		media = repo
	}

	opt := Options{
		OrgID: org, Currency: f.currency, DryRun: f.dryRun, IncludeDrafts: f.includeDrafts, OnlyNew: f.onlyNew,
		SyncStock: f.syncStock, SkipImages: f.skipImages, SiteURL: f.siteURL, DefaultRegion: f.defaultRegion, Locale: f.locale,
	}
	im := NewImporter(opt, pool, media, newImageSource(f.imagesDir), log)
	if err := im.CheckOrg(ctx); err != nil {
		return err
	}
	for _, p := range pageP {
		im.rep.problem("pages: %s", p)
	}

	if !f.skipSettings && (pf != nil || len(pages) > 0) {
		if err := im.ImportSettings(ctx, SettingsDefaults{
			Currency: f.currency, ShippingFeeMinor: fee, Country: f.country, AgeGate: int32(f.ageGate), //nolint:gosec // a small flag value
			Announcement: f.announcement, ContactEmail: f.contactEmail,
		}); err != nil {
			return err
		}
	}
	if len(pages) > 0 {
		if err := im.ImportPages(ctx, pages); err != nil {
			return err
		}
	}
	if pf != nil {
		im.ImportProducts(ctx, pf)
	}
	if cf != nil {
		if err := im.ImportCustomers(ctx, cf); err != nil {
			return err
		}
	}
	if of != nil {
		im.NoteOrders(of)
	}

	im.Report().Write(out)
	if n := failures(im.Report()); n > 0 {
		return fmt.Errorf("finished with %d failed items, see the report above", n)
	}
	return nil
}

func hasImages(pf *ProductsFile, includeDrafts bool) bool {
	keep, _ := SelectProducts(pf.Products, includeDrafts)
	for _, p := range keep {
		if len(p.Images) > 0 {
			return true
		}
	}
	return false
}

func failures(r *Report) int {
	return r.Products.Failed + r.Images.Failed + r.Customers.Failed + r.Pages.Failed + r.Settings.Failed
}

// buildMedia opens the media store the way arena-api and arena-worker do.
func buildMedia(pool *pgxpool.Pool, log *slog.Logger) (*mediastore.Repo, error) {
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.MediaBackend == "" {
		return nil, errors.New("MEDIA_BACKEND is not set")
	}
	st, err := storage.NewFromConfig(storage.Config{
		Backend:           storage.Backend(cfg.MediaBackend),
		LocalRoot:         cfg.MediaLocalRoot,
		S3Endpoint:        cfg.MediaS3Endpoint,
		S3Region:          cfg.MediaS3Region,
		S3Bucket:          cfg.MediaS3Bucket,
		S3AccessKeyID:     cfg.MediaS3AccessKeyID,
		S3SecretAccessKey: cfg.MediaS3SecretAccessKey,
		S3UsePathStyle:    cfg.MediaS3UsePathStyle,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	log.Info("media storage configured", "backend", cfg.MediaBackend)
	return mediastore.New(mediastore.Options{Pool: pool, Storage: st, SigningSecret: cfg.MediaSigningKey()})
}
