package httpserver

// scanner_pair_page.go — the public pairing page of the ticket-scanner app and
// the two association files that let a phone open the app from the pairing
// link (spec 69 A.1). The page holds no state and changes nothing: the pairing
// code stays in the URL fragment (never sent to the server) and is spent only
// by POST /v1/scanner-app/pair.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/http"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

const scanPairPath = "/scan/pair"

// scanPairCSS is inline; the CSP allows it by hash, so no 'unsafe-inline'.
const scanPairCSS = `body{font-family:system-ui,sans-serif;margin:0;padding:24px;max-width:480px;margin:0 auto;color:#111}` +
	`h1{font-size:22px}.code{font-size:40px;letter-spacing:6px;font-weight:700;font-family:ui-monospace,monospace;margin:16px 0;word-break:break-all}` +
	`a.btn,button{display:block;box-sizing:border-box;width:100%;padding:14px;margin:10px 0;border:0;border-radius:8px;background:#1a56db;color:#fff;font-size:17px;text-align:center;text-decoration:none}` +
	`a.alt{background:#eef1f6;color:#111}[hidden]{display:none}`

// scanPairScript is served from /scan/pair.js (script-src 'self').
const scanPairScript = `(function(){
var d=document,h=d.documentElement.dataset;
var code=(location.hash||"").replace(/^#/,"").replace(/[^A-Za-z0-9]/g,"").toUpperCase().slice(0,16);
var codeEl=d.getElementById("code"),copy=d.getElementById("copy"),app=d.getElementById("open"),nocode=d.getElementById("nocode");
if(code){codeEl.textContent=code;codeEl.hidden=false;copy.hidden=false;}else{nocode.hidden=false;}
copy.addEventListener("click",function(){
var done=function(){copy.textContent=h.copied;};
if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(code).then(done,function(){});}
else{var r=d.createRange();r.selectNodeContents(codeEl);var s=getSelection();s.removeAllRanges();s.addRange(r);}
});
if(/Android/i.test(navigator.userAgent)){
app.href="intent://"+location.host+"/scan/pair#Intent;scheme=https;package="+h.pkg+
";S.browser_fallback_url="+encodeURIComponent(h.play)+(code?";S.code="+encodeURIComponent(code):"")+";end";
app.hidden=false;
}
})();
`

type scanPairTexts struct {
	lang, title, intro, open, copy, copied, nocode, android, ios string
}

var scanPairTexts_en = scanPairTexts{
	lang: "en", title: "Pair the scanner app",
	intro:   "Open the scanner app and enter this code, or tap the button below.",
	open:    "Open the app",
	copy:    "Copy the code",
	copied:  "Copied",
	nocode:  "No pairing code in this link. Ask the organizer for a new link.",
	android: "Get it on Google Play",
	ios:     "Get it on the App Store",
}

var scanPairTexts_ru = scanPairTexts{
	lang: "ru", title: "Привязка приложения контроля",
	intro:   "Откройте приложение контроля и введите этот код или нажмите кнопку ниже.",
	open:    "Открыть приложение",
	copy:    "Скопировать код",
	copied:  "Скопировано",
	nocode:  "В этой ссылке нет кода привязки. Попросите организатора прислать новую ссылку.",
	android: "Скачать в Google Play",
	ios:     "Скачать в App Store",
}

// scanPairLang picks ru or en from Accept-Language: the first listed tag the
// page speaks wins, anything else is English.
func scanPairLang(header string) scanPairTexts {
	for _, part := range strings.Split(header, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "ru" || strings.HasPrefix(tag, "ru-"):
			return scanPairTexts_ru
		case tag == "en" || strings.HasPrefix(tag, "en-"):
			return scanPairTexts_en
		}
	}
	return scanPairTexts_en
}

