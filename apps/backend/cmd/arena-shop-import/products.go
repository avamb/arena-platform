package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// ImportVariant is one variant row of the Shopify Products CSV, already
// converted: money in minor units, options as pointers (nil = not set).
type ImportVariant struct {
	Line           int
	Title          string
	SKU            string
	PriceMinor     int64
	CompareAtMinor *int64
	Stock          int32
	TrackStock     bool
	Position       int32
	Option1        *string
	Option2        *string
	Option3        *string
}

// ImportImage is one picture row (Image Src, Image Position, Image Alt Text).
type ImportImage struct {
	Src      string
	Position int32
	Alt      string
}

// ImportProduct is one product (all CSV rows of one Handle).
type ImportProduct struct {
	Line          int // CSV line of the first row (1 = header)
	Handle        string
	Slug          string
	Title         string
	BodyHTML      string
	Vendor        string
	Type          string
	Tags          string
	ShopifyStatus string // active, draft, archived, unlisted
	Status        string // shop status: active, draft or archived
	SortOrder     int32
	Variants      []ImportVariant
	Images        []ImportImage
	Warnings      []string
}

// ProductsFile is the parsed Products CSV.
type ProductsFile struct {
	Products []ImportProduct
	Rows     int      // data rows read
	Problems []string // products that could not be used (reason with CSV line)
}

// shopStatus maps a Shopify product status to the shop's. Shopify's
// "unlisted" is a product reachable only by its link: the shop has no such
// state, so it is imported as a draft (hidden) rather than shown in the list.
func shopStatus(shopify string) string {
	switch strings.ToLower(strings.TrimSpace(shopify)) {
	case "active":
		return shop.ProductActive
	case "archived":
		return shop.ProductArchived
	default:
		return shop.ProductDraft
	}
}

type csvHeader map[string]int

func newHeader(rec []string) csvHeader {
	h := csvHeader{}
	for i, name := range rec {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\xef\xbb\xbf"))
		if _, dup := h[name]; !dup {
			h[name] = i
		}
	}
	return h
}

func (h csvHeader) get(rec []string, name string) string {
	return strings.TrimSpace(h.raw(rec, name))
}

// raw returns the field without trimming (the HTML body keeps its text).
func (h csvHeader) raw(rec []string, name string) string {
	i, ok := h[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}

func (h csvHeader) require(names ...string) error {
	for _, n := range names {
		if _, ok := h[n]; !ok {
			return fmt.Errorf("column %q is missing: this does not look like a Shopify export", n)
		}
	}
	return nil
}

// ParseProductsFile reads a Shopify Products CSV from disk.
func ParseProductsFile(path, currency string) (*ProductsFile, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseProducts(f, currency)
}

// ParseProducts parses the Shopify Products CSV. Rows of one Handle belong to
// one product: the first carries the product fields, later ones add variants
// (a Variant Price, an option value or a SKU) and/or pictures (an Image Src).
// A problem with one product is recorded and the product skipped; the rest
// still parse.
func ParseProducts(r io.Reader, currency string) (*ProductsFile, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	h := newHeader(header)
	if err := h.require("Handle", "Title", "Variant Price"); err != nil {
		return nil, err
	}

	out := &ProductsFile{}
	byHandle := map[string]*ImportProduct{}
	bad := map[string]string{}
	var order []string
	line := 1
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: %v", line, err))
			continue
		}
		out.Rows++
		handle := h.get(rec, "Handle")
		if handle == "" {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: row has no Handle", line))
			continue
		}
		p, seen := byHandle[handle]
		if !seen {
			p = &ImportProduct{Line: line, Handle: handle}
			byHandle[handle] = p
			order = append(order, handle)
			p.Title = h.get(rec, "Title")
			p.BodyHTML = h.raw(rec, "Body (HTML)")
			p.Vendor = h.get(rec, "Vendor")
			p.Type = h.get(rec, "Type")
			p.Tags = h.get(rec, "Tags")
			p.ShopifyStatus = strings.ToLower(h.get(rec, "Status"))
			if p.ShopifyStatus == "" {
				p.ShopifyStatus = "draft"
				p.Warnings = append(p.Warnings, "no Status value, treated as draft")
			}
			p.Status = shopStatus(p.ShopifyStatus)
			slug, ok := shop.NormalizeSlug(handle)
			if !ok {
				bad[handle] = fmt.Sprintf("line %d: handle %q cannot be used as a slug", line, handle)
			}
			p.Slug = slug
			if p.Title == "" {
				bad[handle] = fmt.Sprintf("line %d: product %q has no title", line, handle)
			}
		}
		if _, isBad := bad[handle]; isBad {
			continue
		}

		// A variant row: the first row of a product always is one, later rows
		// are when they carry a price, an option value or a SKU (an image-only
		// row has none of them).
		opt1 := h.get(rec, "Option1 Value")
		price := h.get(rec, "Variant Price")
		sku := h.get(rec, "Variant SKU")
		if !seen || price != "" || opt1 != "" || sku != "" {
			v, err := parseVariant(h, rec, currency, line, int32(len(p.Variants))) //nolint:gosec // bounded by the variant cap below
			if err != nil {
				bad[handle] = fmt.Sprintf("line %d: %v", line, err)
				continue
			}
			p.Variants = append(p.Variants, v)
		}
		if src := h.get(rec, "Image Src"); src != "" {
			pos := int32(len(p.Images) + 1) //nolint:gosec // a product has few pictures
			if n, err := strconv.Atoi(h.get(rec, "Image Position")); err == nil && n > 0 && n < 10000 {
				pos = int32(n) //nolint:gosec // bounded above
			}
			dup := false
			for _, im := range p.Images {
				if im.Src == src {
					dup = true
				}
			}
			if !dup {
				p.Images = append(p.Images, ImportImage{Src: src, Position: pos, Alt: h.get(rec, "Image Alt Text")})
			}
		}
	}

	for i, handle := range order {
		if reason, isBad := bad[handle]; isBad {
			out.Problems = append(out.Problems, reason)
			continue
		}
		p := byHandle[handle]
		p.SortOrder = int32(i) //nolint:gosec // a CSV has far fewer than 2^31 products
		if len(p.Variants) > shop.MaxVariants {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: product %q has %d variants, the shop allows %d", p.Line, handle, len(p.Variants), shop.MaxVariants))
			continue
		}
		out.Products = append(out.Products, *p)
	}
	return out, nil
}

