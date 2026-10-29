package templates

import (
	"strings"
	"testing"
)

func shopTestData() Data {
	return Data{
		RecipientEmail: "buyer@example.com",
		HolderName:     "Anna",
		Branding:       Branding{OrgName: "Vino&Co", LegalName: "Vino Ltd", ContactEmail: "shop@vino.example"},
		Accent:         DefaultAccentColor,
		Shop: &ShopOrderData{
			OrderNumber: "1000777",
			Items: []ShopLine{
				{Title: "Rosé <2024>", Qty: 2, UnitPrice: "60.00 ILS", LineTotal: "120.00 ILS", Kind: ShopKindPhysical},
				{Title: "Wine guide (PDF)", Qty: 1, UnitPrice: "20.00 ILS", LineTotal: "20.00 ILS", Kind: ShopKindDigital},
			},
			Digital:      []ShopLine{{Title: "Wine guide (PDF)", Qty: 1, Kind: ShopKindDigital}},
			Subtotal:     "140.00 ILS",
			Discount:     "10.00 ILS",
			Shipping:     "50.00 ILS",
			Total:        "180.00 ILS",
			Address:      []string{"Dan L", "+972500000000", "Haifa", "Herzl 1", "apt 4", "3100000"},
			Note:         "call before",
			TrackingNote: "Courier ZX-99, tel 03-1234567",
			StatusURL:    "https://shop.vino.example/checkout/abc123",
		},
	}
}

func TestRender_ShopLetters_EveryLocaleCarriesTheOrder(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{TemplateKindShopOrderReceived, TemplateKindShopOrderShipped} {
		for _, loc := range SupportedLocales {
			out, err := r.Render(kind, loc, shopTestData())
			if err != nil {
				t.Fatalf("%s/%s: %v", kind, loc, err)
			}
			if got := r.ResolveLocale(kind, loc); got != loc {
				t.Errorf("%s/%s resolves to %s", kind, loc, got)
			}
			for part, body := range map[string]string{"subject": out.Subject, "html": out.HTMLBody, "text": out.TextBody} {
				if strings.Contains(body, "<no value>") || strings.Contains(body, "{{") || strings.Contains(body, "%!") {
					t.Errorf("%s/%s %s has an unresolved placeholder", kind, loc, part)
				}
			}
			if !strings.Contains(out.Subject, "1000777") {
				t.Errorf("%s/%s: subject %q lacks the order number", kind, loc, out.Subject)
			}
			for part, body := range map[string]string{"html": out.HTMLBody, "text": out.TextBody} {
				for _, want := range []string{"1000777", "180.00 ILS", "140.00 ILS", "50.00 ILS", "10.00 ILS",
					"Herzl 1", "3100000", "Dan L", "call before", "https://shop.vino.example/checkout/abc123", "20.00 ILS"} {
					if !strings.Contains(body, want) {
						t.Errorf("%s/%s %s lacks %q", kind, loc, part, want)
					}
				}
			}
			// The HTML engine escapes what the buyer or organizer typed.
			if !strings.Contains(out.HTMLBody, "Ros&eacute; &lt;2024&gt;") && !strings.Contains(out.HTMLBody, "Rosé &lt;2024&gt;") {
				t.Errorf("%s/%s: the product title is not HTML-escaped", kind, loc)
			}
			if strings.Contains(out.HTMLBody, "<2024>") {
				t.Errorf("%s/%s: a title was printed as markup", kind, loc)
			}
			tracking := strings.Contains(out.TextBody, "ZX-99")
			if tracking != (kind == TemplateKindShopOrderShipped) {
				t.Errorf("%s/%s: the tracking note belongs to the shipped letter only (printed=%v)", kind, loc, tracking)
			}
			filesBlock := strings.Contains(strings.ReplaceAll(out.TextBody, "\r\n", "\n"), "- Wine guide (PDF)\n")
			if filesBlock != (kind == TemplateKindShopOrderReceived) {
				t.Errorf("%s/%s: the files block belongs to the received letter only (printed=%v)", kind, loc, filesBlock)
			}
			if !strings.Contains(out.HTMLBody, `dir="`+map[bool]string{true: "rtl", false: "ltr"}[loc == "he"]+`"`) {
				t.Errorf("%s/%s: wrong text direction", kind, loc)
			}
			if !strings.Contains(out.HTMLBody, "shop@vino.example") {
				t.Errorf("%s/%s: the organizer's contact is missing", kind, loc)
			}
		}
	}
}

