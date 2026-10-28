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
