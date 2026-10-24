package config

import (
	"fmt"
	"strings"
)

// Role names the binary that loads the configuration. Each deployed binary
// declares its own role in code (LoadFor(RoleAPI) in cmd/arena-api, and so
// on), never through the environment, so a container that carries only its
// own variables still starts in production: validation checks what that
// binary reads and nothing else. The rules every binary shares (LOG_FORMAT,
// DATABASE_URL and its TLS mode, the bans on debug flags and on the
// *_API_BASE_URL test seams) apply to every role.
type Role string

const (
	// RoleAPI is arena-api: the HTTP API.
	RoleAPI Role = "api"
	// RoleWorker is arena-worker: background jobs, mail and the outbox.
	RoleWorker Role = "worker"
	// RoleBot is arena-bot: the Telegram event-center bot.
	RoleBot Role = "bot"
	// RoleMigrate is a one-shot database tool: arena-migrate and arena-seed.
	RoleMigrate Role = "migrate"
	// RoleAll checks the rules of every role at once. It is what Load and a
	// Config with an empty Role use; no deployed binary may use it, because it
	// demands every secret of every service
	// (tests/staticanalysis/cmd_config_role_test.go).
	RoleAll Role = "all"
)

// capability is one group of settings that some roles read.
type capability int

const (
	// capJWT: JWT_SIGNING_SECRET / ISSUER / AUDIENCE (api verifies, bot mints).
	capJWT capability = iota
	// capEdge: the public HTTP surface — dev auth, CORS, payment webhook
	// secrets, the Bil24 gateway, the Brevo webhook token.
	capEdge
	// capMail: EMAIL_MODE and SMTP_* (only the worker sends mail).
	capMail
	// capOutbox: OUTBOX_MODE and its webhook settings.
	capOutbox
	// capMedia: MEDIA_* (the api serves media, the worker signs links to it).
	capMedia
	// capPublicURL: APP_PUBLIC_URL, the origin of links in mail and of the
	// api's own absolute URLs.
	capPublicURL
	// capEventsBot: EVENTS_TELEGRAM_BOT_TOKEN.
	capEventsBot
)

var roleCapabilities = map[Role][]capability{
	RoleAPI:     {capJWT, capEdge, capMedia, capPublicURL},
	RoleWorker:  {capMail, capOutbox, capMedia, capPublicURL},
	RoleBot:     {capJWT, capEventsBot},
	RoleMigrate: nil,
}

// Binary returns the name of the binary that runs with the role; it is the
// APP_NAME default.
func (r Role) Binary() string {
	switch r {
	case RoleAPI, RoleAll, "":
		return "arena-api"
	default:
		return "arena-" + string(r)
	}
}

func (r Role) valid() bool {
	if r == RoleAll || r == "" {
		return true
	}
	_, ok := roleCapabilities[r]
	return ok
}

// uses reports whether the configuration's role reads the capability's
// settings. An empty role means RoleAll, which covers every capability of
// the services that shared one environment before the split (api, worker,
// migrate) but not the bot's own token: requiring it there would turn every
// pre-split configuration and test fixture invalid.
func (c *Config) uses(cp capability) bool {
	if c.Role == "" || c.Role == RoleAll {
		return cp != capEventsBot
	}
	for _, have := range roleCapabilities[c.Role] {
		if have == cp {
			return true
		}
	}
	return false
}

// roleSecret is a secret variable together with the roles that read it.
type roleSecret struct {
	name  string
	roles []Role
	value func(*Config) string
}