func TestRender_ShopReceived_DigitalFilesLinkToTheStatusPage(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := shopTestData()
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindShopOrderReceived, loc, d)
		if err != nil {
			t.Fatal(err)
		}
		link := `<a href="https://shop.vino.example/checkout/abc123" style="color:#4f46e5;">Wine guide (PDF)</a>`
		if !strings.Contains(out.HTMLBody, link) {
			t.Errorf("%s: the digital line must link to the status page, not to a download", loc)
		}
	}
	// "Ваши файлы" is the Russian heading of the block.
	ru, _ := r.Render(TemplateKindShopOrderReceived, "ru", d)
	if !strings.Contains(ru.TextBody, "Ваши файлы") {
		t.Errorf("the Russian letter lacks its files heading:\n%s", ru.TextBody)
	}
}

func TestRender_ShopReceived_OnlyDigitalOrderHasNoShippingBlock(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := shopTestData()
	d.Shop.Items = d.Shop.Items[1:]
	d.Shop.Subtotal, d.Shop.Discount, d.Shop.Shipping, d.Shop.Total = "", "", "", "20.00 ILS"
	d.Shop.Address = nil
	for _, loc := range SupportedLocales {
		out, err := r.Render(TemplateKindShopOrderReceived, loc, d)
		if err != nil {
			t.Fatal(err)
		}
		for _, gone := range []string{"Herzl 1", "50.00 ILS", "3100000"} {
			if strings.Contains(out.HTMLBody, gone) || strings.Contains(out.TextBody, gone) {
				t.Errorf("%s: %q must not appear on an order with nothing to ship", loc, gone)
			}
		}
		if !strings.Contains(out.TextBody, "Wine guide (PDF)") || !strings.Contains(out.TextBody, "20.00 ILS") {
			t.Errorf("%s: the digital line is missing", loc)
		}
	}
	en, _ := r.Render(TemplateKindShopOrderReceived, "en", d)
	if strings.Contains(en.TextBody, "Delivery address") || strings.Contains(en.TextBody, "Shipping") {
		t.Errorf("the English letter still mentions shipping:\n%s", en.TextBody)
	}
}

