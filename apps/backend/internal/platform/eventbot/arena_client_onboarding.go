package eventbot

// arena_client_onboarding.go — the bot's calls of the organizer application
// flow (/v1/bot/onboarding/*). They carry BOT_SERVICE_TOKEN and name the
// Telegram account in every call; the bot keeps no answers of its own.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// OnbSchemaOption is one choice of a select field.
type OnbSchemaOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OnbSchemaField is one question of the form.
type OnbSchemaField struct {
	Key         string            `json:"key"`
	Type        string            `json:"type"`
	Label       string            `json:"label"`
	Hint        string            `json:"hint"`
	Required    bool              `json:"required"`
	ReadOnly    bool              `json:"read_only"`
	DefaultFrom string            `json:"default_from"`
	Options     []OnbSchemaOption `json:"options"`
}

// OnbSchemaStep is one page of the form.
type OnbSchemaStep struct {
	Key    string           `json:"key"`
	Title  string           `json:"title"`
	Fields []OnbSchemaField `json:"fields"`
}

// OnbSchema is the form definition the bot walks.
type OnbSchema struct {
	Version           int             `json:"version"`
	Steps             []OnbSchemaStep `json:"steps"`
	AcceptedCountries []string        `json:"accepted_countries"`
}

// Field returns the field with the given key.
func (s OnbSchema) Field(key string) (OnbSchemaField, bool) {
	for _, st := range s.Steps {
		for _, f := range st.Fields {
			if f.Key == key {
				return f, true
			}
		}
	}
	return OnbSchemaField{}, false
}

// OnbForm is the schema plus the website's legal pages.
type OnbForm struct {
	Schema     OnbSchema `json:"schema"`
	TermsURL   string    `json:"terms_url"`
	PrivacyURL string    `json:"privacy_url"`
}

// OnbApplication is an application as the applicant sees it.
type OnbApplication struct {
	ID                 string         `json:"id"`
	Status             string         `json:"status"`
	Locale             string         `json:"locale"`
	Email              string         `json:"email"`
	EmailConfirmed     bool           `json:"email_confirmed"`
	CurrentStep        string         `json:"current_step"`
	Progress           int            `json:"progress"`
	Answers            map[string]any `json:"answers"`
	MissingFields      []string       `json:"missing_fields"`
	RequestedFields    []string       `json:"requested_fields"`
	InfoRequestMessage string         `json:"info_request_message"`
}

// OnbStartRequest is the bot's first screen.
type OnbStartRequest struct {
	TelegramUserID   int64  `json:"telegram_user_id"`
	TelegramUsername string `json:"telegram_username,omitempty"`
	FirstName        string `json:"first_name"`
	LastName         string `json:"last_name"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	Locale           string `json:"locale,omitempty"`
}

// OnbNotice is a decision to announce to an applicant.
type OnbNotice struct {
	ApplicationID  string `json:"application_id"`
	TelegramUserID int64  `json:"telegram_user_id"`
	Status         string `json:"status"`
	Locale         string `json:"locale"`
	OrgName        string `json:"org_name"`
	Message        string `json:"message"`
}

type onbApplicationEnvelope struct {
	Application OnbApplication `json:"application"`
}

type onbTG struct {
	TelegramUserID int64 `json:"telegram_user_id"`
}

// OnboardingForm reads the form definition in a language.
func (c *ArenaClient) OnboardingForm(ctx context.Context, locale string) (OnbForm, error) {
	var out OnbForm
	err := c.do(ctx, http.MethodGet, "/v1/bot/onboarding/form-schema?locale="+url.QueryEscape(locale), c.serviceToken, nil, &out)
	return out, err
}

// OnboardingCurrent returns the account's open application (404 when none).
func (c *ArenaClient) OnboardingCurrent(ctx context.Context, tg int64) (OnbApplication, error) {
	var out onbApplicationEnvelope
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/bot/onboarding/application?telegram_user_id=%d", tg), c.serviceToken, nil, &out)
	return out.Application, err
}

// OnboardingStart opens an application (or returns the open one).
func (c *ArenaClient) OnboardingStart(ctx context.Context, req OnbStartRequest) (OnbApplication, error) {
	var out onbApplicationEnvelope
	err := c.do(ctx, http.MethodPost, "/v1/bot/onboarding/applications", c.serviceToken, req, &out)
	return out.Application, err
}

// OnboardingSave stores answers.
func (c *ArenaClient) OnboardingSave(ctx context.Context, id string, tg int64, answers map[string]any) (OnbApplication, error) {
	var out onbApplicationEnvelope
	body := struct {
		onbTG
		Answers map[string]any `json:"answers"`
	}{onbTG{tg}, answers}
	err := c.do(ctx, http.MethodPut, "/v1/bot/onboarding/applications/"+id+"/answers", c.serviceToken, body, &out)
	return out.Application, err
}

// OnboardingSendCode e-mails a new confirmation code.
func (c *ArenaClient) OnboardingSendCode(ctx context.Context, id string, tg int64) error {
	return c.do(ctx, http.MethodPost, "/v1/bot/onboarding/applications/"+id+"/email-code", c.serviceToken, onbTG{tg}, nil)
}

// OnboardingConfirmEmail checks the typed code.
func (c *ArenaClient) OnboardingConfirmEmail(ctx context.Context, id string, tg int64, code string) (OnbApplication, error) {
	var out onbApplicationEnvelope
	body := struct {
		onbTG
		Code string `json:"code"`
	}{onbTG{tg}, code}
	err := c.do(ctx, http.MethodPost, "/v1/bot/onboarding/applications/"+id+"/confirm-email", c.serviceToken, body, &out)
	return out.Application, err
}

// OnboardingSubmit hands the application to the operator.
func (c *ArenaClient) OnboardingSubmit(ctx context.Context, id string, tg int64) (OnbApplication, error) {
	var out onbApplicationEnvelope
	err := c.do(ctx, http.MethodPost, "/v1/bot/onboarding/applications/"+id+"/submit", c.serviceToken, onbTG{tg}, &out)
	return out.Application, err
}

// OnboardingSiteLink returns a website link that opens the application.
func (c *ArenaClient) OnboardingSiteLink(ctx context.Context, id string, tg int64) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/onboarding/applications/"+id+"/site-link", c.serviceToken, onbTG{tg}, &out)
	return out.URL, err
}

// OnboardingClaimNotices claims the decisions not yet announced.
func (c *ArenaClient) OnboardingClaimNotices(ctx context.Context) ([]OnbNotice, error) {
	var out struct {
		Notices []OnbNotice `json:"notices"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/bot/onboarding/notifications/claim", c.serviceToken, struct{}{}, &out)
	return out.Notices, err
}