func parseVariant(h csvHeader, rec []string, currency string, line int, position int32) (ImportVariant, error) {
	v := ImportVariant{Line: line, Position: position}
	price, err := ParseMinor(h.get(rec, "Variant Price"), currency)
	switch {
	case errors.Is(err, errEmptyMoney):
		price = 0 // a variant row with no price: free until the organizer sets one
	case err != nil:
		return v, fmt.Errorf("Variant Price: %w", err)
	}
	v.PriceMinor = price
	if raw := h.get(rec, "Variant Compare At Price"); raw != "" {
		c, err := ParseMinor(raw, currency)
		if err != nil {
			return v, fmt.Errorf("Variant Compare At Price: %w", err)
		}
		// A compare-at price that is not above the price is not a discount.
		if c > price {
			v.CompareAtMinor = &c
		}
	}
	for i, name := range []string{"Option1 Value", "Option2 Value", "Option3 Value"} {
		val := h.get(rec, name)
		if val == "" {
			continue
		}
		switch i {
		case 0:
			v.Option1 = &val
		case 1:
			v.Option2 = &val
		default:
			v.Option3 = &val
		}
	}
	var parts []string
	for _, o := range []*string{v.Option1, v.Option2, v.Option3} {
		if o != nil {
			parts = append(parts, *o)
		}
	}
	v.Title = strings.Join(parts, " / ")
	if v.Title == "" {
		v.Title = shop.DefaultVariantTitle
	}
	v.SKU = h.get(rec, "Variant SKU")
	if q := h.get(rec, "Variant Inventory Qty"); q != "" {
		n, err := strconv.ParseInt(q, 10, 32)
		if err != nil {
			return v, fmt.Errorf("Variant Inventory Qty %q is not a whole number", q)
		}
		if n < 0 {
			n = 0
		}
		v.Stock = int32(n) //nolint:gosec // parsed with bitSize 32
	}
	// Stock is counted only when Shopify counted it and refused to oversell;
	// "continue" means "keep selling at zero", which the shop expresses as an
	// untracked variant.
	v.TrackStock = h.get(rec, "Variant Inventory Tracker") != "" &&
		!strings.EqualFold(h.get(rec, "Variant Inventory Policy"), "continue")
	return v, nil
}

// SelectProducts keeps what the run imports: active products by default,
// every status with includeDrafts. The second result counts the skipped
// products by Shopify status.
func SelectProducts(all []ImportProduct, includeDrafts bool) (keep []ImportProduct, skipped map[string]int) {
	skipped = map[string]int{}
	for _, p := range all {
		if p.ShopifyStatus == "active" || includeDrafts {
			keep = append(keep, p)
			continue
		}
		skipped[p.ShopifyStatus]++
	}
	return keep, skipped
}
