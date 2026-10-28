//go:build integration

package httpserver

// shop_image_dims_integration_test.go — a picture uploaded through POST
// /v1/media records its pixel size, and the shop's product answers (org and
// storefront) carry it so a card can reserve the picture's aspect.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShop_PictureCarriesItsSize(t *testing.T) {
	f := newShopFixture(t)
	productID, _ := f.openShop(4500, 3)

	var pic bytes.Buffer
	if err := png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 300, 200))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("owner_type", "shop_product")
	_ = mw.WriteField("org_id", f.orgA.String())
	fw, err := mw.CreateFormFile("file", "wine.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(pic.Bytes())
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/media", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+f.ownerA)
	rec := httptest.NewRecorder()
	f.srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/media = %d %s", rec.Code, rec.Body.String())
	}
	var up struct {
		MediaObject struct {
			ID     string `json:"id"`
			Width  *int   `json:"width"`
			Height *int   `json:"height"`
		} `json:"media_object"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	if up.MediaObject.Width == nil || *up.MediaObject.Width != 300 || up.MediaObject.Height == nil || *up.MediaObject.Height != 200 {
		t.Fatalf("uploaded media size = %v x %v; want 300 x 200 (%s)", up.MediaObject.Width, up.MediaObject.Height, rec.Body.String())
	}

	code, out, _ := f.do(http.MethodPost, f.orgPath(f.orgA, "/products/"+productID+"/images"), f.ownerA,
		fmt.Sprintf(`{"media_id":%q,"alt":"bottle"}`, up.MediaObject.ID))
	if code != http.StatusOK {
		t.Fatalf("add image = %d %v", code, out)
	}
	img := out["images"].([]any)[0].(map[string]any)
	if img["width"] != float64(300) || img["height"] != float64(200) {
		t.Fatalf("org image = %v; want width 300 height 200", img)
	}
	code, out, _ = f.do(http.MethodGet, fmt.Sprintf("/v1/public/shop/%s/products/%s", f.orgASlug, out["slug"]), "", "")
	if code != http.StatusOK {
		t.Fatalf("public product = %d %v", code, out)
	}
	pub := out["product"].(map[string]any)["images"].([]any)[0].(map[string]any)
	if pub["width"] != float64(300) || pub["height"] != float64(200) {
		t.Fatalf("public image = %v; want width 300 height 200", pub)
	}
	// A picture whose size was never recorded leaves the fields out.
	f.exec(`UPDATE media_objects SET width = NULL, height = NULL WHERE id = $1`, up.MediaObject.ID)
	_, out, _ = f.do(http.MethodGet, f.orgPath(f.orgA, "/products/"+productID), f.ownerA, "")
	if _, ok := out["images"].([]any)[0].(map[string]any)["width"]; ok {
		t.Fatalf("an unknown size is still shown: %v", out["images"])
	}
}
