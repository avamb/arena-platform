package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ImportOrderLine is one line item of an order.
type ImportOrderLine struct {
	Name       string
	SKU        string
	Qty        int
	PriceMinor int64
}

// ImportOrder is one Shopify order (all CSV rows of one Name). The order
// level fields sit on its first row, the continuation rows only add lines.
type ImportOrder struct {
	Line           int
	Name           string
	Currency       string
	FinancialState string // paid, refunded, partially_refunded, pending, ...
	Fulfillment    string
	SubtotalMinor  int64
	ShippingMinor  int64
	DiscountMinor  int64
	TotalMinor     int64
	RefundedMinor  int64
	Cancelled      bool
	HasEmail       bool
	Lines          []ImportOrderLine
}

// OrdersFile is the parsed Orders CSV.
type OrdersFile struct {
	Orders   []ImportOrder
	Rows     int
	Problems []string
}

// ParseOrdersFile reads a Shopify Orders CSV from disk.
func ParseOrdersFile(path string) (*OrdersFile, error) {
	f, err := os.Open(path) //nolint:gosec // operator-controlled path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseOrders(f)
}

// ParseOrders parses the Orders CSV. The second wave of the import (shop
// orders with source "shopify") is not written yet: the parse feeds the
// dry-run totals so the operator can check them against Shopify's own.
func ParseOrders(r io.Reader) (*OrdersFile, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	h := newHeader(header)
	if err := h.require("Name", "Financial Status", "Total", "Lineitem name"); err != nil {
		return nil, err
	}
	out := &OrdersFile{}
	byName := map[string]*ImportOrder{}
	var order []string
	bad := map[string]string{}
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
		name := h.get(rec, "Name")
		if name == "" {
			out.Problems = append(out.Problems, fmt.Sprintf("line %d: row has no order Name", line))
			continue
		}
		o, seen := byName[name]
		if !seen {
			o = &ImportOrder{Line: line, Name: name}
			byName[name] = o
			order = append(order, name)
			o.Currency = strings.ToUpper(h.get(rec, "Currency"))
			o.FinancialState = strings.ToLower(h.get(rec, "Financial Status"))
			o.Fulfillment = strings.ToLower(h.get(rec, "Fulfillment Status"))
			o.HasEmail = h.get(rec, "Email") != ""
			o.Cancelled = h.get(rec, "Cancelled at") != ""
			cur := o.Currency
			if cur == "" {
				cur = "ILS"
			}
			for _, f := range []struct {
				col string
				dst *int64
			}{
				{"Subtotal", &o.SubtotalMinor}, {"Shipping", &o.ShippingMinor}, {"Discount Amount", &o.DiscountMinor},
				{"Total", &o.TotalMinor}, {"Refunded Amount", &o.RefundedMinor},
			} {
				v, err := ParseMinor(h.get(rec, f.col), cur)
				if err != nil && !errors.Is(err, errEmptyMoney) {
					bad[name] = fmt.Sprintf("line %d: %s: %v", line, f.col, err)
					break
				}
				*f.dst = v
			}
		}
		if _, isBad := bad[name]; isBad {
			continue
		}
		if ln := h.get(rec, "Lineitem name"); ln != "" {
			qty, _ := strconv.Atoi(h.get(rec, "Lineitem quantity"))
			price, err := ParseMinor(h.get(rec, "Lineitem price"), orderCurrency(o))
			if err != nil && !errors.Is(err, errEmptyMoney) {
				bad[name] = fmt.Sprintf("line %d: Lineitem price: %v", line, err)
				continue
			}
			o.Lines = append(o.Lines, ImportOrderLine{Name: ln, SKU: h.get(rec, "Lineitem sku"), Qty: qty, PriceMinor: price})
		}
	}
	for _, name := range order {
		if reason, isBad := bad[name]; isBad {
			out.Problems = append(out.Problems, reason)
			continue
		}
		out.Orders = append(out.Orders, *byName[name])
	}
	return out, nil
}

func orderCurrency(o *ImportOrder) string {
	if o.Currency == "" {
		return "ILS"
	}
	return o.Currency
}

// OrdersSummary is the dry-run view of the orders file: counts and money,
// nothing personal.
type OrdersSummary struct {
	Orders        int
	Lines         int
	WithoutEmail  int
	Cancelled     int
	ByState       map[string]int
	ByCurrency    map[string]*OrderTotals
	TotalProblems int
}

// OrderTotals are the money sums of one currency, in minor units.
type OrderTotals struct {
	Total, Subtotal, Shipping, Discount, Refunded int64
}

// SummarizeOrders adds the orders up.
func SummarizeOrders(f *OrdersFile) OrdersSummary {
	s := OrdersSummary{ByState: map[string]int{}, ByCurrency: map[string]*OrderTotals{}, TotalProblems: len(f.Problems)}
	for _, o := range f.Orders {
		s.Orders++
		s.Lines += len(o.Lines)
		if !o.HasEmail {
			s.WithoutEmail++
		}
		if o.Cancelled {
			s.Cancelled++
		}
		state := o.FinancialState
		if state == "" {
			state = "(none)"
		}
		s.ByState[state]++
		cur := orderCurrency(&o)
		t := s.ByCurrency[cur]
		if t == nil {
			t = &OrderTotals{}
			s.ByCurrency[cur] = t
		}
		t.Total += o.TotalMinor
		t.Subtotal += o.SubtotalMinor
		t.Shipping += o.ShippingMinor
		t.Discount += o.DiscountMinor
		t.Refunded += o.RefundedMinor
	}
	return s
}

// sortedKeys returns map keys in order (stable reports).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