func renderScanPairPage(t scanPairTexts) []byte {
	e := html.EscapeString
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="` + t.lang + `" data-pkg="` + scannerapp.AndroidPackage +
		`" data-play="` + e(scannerapp.AndroidURL) + `" data-copied="` + e(t.copied) + `">`)
	b.WriteString(`<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<meta name="robots" content="noindex"><title>` + e(t.title) + `</title><style>` + scanPairCSS + `</style></head><body>`)
	b.WriteString(`<h1>` + e(t.title) + `</h1><p>` + e(t.intro) + `</p>`)
	b.WriteString(`<div id="code" class="code" hidden></div>`)
	b.WriteString(`<button id="copy" type="button" hidden>` + e(t.copy) + `</button>`)
	b.WriteString(`<p id="nocode" hidden>` + e(t.nocode) + `</p>`)
	b.WriteString(`<a id="open" class="btn" href="#" hidden>` + e(t.open) + `</a>`)
	b.WriteString(`<a class="btn alt" href="` + e(scannerapp.AndroidURL) + `">` + e(t.android) + `</a>`)
	b.WriteString(`<a class="btn alt" href="` + e(scannerapp.IOSURL) + `">` + e(t.ios) + `</a>`)
	b.WriteString(`<script src="` + scanPairPath + `.js"></script></body></html>`)
	return []byte(b.String())
}

func scanPairCSP() string {
	sum := sha256.Sum256([]byte(scanPairCSS))
	return "default-src 'none'; script-src 'self'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

func setScanPairHeaders(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Security-Policy", scanPairCSP())
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

func handleScanPairPage(w http.ResponseWriter, r *http.Request) {
	setScanPairHeaders(w, "text/html; charset=utf-8")
	w.Header().Set("Vary", "Accept-Language")
	t := scanPairLang(r.Header.Get("Accept-Language"))
	w.Header().Set("Content-Language", t.lang)
	_, _ = w.Write(renderScanPairPage(t))
}

func handleScanPairScript(w http.ResponseWriter, _ *http.Request) {
	setScanPairHeaders(w, "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(scanPairScript))
}

// appleAppSiteAssociationHandler answers the iOS association file, or the
// standard 404 while SCANNER_IOS_TEAM_ID is empty.
func appleAppSiteAssociationHandler(teamID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if teamID == "" {
			handleNotFound(w, r)
			return
		}
		body := map[string]any{
			"applinks": map[string]any{
				"apps": []string{},
				"details": []any{map[string]any{
					"appIDs":     []string{teamID + "." + scannerapp.AndroidPackage},
					"components": []any{map[string]string{"/": scanPairPath + "*"}},
				}},
			},
		}
		writeScanAssociation(w, body)
	}
}

// assetLinksHandler answers the Android association file, or the standard 404
// while SCANNER_ANDROID_CERT_SHA256 is empty.
func assetLinksHandler(fingerprints []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(fingerprints) == 0 {
			handleNotFound(w, r)
			return
		}
		body := []any{map[string]any{
			"relation": []string{"delegate_permission/common.handle_all_urls"},
			"target": map[string]any{
				"namespace":                "android_app",
				"package_name":             scannerapp.AndroidPackage,
				"sha256_cert_fingerprints": fingerprints,
			},
		}}
		writeScanAssociation(w, body)
	}
}

func writeScanAssociation(w http.ResponseWriter, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

// mountScannerPairRoutes registers the pairing page and the association
// files on the root router. Called from mountOperationalRoutes.
func (s *Server) mountScannerPairRoutes() {
	var team string
	var certs []string
	if s.cfg != nil {
		team = s.cfg.ScannerIOSTeamID
		certs = s.cfg.ScannerAndroidCertSHA256
	}
	s.router.Get(scanPairPath, handleScanPairPage)
	s.router.Get(scanPairPath+".js", handleScanPairScript)
	s.router.Get("/.well-known/apple-app-site-association", appleAppSiteAssociationHandler(team))
	s.router.Get("/.well-known/assetlinks.json", assetLinksHandler(certs))
}
