package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/shop"
)

// ImportCustomer is one row of the Shopify Customers CSV.
type ImportCustomer struct {
	Line           int
	ShopifyID      string
	Email          string // lower-cased, "" when absent or not an address
	Phone          string // as exported, leading apostrophe removed
	Name           string
	Region         string // ISO 3166-1 alpha-2 for reading a national phone number
	MarketingEmail bool   // "Accepts Email Marketing" = yes
	// MarketingSMS and MarketingWhatsApp are the same flags of the optional
	// "Accepts SMS Marketing" and "Accepts WhatsApp Marketing" columns.
	MarketingSMS      bool
	MarketingWhatsApp bool
	Warnings          []string
}

// CustomersFile is the parsed Customers CSV.
type CustomersFile struct {
	Customers []ImportCustomer
	Rows      int
	// Problems are rows that cannot be imported (reason with CSV line, never
	// the e-mail or phone itself: the report is meant to be shareable).
	Problems []string
	// Duplicates counts rows whose e-mail already appeared earlier in the file.
	Duplicates int
	// MarketingYes counts imported rows that accept e-mail marketing.
	MarketingYes int
	// MarketingNo counts imported rows that do not (nothing is recorded for
	// them: a "no" is the absence of a consent row).
	MarketingNo int
}

// cleanPhone drops the leading apostrophe Shopify adds so a spreadsheet does
// not turn the number into a formula.
func cleanPhone(raw string) string {
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "'"))
}

// ParseCustomersFile reads a Shopify Customers CSV from disk.
func ParseCustomersFile(path, defaultRegion string) (*CustomersFile, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseCustomers(f, defaultRegion)
}

// ParseCustomers parses the Customers CSV. A row needs an e-mail or a phone;
// the phone is taken from the Phone column, else from the default address.
// defaultRegion reads a national number written without "+" when the row
// names no country.
func ParseCustomers(r io.Reader, defaultRegion string) (*CustomersFile, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	h := newHeader(header)
	if err := h.require("Email", "First Name", "Last Name", "Accepts Email Marketing"); err != nil {
		return nil, err
	}
	region := strings.ToUpper(strings.TrimSpace(defaultRegion))
	out := &CustomersFile{}
	seen := map[string]bool{}
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
		c := ImportCustomer{Line: line, ShopifyID: strings.TrimLeft(h.get(rec, "Customer ID"), "'")}
		c.Name = strings.TrimSpace(h.get(rec, "First Name") + " " + h.get(rec, "Last Name"))
		c.MarketingEmail = strings.EqualFold(h.get(rec, "Accepts Email Marketing"), "yes")
		c.MarketingSMS = strings.EqualFold(h.get(rec, "Accepts SMS Marketing"), "yes")
		c.MarketingWhatsApp = strings.EqualFold(h.get(rec, "Accepts WhatsApp Marketing"), "yes")

		if e := strings.ToLower(h.get(rec, "Email")); e != "" {
			if shop.ValidEmail(e) {
				c.Email = e
			} else {
				c.Warnings = append(c.Warnings, "e-mail is not a valid address, ignored")
			}
		}
		c.Phone = cleanPhone(h.get(rec, "Phone"))
		if c.Phone == "" {
			c.Phone = cleanPhone(h.get(rec, "Default Address Phone"))
		}
		c.Region = region
		if cc := strings.ToUpper(h.get(rec, "Default Address Country Code")); len(cc) == 2 {
			c.Region = cc
		}

		if c.Email == "" && c.Phone == "" {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: no usable e-mail and no phone", line))
			continue
		}
		if c.Email != "" {
			if seen[c.Email] {
				out.Duplicates++
				continue
			}
			seen[c.Email] = true
		}
		if c.MarketingEmail && c.Email == "" {
			c.MarketingEmail = false  // consent to e-mail marketing needs an e-mail
			if len(c.Warnings) == 0 { // an ignored invalid e-mail already said so
				c.Warnings = append(c.Warnings, "e-mail marketing consent without an e-mail, not recorded")
			}
		}
		if (c.MarketingSMS || c.MarketingWhatsApp) && c.Phone == "" {
			c.MarketingSMS, c.MarketingWhatsApp = false, false // those channels need a number
			c.Warnings = append(c.Warnings, "SMS or WhatsApp consent without a phone number, not recorded")
		}
		if c.MarketingEmail {
			out.MarketingYes++
		} else {
			out.MarketingNo++
		}
		out.Customers = append(out.Customers, c)
	}
	return out, nil
}