// roleSecrets is the map of which service needs which secret. It drives the
// "set but unused" startup warning and the table in deploy/DOKPLOY.md
// (section 2.6), keep the two in step.
var roleSecrets = []roleSecret{
	{"JWT_SIGNING_SECRET", []Role{RoleAPI, RoleBot}, func(c *Config) string { return c.JWTSecretStub }},
	{"STRIPE_WEBHOOK_SECRET", []Role{RoleAPI}, func(c *Config) string { return c.StripeWebhookSecret }},
	{"ALLPAY_WEBHOOK_SECRET", []Role{RoleAPI}, func(c *Config) string { return c.AllPayWebhookSecret }},
	{"BREVO_WEBHOOK_TOKEN", []Role{RoleAPI}, func(c *Config) string { return c.BrevoWebhookToken }},
	{"BREVO_WEBHOOK_TOKEN_PREVIOUS", []Role{RoleAPI}, func(c *Config) string { return c.BrevoWebhookTokenPrevious }},
	{"BREVO_API_KEY", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.BrevoAPIKey }},
	{"ONBOARDING_TURNSTILE_SECRET", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.OnboardingTurnstileSecret }},
	{"CONTACT_FORM_TURNSTILE_SECRET", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.ContactFormTurnstileSecret }},
	{"MEDIA_SIGNING_SECRET", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.MediaSigningSecret }},
	{"MEDIA_S3_ACCESS_KEY_ID", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.MediaS3AccessKeyID }},
	{"MEDIA_S3_SECRET_ACCESS_KEY", []Role{RoleAPI, RoleWorker}, func(c *Config) string { return c.MediaS3SecretAccessKey }},
	{"BOT_SERVICE_TOKEN", []Role{RoleAPI, RoleBot}, func(c *Config) string { return c.BotServiceToken }},
	{"BILLING_STRIPE_SECRET_KEY", []Role{RoleAPI}, func(c *Config) string { return c.BillingStripeSecretKey }},
	{"BILLING_STRIPE_WEBHOOK_SECRET", []Role{RoleAPI}, func(c *Config) string { return c.BillingStripeWebhookSecret }},
	{"BILLING_BANK_WEBHOOK_SECRET", []Role{RoleAPI}, func(c *Config) string { return c.BillingBankWebhookSecret }},
	{"METRICS_BEARER_TOKEN", []Role{RoleAPI, RoleWorker, RoleBot}, func(c *Config) string { return c.MetricsBearerToken }},
	{"SMTP_PASSWORD", []Role{RoleWorker}, func(c *Config) string { return c.SMTPPassword }},
	{"OUTBOX_SIGNING_SECRET", []Role{RoleWorker}, func(c *Config) string { return c.OutboxSigningSecret }},
	{"OPS_TELEGRAM_BOT_TOKEN", []Role{RoleWorker}, func(c *Config) string { return c.OpsTelegramBotToken }},
	{"SALES_TELEGRAM_BOT_TOKEN", []Role{RoleWorker}, func(c *Config) string { return c.SalesTelegramBotToken }},
	{"EVENTS_TELEGRAM_BOT_TOKEN", []Role{RoleBot}, func(c *Config) string { return c.EventsTelegramBotToken }},
	// POSTER_LLM_API_KEY also serves the text-formatting route (spec 43 RT-04,
	// TEXT_FORMAT_LLM_MODEL and TEXT_FORMATS_PER_*_PER_DAY are plain settings of
	// the api, not secrets): the model key stays on arena-api only.
	{"POSTER_LLM_API_KEY", []Role{RoleAPI}, func(c *Config) string { return c.PosterLLMAPIKey }},
}

// unusedSecretWarnings names every secret that is set although the role
// never reads it. The value is never printed. Development is skipped: the
// local compose file shares one environment on purpose.
func (c *Config) unusedSecretWarnings() []string {
	if c.Role == "" || c.Role == RoleAll || c.AppEnv == EnvDevelopment {
		return nil
	}
	var unused []string
	for _, s := range roleSecrets {
		if strings.TrimSpace(s.value(c)) == "" || roleListed(s.roles, c.Role) {
			continue
		}
		unused = append(unused, s.name)
	}
	if len(unused) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s does not use %s: remove them from this container's environment"+
			" (deploy/DOKPLOY.md section 2.6 lists which service needs which secret)",
		c.Role.Binary(), strings.Join(unused, ", "),
	)}
}

func roleListed(roles []Role, r Role) bool {
	for _, have := range roles {
		if have == r {
			return true
		}
	}
	return false
}