func TestRender_ShopLetters_PickupAndFallbacks(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	d := shopTestData()
	d.Shop.Pickup, d.Shop.Address = true, nil
	en, err := r.Render(TemplateKindShopOrderShipped, "en", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(en.Subject, "is ready") || !strings.Contains(en.TextBody, "ready for pickup") {
		t.Errorf("a pickup order is announced as ready, got %q", en.Subject)
	}
	// An unknown locale falls back to English; a missing Shop block renders empty.
	pt, err := r.Render(TemplateKindShopOrderReceived, "pt-BR", shopTestData())
	if err != nil || !strings.Contains(pt.Subject, "confirmed") {
		t.Errorf("fallback to English failed: %q %v", pt.Subject, err)
	}
	if _, err := r.Render(TemplateKindShopOrderReceived, "en", Data{}); err != nil {
		t.Errorf("a letter without shop data must still render: %v", err)
	}
}

// shopRefundTestData is the shop order of shopTestData after a PARTIAL refund
// of 50.00 ILS out of 180.00.
func shopRefundTestData() Data {
	d := shopTestData()
	d.Shop.RefundAmount = "50.00 ILS"
	d.Shop.RefundedTotal = "50.00 ILS"
	d.Shop.RefundFull = false
	return d
}

func TestRender_ShopRefunded_PartialAndFullInEveryLocale(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range SupportedLocales {
		if got := r.ResolveLocale(TemplateKindShopOrderRefunded, loc); got != loc {
			t.Errorf("%s resolves to %s", loc, got)
		}
		// Partial, first refund: the amount and the order total, no "in total" row.
		part, err := r.Render(TemplateKindShopOrderRefunded, loc, shopRefundTestData())
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		// Partial, second refund: the running total appears.
		d2 := shopRefundTestData()
		d2.Shop.RefundAmount, d2.Shop.RefundedTotal = "30.00 ILS", "80.00 ILS"
		part2, err := r.Render(TemplateKindShopOrderRefunded, loc, d2)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		// Full: the order is refunded as a whole.
		d3 := shopRefundTestData()
		d3.Shop.RefundAmount, d3.Shop.RefundedTotal, d3.Shop.RefundFull = "100.00 ILS", "180.00 ILS", true
		full, err := r.Render(TemplateKindShopOrderRefunded, loc, d3)
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for name, out := range map[string]Rendered{"partial": part, "partial2": part2, "full": full} {
			for part, body := range map[string]string{"subject": out.Subject, "html": out.HTMLBody, "text": out.TextBody} {
				if strings.Contains(body, "<no value>") || strings.Contains(body, "{{") || strings.Contains(body, "%!") {
					t.Errorf("%s/%s/%s has an unresolved placeholder", loc, name, part)
				}
			}
			if !strings.Contains(out.Subject, "1000777") {
				t.Errorf("%s/%s: subject %q lacks the order number", loc, name, out.Subject)
			}
			for _, body := range []string{out.HTMLBody, out.TextBody} {
				for _, want := range []string{"1000777", "180.00 ILS", "Rosé", "shop@vino.example", "https://shop.vino.example/checkout/abc123"} {
					if !strings.Contains(body, want) {
						t.Errorf("%s/%s lacks %q", loc, name, want)
					}
				}
				// The order is not re-billed: no address, no tracking, no files.
				for _, gone := range []string{"Herzl 1", "ZX-99", "call before"} {
					if strings.Contains(body, gone) {
						t.Errorf("%s/%s must not print %q", loc, name, gone)
					}
				}
			}
			if !strings.Contains(out.HTMLBody, `dir="`+map[bool]string{true: "rtl", false: "ltr"}[loc == "he"]+`"`) {
				t.Errorf("%s/%s: wrong text direction", loc, name)
			}
		}
		for _, b := range []string{part.HTMLBody, part.TextBody} {
			if !strings.Contains(b, "50.00 ILS") {
				t.Errorf("%s partial: the refunded amount is missing", loc)
			}
		}
		// "In total" shows only once it differs from this refund.
		if strings.Count(part.TextBody, "50.00 ILS") != 2 {
			t.Errorf("%s partial: the first refund must name 50.00 only in the sentence and the now row, no running-total row:\n%s", loc, part.TextBody)
		}
		for _, want := range []string{"30.00 ILS", "80.00 ILS"} {
			if !strings.Contains(part2.TextBody, want) || !strings.Contains(part2.HTMLBody, want) {
				t.Errorf("%s second partial lacks %q", loc, want)
			}
		}
		for _, want := range []string{"100.00 ILS", "180.00 ILS"} {
			if !strings.Contains(full.TextBody, want) {
				t.Errorf("%s full lacks %q", loc, want)
			}
		}
		// The full letter differs from the partial one in its subject.
		if full.Subject == part.Subject {
			t.Errorf("%s: full and partial letters share the subject %q", loc, full.Subject)
		}
	}
}

func TestRender_ShopRefunded_EnglishAndRussianWording(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	full := shopRefundTestData()
	full.Shop.RefundAmount, full.Shop.RefundedTotal, full.Shop.RefundFull = "130.00 ILS", "180.00 ILS", true

	en, _ := r.Render(TemplateKindShopOrderRefunded, "en", shopRefundTestData())
	if !strings.HasPrefix(en.Subject, "Refund for order No. 1000777") ||
		!strings.Contains(en.TextBody, "We have refunded 50.00 ILS for order No. 1000777.") ||
		!strings.Contains(en.TextBody, "payment system") || strings.Contains(en.TextBody, "refunded in full") {
		t.Errorf("English partial letter:\n%s\n%s", en.Subject, en.TextBody)
	}
	enFull, _ := r.Render(TemplateKindShopOrderRefunded, "en", full)
	if !strings.HasPrefix(enFull.Subject, "Order No. 1000777 refunded") || !strings.Contains(enFull.TextBody, "has been refunded in full") {
		t.Errorf("English full letter:\n%s\n%s", enFull.Subject, enFull.TextBody)
	}
	ru, _ := r.Render(TemplateKindShopOrderRefunded, "ru", shopRefundTestData())
	if !strings.HasPrefix(ru.Subject, "Возврат по заказу № 1000777") ||
		!strings.Contains(ru.TextBody, "Мы вернули 50.00 ILS по заказу № 1000777.") ||
		!strings.Contains(ru.TextBody, "платёжную систему") || !strings.Contains(ru.TextBody, "Сумма заказа: 180.00 ILS") {
		t.Errorf("Russian partial letter:\n%s\n%s", ru.Subject, ru.TextBody)
	}
	ruFull, _ := r.Render(TemplateKindShopOrderRefunded, "ru", full)
	if !strings.HasPrefix(ruFull.Subject, "Заказ № 1000777 возвращён") || !strings.Contains(ruFull.TextBody, "возвращён полностью") {
		t.Errorf("Russian full letter:\n%s\n%s", ruFull.Subject, ruFull.TextBody)
	}
	// No date is promised: the delay is the bank's.
	for _, b := range []string{en.TextBody, ru.TextBody} {
		if strings.Contains(b, "business day") || strings.Contains(b, "рабочих дн") {
			t.Errorf("the letter promises a date:\n%s", b)
		}
	}
}
