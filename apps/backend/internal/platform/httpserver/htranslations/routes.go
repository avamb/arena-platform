package htranslations

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// TranslationItem is one translation in the answers.
type TranslationItem struct {
	Locale           string  `json:"locale"`
	Name             *string `json:"name"`
	ShortDescription *string `json:"short_description"`
	DescriptionHTML  *string `json:"description_html"`
	ReportTextHTML   *string `json:"report_text_html"`
	Source           string  `json:"source"`
	TranslationStale bool    `json:"translation_stale"`
	UpdatedAt        string  `json:"updated_at"`
	UpdatedBy        *string `json:"updated_by"`
}

func item(t contenttranslate.Translation, currentHash string) TranslationItem {
	it := TranslationItem{
		Locale: t.Locale, Name: t.Fields.Name, ShortDescription: t.Fields.ShortDescription,
		DescriptionHTML: t.Fields.DescriptionHTML, ReportTextHTML: t.Fields.ReportTextHTML,
		Source: t.Source, TranslationStale: contenttranslate.Stale(t, currentHash), UpdatedAt: rfc3339(t.UpdatedAt),
	}
	if t.UpdatedBy != nil {
		s := t.UpdatedBy.String()
		it.UpdatedBy = &s
	}
	return it
}

// OriginalFields is the original content of the event.
type OriginalFields struct {
	Name             string  `json:"name"`
	ShortDescription *string `json:"short_description"`
	DescriptionHTML  *string `json:"description_html"`
	ReportTextHTML   *string `json:"report_text_html"`
}

// ListResponse is the answer of GET .../translations.
type ListResponse struct {
	ContentLocale    string            `json:"content_locale"`
	Original         OriginalFields    `json:"original"`
	SourceHash       string            `json:"source_hash"`
	MachineAvailable bool              `json:"machine_available"`
	SupportedLocales []string          `json:"supported_locales"`
	SiteLocales      []string          `json:"site_locales"`
	Translations     []TranslationItem `json:"translations"`
}

// siteLocales is the distinct union of the sales channels' settings.site.locales
// of the organization, restricted to the supported locales.
func (h *Handler) siteLocales(r *http.Request, orgID uuid.UUID) ([]string, error) {
	rows, err := h.pool.Query(r.Context(), `
SELECT DISTINCT l
  FROM sales_channels c,
       jsonb_array_elements_text(CASE WHEN jsonb_typeof(c.settings->'site'->'locales') = 'array'
                                      THEN c.settings->'site'->'locales' ELSE '[]'::jsonb END) AS l
 WHERE c.org_id = $1 AND c.deleted_at IS NULL
 ORDER BY l`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		if n, ok := contenttranslate.NormalizeLocale(l); ok && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// HandleList serves GET .../translations.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	orgID, orig, ok := h.scope(w, r)
	if !ok {
		return
	}
	rows, err := contenttranslate.List(r.Context(), h.pool, contenttranslate.OwnerEvent, orig.EventID)
	if err != nil {
		h.failed(w, r, "list", err)
		return
	}
	site, err := h.siteLocales(r, orgID)
	if err != nil {
		h.failed(w, r, "site locales", err)
		return
	}
	f := orig.Fields()
	hash := orig.Hash()
	items := make([]TranslationItem, 0, len(rows))
	for _, t := range rows {
		items = append(items, item(t, hash))
	}
	httputil.WriteJSON(w, http.StatusOK, ListResponse{
		ContentLocale: orig.ContentLocale,
		Original: OriginalFields{
			Name: orig.Name, ShortDescription: f.ShortDescription, DescriptionHTML: f.DescriptionHTML, ReportTextHTML: f.ReportTextHTML,
		},
		SourceHash: hash, MachineAvailable: h.machine,
		SupportedLocales: contenttranslate.Supported, SiteLocales: site, Translations: items,
	})
}

type putRequest struct {
	Name             *string `json:"name"`
	ShortDescription *string `json:"short_description"`
	DescriptionHTML  *string `json:"description_html"`
	ReportTextHTML   *string `json:"report_text_html"`
}

// HandlePut serves PUT .../translations/{locale}: a hand-made translation that
// replaces the row of the locale as a whole.
func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {
	orgID, orig, ok := h.scope(w, r)
	if !ok {
		return
	}
	locale, ok := pathLocale(r, orig.ContentLocale)
	if !ok {
		invalidLocale(w, r, "the locale must be one of the supported locales and differ from the language of the original")
		return
	}
	var req putRequest
	if !readBody(w, r, &req) {
		return
	}
	ctx := r.Context()
	var saved contenttranslate.Translation
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		saved, err = contenttranslate.PutManual(ctx, tx, contenttranslate.Translation{
			OwnerType: contenttranslate.OwnerEvent, OwnerID: orig.EventID, OrgID: orgID, Locale: locale,
			Fields: contenttranslate.Fields{
				Name: req.Name, ShortDescription: req.ShortDescription,
				DescriptionHTML: req.DescriptionHTML, ReportTextHTML: req.ReportTextHTML,
			},
			SourceHash: orig.Hash(), UpdatedBy: callerUserID(ctx),
		})
		if err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.translation.put", orig.EventID, map[string]any{"locale": locale})
	})
	var fe *contenttranslate.FieldError
	if errors.As(err, &fe) {
		fieldRefused(w, r, fe)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// The upsert is guarded by org_id: the row belongs to another organization.
		notFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "save", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, item(saved, orig.Hash()))
}

