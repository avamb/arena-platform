package main

import (
	"strings"
	"testing"
)

// The fixtures are invented: the real export is personal data and is never
// committed.
const customersCSV = "Customer ID,First Name,Last Name,Email,Accepts Email Marketing,Default Address Company,Default Address Address1,Default Address Address2,Default Address City,Default Address Province Code,Default Address Country Code,Default Address Zip,Default Address Phone,Phone,Accepts SMS Marketing,Total Spent,Total Orders,Note,Tax Exempt,Tags,Accepts WhatsApp Marketing\n" +
	"'1001,Дина,Леви,Dina.Levi@Example.com,yes,,,,,,IL,,'0501234567,,no,0.00,0,,no,,no\n" +
	"'1002,Ivan,,ivan@example.com,no,,,,,,CY,,'+35799123456,'+35799123456,no,0.00,0,,no,,no\n" +
	"'1003,,,,no,,,,,,IL,,'0507654321,,no,0.00,0,,no,,no\n" +
	"'1004,,,,yes,,,,,,IL,,,,no,0.00,0,,no,,no\n" +
	"'1005,Dup,Licate,DINA.LEVI@example.com,yes,,,,,,IL,,,,no,0.00,0,,no,,no\n" +
	"'1006,Bad,Mail,not an address,yes,,,,,,,,,'0521112222,no,0.00,0,,no,,no\n"

func TestParseCustomers(t *testing.T) {
	cf, err := ParseCustomers(strings.NewReader(customersCSV), "il")
	if err != nil {
		t.Fatal(err)
	}
	if cf.Rows != 6 {
		t.Errorf("rows %d", cf.Rows)
	}
	if len(cf.Customers) != 4 || cf.Duplicates != 1 || len(cf.Problems) != 1 {
		t.Fatalf("customers=%d duplicates=%d problems=%v", len(cf.Customers), cf.Duplicates, cf.Problems)
	}
	dina := cf.Customers[0]
	if dina.Email != "dina.levi@example.com" || dina.Name != "Дина Леви" || !dina.MarketingEmail {
		t.Errorf("first customer: %+v", dina)
	}
	// The default address phone is used when Phone is empty, the apostrophe
	// Shopify adds in front is removed, the region follows the address.
	if dina.Phone != "0501234567" || dina.Region != "IL" || dina.ShopifyID != "1001" {
		t.Errorf("phone/region/id: %+v", dina)
	}
	if ivan := cf.Customers[1]; ivan.Phone != "+35799123456" || ivan.Region != "CY" || ivan.Name != "Ivan" || ivan.MarketingEmail {
		t.Errorf("second customer: %+v", ivan)
	}
	// A phone-only row is kept; consent to e-mail marketing needs an e-mail.
	if p := cf.Customers[2]; p.Email != "" || p.Phone != "0507654321" {
		t.Errorf("phone-only customer: %+v", p)
	}
	// An invalid e-mail with a phone is imported by phone and flagged.
	bad := cf.Customers[3]
	if bad.Email != "" || bad.Phone != "0521112222" || len(bad.Warnings) != 1 || bad.MarketingEmail {
		t.Errorf("invalid e-mail customer: %+v", bad)
	}
	if cf.MarketingYes != 1 {
		t.Errorf("marketing yes %d (only rows with an e-mail count)", cf.MarketingYes)
	}
	for _, p := range cf.Problems {
		if strings.Contains(p, "@") {
			t.Errorf("a problem line leaks an address: %q", p)
		}
	}
}

func TestParseCustomers_RejectsForeignCSV(t *testing.T) {
	if _, err := ParseCustomers(strings.NewReader("a,b\n1,2\n"), "IL"); err == nil {
		t.Fatal("want an error")
	}
}

const ordersCSV = "Name,Email,Financial Status,Paid at,Fulfillment Status,Currency,Subtotal,Shipping,Taxes,Total,Discount Code,Discount Amount,Created at,Lineitem quantity,Lineitem name,Lineitem price,Lineitem sku,Cancelled at,Refunded Amount\n" +
	"#1001,a@example.com,paid,2025-01-01,fulfilled,ILS,400.00,50.00,0.00,450.00,,0.00,2025-01-01,2,Wine A,200.00,SKU1,,0.00\n" +
	"#1001,,,,,,,,,,,,,1,Wine B,100.00,,,\n" +
	"#1002,,refunded,2025-01-02,fulfilled,ILS,200.00,50.00,0.00,250.00,CODE,0.00,2025-01-02,1,Wine A,200.00,SKU1,,250.00\n" +
	"#1003,c@example.com,paid,2025-01-03,unfulfilled,ILS,180.00,50.00,0.00,200.00,NY,30.00,2025-01-03,1,Wine C,180.00,,2025-01-04,0.00\n"

func TestParseOrders_GroupsLinesAndSums(t *testing.T) {
	of, err := ParseOrders(strings.NewReader(ordersCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(of.Orders) != 3 || len(of.Problems) != 0 {
		t.Fatalf("orders=%d problems=%v", len(of.Orders), of.Problems)
	}
	s := SummarizeOrders(of)
	if s.Orders != 3 || s.Lines != 4 || s.WithoutEmail != 1 || s.Cancelled != 1 {
		t.Errorf("summary: %+v", s)
	}
	ils := s.ByCurrency["ILS"]
	if ils == nil || ils.Total != 90000 || ils.Shipping != 15000 || ils.Discount != 3000 || ils.Refunded != 25000 || ils.Subtotal != 78000 {
		t.Errorf("ILS totals: %+v", ils)
	}
	if s.ByState["paid"] != 2 || s.ByState["refunded"] != 1 {
		t.Errorf("states: %v", s.ByState)
	}
}

func TestParseOrders_ContinuationRowsRepeatingTheName(t *testing.T) {
	csv := "Name,Email,Financial Status,Currency,Subtotal,Shipping,Total,Discount Amount,Refunded Amount,Lineitem quantity,Lineitem name,Lineitem price\n" +
		"#5,a@example.com,paid,ILS,300.00,0.00,300.00,0.00,0.00,1,A,100.00\n" +
		"#5,,,,,,,,,2,B,100.00\n"
	of, err := ParseOrders(strings.NewReader(csv))
	if err != nil || len(of.Problems) != 0 || len(of.Orders) != 1 {
		t.Fatalf("%v %v", err, of)
	}
	if o := of.Orders[0]; len(o.Lines) != 2 || o.Lines[1].Qty != 2 || o.TotalMinor != 30000 {
		t.Errorf("%+v", o)
	}
}