// HandleDelete serves DELETE .../translations/{locale}.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	orgID, orig, ok := h.scope(w, r)
	if !ok {
		return
	}
	locale, ok := contenttranslate.NormalizeLocale(pathLocaleRaw(r))
	if !ok {
		translationNotFound(w, r)
		return
	}
	ctx := r.Context()
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		removed, err := contenttranslate.Delete(ctx, tx, orgID, contenttranslate.OwnerEvent, orig.EventID, locale)
		if err != nil {
			return err
		}
		if !removed {
			return pgx.ErrNoRows
		}
		return h.writeAudit(ctx, tx, r, "v1.event.translation.delete", orig.EventID, map[string]any{"locale": locale})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		translationNotFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func translationNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"translation.not_found", "this event has no translation for that locale", r))
}

type machineRequest struct {
	Locales []string `json:"locales"`
}

// MachineResponse is the answer of POST .../translations/machine.
type MachineResponse struct {
	JobID   string   `json:"job_id"`
	Locales []string `json:"locales"`
}

// HandleMachine serves POST .../translations/machine: it queues the job
// content.translate. The job never touches a manual translation.
func (h *Handler) HandleMachine(w http.ResponseWriter, r *http.Request) {
	orgID, orig, ok := h.scope(w, r)
	if !ok {
		return
	}
	if !h.machine {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"translation.not_configured", "machine translation is not switched on for this platform", r))
		return
	}
	var req machineRequest
	if !readBody(w, r, &req) {
		return
	}
	if len(req.Locales) == 0 || len(req.Locales) > len(contenttranslate.Supported) {
		invalidLocale(w, r, "locales must list at least one supported locale")
		return
	}
	seen := map[string]bool{}
	locales := make([]string, 0, len(req.Locales))
	for _, raw := range req.Locales {
		loc, ok := contenttranslate.NormalizeLocale(raw)
		if !ok || loc == orig.ContentLocale {
			invalidLocale(w, r, "every locale must be supported and differ from the language of the original: "+raw)
			return
		}
		if !seen[loc] {
			seen[loc] = true
			locales = append(locales, loc)
		}
	}
	ctx := r.Context()
	var jobID string
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		jobID, err = contenttranslate.Enqueue(ctx, tx, contenttranslate.Payload{
			OwnerType: contenttranslate.OwnerEvent, OwnerID: orig.EventID, OrgID: orgID,
			Locales: locales, RequestedBy: callerUserID(ctx),
		})
		if err != nil {
			return err
		}
		return h.writeAudit(ctx, tx, r, "v1.event.translation.machine_requested", orig.EventID, map[string]any{"locales": locales})
	})
	if err != nil {
		h.failed(w, r, "enqueue", err)
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, MachineResponse{JobID: jobID, Locales: locales})
}

type reportTextRequest struct {
	ReportTextHTML *string `json:"report_text_html"`
}

// ReportTextResponse is the answer of PUT .../report-text.
type ReportTextResponse struct {
	ReportTextHTML *string `json:"report_text_html"`
}

// HandlePutReportText serves PUT .../report-text: the photo-report text of the
// original event. A null or blank text clears it.
func (h *Handler) HandlePutReportText(w http.ResponseWriter, r *http.Request) {
	orgID, orig, ok := h.scope(w, r)
	if !ok {
		return
	}
	var req reportTextRequest
	if !readBody(w, r, &req) {
		return
	}
	ctx := r.Context()
	var stored *string
	err := h.inTx(ctx, func(tx pgx.Tx) error {
		if err := contenttranslate.SetEventReportText(ctx, tx, orgID, orig.EventID, req.ReportTextHTML); err != nil {
			return err
		}
		o, err := contenttranslate.LoadEventOriginal(ctx, tx, orgID, orig.EventID)
		if err != nil {
			return err
		}
		stored = o.ReportTextHTML
		return h.writeAudit(ctx, tx, r, "v1.event.report_text.set", orig.EventID, nil)
	})
	var fe *contenttranslate.FieldError
	if errors.As(err, &fe) {
		fieldRefused(w, r, fe)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r)
		return
	}
	if err != nil {
		h.failed(w, r, "save report text", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, ReportTextResponse{ReportTextHTML: stored})
}
