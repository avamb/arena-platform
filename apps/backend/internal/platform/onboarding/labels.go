package onboarding

import "strings"

// SupportedLocales are the languages the form speaks. Anything else falls back
// to English; the website passes whatever its own language is.
var SupportedLocales = []string{"en", "ru", "es"}

// NormalizeLocale folds a tag such as "ru-RU" to a supported language.
func NormalizeLocale(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	for _, s := range SupportedLocales {
		if l == s {
			return l
		}
	}
	return "en"
}

// label looks a key up in the locale and falls back to English, then to the key.
func label(locale, key string) string {
	if m, ok := labels[locale]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := labels["en"][key]; ok {
		return s
	}
	return key
}

// labels: "s.<step>" a step title, "f.<field>" a field label, "h.<field>" a
// hint, "o.<field>.<value>" an option.
var labels = map[string]map[string]string{
	"en": {
		"s.contact":      "About you",
		"s.organization": "Your organization",
		"s.events":       "Your events",
		"s.platform":     "Tickets and payments",
		"s.consents":     "Agreements",

		"f.first_name":           "First name",
		"f.last_name":            "Last name",
		"f.email":                "E-mail",
		"f.phone":                "Phone",
		"h.phone":                "With the country code, for example +34 600 000 000",
		"f.telegram_username":    "Telegram username",
		"f.org_name":             "Name on posters and tickets",
		"h.org_name":             "The organizer's (promoter's) name as buyers see it on tickets and posters. The name of a concert comes later, when you create the event",
		"f.legal_name":           "Registered (legal) name",
		"h.legal_name":           "For our internal records only, so that we know our partner. It is never published",
		"f.country":              "Country of registration",
		"f.legal_form":           "Legal form",
		"f.tax_id":               "Tax number",
		"h.tax_id":               "Any tax number you consider right for yourself: company, sole proprietor or personal. For our internal records only, never published, and it does not have to match your payment account",
		"f.tax_id_scheme":        "Type of tax number",
		"f.registration_number":  "Company registration number",
		"f.address_line1":        "Registered address",
		"f.address_postal_code":  "Postal code",
		"f.address_city":         "City",
		"f.address_country":      "Country of the address",
		"f.website":              "Website",
		"f.social_links":         "Social media links",
		"f.event_types":          "What kind of events",
		"f.seating":              "Seating",
		"f.events_per_year":      "Events per year",
		"f.tickets_per_year":     "Tickets per year",
		"f.avg_ticket_price":     "Average ticket price",
		"f.currency":             "Currency",
		"f.first_event_date":     "Date of the first event",
		"f.previous_system":      "Where do you sell tickets now",
		"f.previous_system_name": "Name of the current system",
		"f.has_website":          "Do you have a website",
		"f.website_platform":     "What is the website built on",
		"f.wants_wp_plugin":      "Connect it to our WordPress plugin",
		"f.payment_provider":     "Payment provider",
		"h.payment_provider":     "You will enter your own account keys after approval, never in this form",
		"f.has_payment_account":  "Do you already have an account with it",
		"f.notes":                "Anything else we should know",
		"f.accept_terms":         "I accept the terms of service",
		"f.accept_privacy":       "I accept the privacy policy",
		"f.confirm_authority":    "I am authorized to act for this organization",
		"f.marketing_opt_in":     "Send me news and tips (optional)",

		"o.tax_id_scheme.vat":   "VAT number",
		"o.tax_id_scheme.ico":   "Company ID (IČO and similar)",
		"o.tax_id_scheme.ein":   "EIN (USA)",
		"o.tax_id_scheme.other": "Other",

		"o.event_types.concert":     "Concerts",
		"o.event_types.theatre":     "Theatre and shows",
		"o.event_types.masterclass": "Master classes and workshops",
		"o.event_types.festival":    "Festivals",
		"o.event_types.sport":       "Sport",
		"o.event_types.tour":        "Tours and excursions",
		"o.event_types.other":       "Other",

		"o.seating.ga":     "Free seating (no assigned seats)",
		"o.seating.seated": "Assigned seats",
		"o.seating.both":   "Both",

		"o.events_per_year.1-5":    "1 to 5",
		"o.events_per_year.6-20":   "6 to 20",
		"o.events_per_year.21-100": "21 to 100",
		"o.events_per_year.100+":   "More than 100",

		"o.tickets_per_year.<500":   "Fewer than 500",
		"o.tickets_per_year.500-5k": "500 to 5,000",
		"o.tickets_per_year.5k-50k": "5,000 to 50,000",
		"o.tickets_per_year.50k+":   "More than 50,000",

		"o.previous_system.bil24": "Bil24",
		"o.previous_system.other": "Another system",
		"o.previous_system.none":  "I do not sell tickets online yet",

		"o.website_platform.wordpress": "WordPress",
		"o.website_platform.other":     "Something else",
		"o.website_platform.none":      "No website",

		"o.payment_provider.stripe":    "Stripe",
		"o.payment_provider.flitt":     "Flitt",
		"o.payment_provider.other":     "Another provider",
		"o.payment_provider.undecided": "Not decided yet",
	},
	"ru": {
		"s.contact":      "О вас",
		"s.organization": "Ваша организация",
		"s.events":       "Ваши мероприятия",
		"s.platform":     "Билеты и оплата",
		"s.consents":     "Согласия",

		"f.first_name":           "Имя",
		"f.last_name":            "Фамилия",
		"f.email":                "E-mail",
		"f.phone":                "Телефон",
		"h.phone":                "С кодом страны, например +34 600 000 000",
		"f.telegram_username":    "Имя в Telegram",
		"f.org_name":             "Название на афишах и билетах",
		"h.org_name":             "Название организатора (промоутера), как его увидят покупатели на билетах и афишах. Название концерта вы введёте позже, когда будете создавать мероприятие",
		"f.legal_name":           "Юридическое название",
		"h.legal_name":           "Только для нашей внутренней админки, чтобы мы знали своего партнёра. Нигде не публикуется",
		"f.country":              "Страна регистрации",
		"f.legal_form":           "Организационно-правовая форма",
		"f.tax_id":               "Налоговый номер",
		"h.tax_id":               "Любой налоговый номер, который вы считаете правильным для себя: компании, ИП или личный. Только для нашей внутренней админки, нигде не публикуется. С вашей платёжной системой он совпадать не обязан",
		"f.tax_id_scheme":        "Тип налогового номера",
		"f.registration_number":  "Регистрационный номер компании",
		"f.address_line1":        "Юридический адрес",
		"f.address_postal_code":  "Почтовый индекс",
		"f.address_city":         "Город",
		"f.address_country":      "Страна адреса",
		"f.website":              "Сайт",
		"f.social_links":         "Ссылки на соцсети",
		"f.event_types":          "Какие мероприятия",
		"f.seating":              "Рассадка",
		"f.events_per_year":      "Мероприятий в год",
		"f.tickets_per_year":     "Билетов в год",
		"f.avg_ticket_price":     "Средняя цена билета",
		"f.currency":             "Валюта",
		"f.first_event_date":     "Дата первого мероприятия",
		"f.previous_system":      "Где вы продаёте билеты сейчас",
		"f.previous_system_name": "Название текущей системы",
		"f.has_website":          "Есть ли у вас сайт",
		"f.website_platform":     "На чём сделан сайт",
		"f.wants_wp_plugin":      "Подключить к нашему плагину для WordPress",
		"f.payment_provider":     "Платёжный провайдер",
		"h.payment_provider":     "Ключи своего аккаунта вы введёте после одобрения, не в этой анкете",
		"f.has_payment_account":  "Есть ли у вас уже аккаунт у провайдера",
		"f.notes":                "Что ещё нам стоит знать",
		"f.accept_terms":         "Я принимаю условия использования",
		"f.accept_privacy":       "Я принимаю политику конфиденциальности",
		"f.confirm_authority":    "Я уполномочен(а) действовать от имени этой организации",
		"f.marketing_opt_in":     "Присылайте мне новости и советы (по желанию)",

		"o.tax_id_scheme.vat":   "Номер плательщика НДС",
		"o.tax_id_scheme.ico":   "Регистрационный номер компании (IČO и аналоги)",
		"o.tax_id_scheme.ein":   "EIN (США)",
		"o.tax_id_scheme.other": "Другое",

		"o.event_types.concert":     "Концерты",
		"o.event_types.theatre":     "Театр и шоу",
		"o.event_types.masterclass": "Мастер-классы и воркшопы",
		"o.event_types.festival":    "Фестивали",
		"o.event_types.sport":       "Спорт",
		"o.event_types.tour":        "Туры и экскурсии",
		"o.event_types.other":       "Другое",

		"o.seating.ga":     "Свободная рассадка (без мест)",
		"o.seating.seated": "Места по схеме",
		"o.seating.both":   "И то, и другое",

		"o.events_per_year.1-5":    "От 1 до 5",
		"o.events_per_year.6-20":   "От 6 до 20",
		"o.events_per_year.21-100": "От 21 до 100",
		"o.events_per_year.100+":   "Больше 100",

		"o.tickets_per_year.<500":   "Меньше 500",
		"o.tickets_per_year.500-5k": "От 500 до 5 000",
		"o.tickets_per_year.5k-50k": "От 5 000 до 50 000",
		"o.tickets_per_year.50k+":   "Больше 50 000",

		"o.previous_system.bil24": "Bil24",
		"o.previous_system.other": "Другая система",
		"o.previous_system.none":  "Пока не продаю билеты онлайн",

		"o.website_platform.wordpress": "WordPress",
		"o.website_platform.other":     "Что-то другое",
		"o.website_platform.none":      "Сайта нет",

		"o.payment_provider.stripe":    "Stripe",
		"o.payment_provider.flitt":     "Flitt",
		"o.payment_provider.other":     "Другой провайдер",
		"o.payment_provider.undecided": "Ещё не решил(а)",
	},
	"es": {
		"s.contact":      "Sobre ti",
		"s.organization": "Tu organización",
		"s.events":       "Tus eventos",
		"s.platform":     "Entradas y pagos",
		"s.consents":     "Aceptaciones",

		"f.first_name":           "Nombre",
		"f.last_name":            "Apellidos",
		"f.email":                "Correo electrónico",
		"f.phone":                "Teléfono",
		"h.phone":                "Con el prefijo del país, por ejemplo +34 600 000 000",
		"f.telegram_username":    "Usuario de Telegram",
		"f.org_name":             "Nombre en carteles y entradas",
		"h.org_name":             "El nombre del organizador (promotor) tal como lo ven los compradores en las entradas y los carteles. El nombre del concierto vendrá después, al crear el evento",
		"f.legal_name":           "Razón social",
		"h.legal_name":           "Solo para nuestros registros internos, para conocer a nuestro socio. Nunca se publica",
		"f.country":              "País de registro",
		"f.legal_form":           "Forma jurídica",
		"f.tax_id":               "Número fiscal",
		"h.tax_id":               "Cualquier número fiscal que consideres adecuado: de empresa, autónomo o personal. Solo para nuestros registros internos, nunca se publica, y no tiene que coincidir con tu cuenta de pagos",
		"f.tax_id_scheme":        "Tipo de número fiscal",
		"f.registration_number":  "Número de registro de la empresa",
		"f.address_line1":        "Domicilio social",
		"f.address_postal_code":  "Código postal",
		"f.address_city":         "Ciudad",
		"f.address_country":      "País de la dirección",
		"f.website":              "Sitio web",
		"f.social_links":         "Enlaces a redes sociales",
		"f.event_types":          "Qué tipo de eventos",
		"f.seating":              "Ubicación",
		"f.events_per_year":      "Eventos al año",
		"f.tickets_per_year":     "Entradas al año",
		"f.avg_ticket_price":     "Precio medio de la entrada",
		"f.currency":             "Moneda",
		"f.first_event_date":     "Fecha del primer evento",
		"f.previous_system":      "Dónde vendes entradas ahora",
		"f.previous_system_name": "Nombre del sistema actual",
		"f.has_website":          "¿Tienes un sitio web?",
		"f.website_platform":     "Con qué está hecho el sitio",
		"f.wants_wp_plugin":      "Conectarlo con nuestro plugin de WordPress",
		"f.payment_provider":     "Proveedor de pagos",
		"h.payment_provider":     "Las claves de tu cuenta las introducirás tras la aprobación, nunca en este formulario",
		"f.has_payment_account":  "¿Ya tienes cuenta con él?",
		"f.notes":                "Algo más que debamos saber",
		"f.accept_terms":         "Acepto las condiciones del servicio",
		"f.accept_privacy":       "Acepto la política de privacidad",
		"f.confirm_authority":    "Estoy autorizado/a para actuar en nombre de esta organización",
		"f.marketing_opt_in":     "Quiero recibir novedades y consejos (opcional)",

		"o.tax_id_scheme.vat":   "Número de IVA",
		"o.tax_id_scheme.ico":   "Número de empresa (IČO y similares)",
		"o.tax_id_scheme.ein":   "EIN (EE. UU.)",
		"o.tax_id_scheme.other": "Otro",

		"o.event_types.concert":     "Conciertos",
		"o.event_types.theatre":     "Teatro y espectáculos",
		"o.event_types.masterclass": "Clases magistrales y talleres",
		"o.event_types.festival":    "Festivales",
		"o.event_types.sport":       "Deporte",
		"o.event_types.tour":        "Tours y excursiones",
		"o.event_types.other":       "Otro",

		"o.seating.ga":     "Entrada libre (sin asientos numerados)",
		"o.seating.seated": "Asientos numerados",
		"o.seating.both":   "Ambos",

		"o.events_per_year.1-5":    "De 1 a 5",
		"o.events_per_year.6-20":   "De 6 a 20",
		"o.events_per_year.21-100": "De 21 a 100",
		"o.events_per_year.100+":   "Más de 100",

		"o.tickets_per_year.<500":   "Menos de 500",
		"o.tickets_per_year.500-5k": "De 500 a 5.000",
		"o.tickets_per_year.5k-50k": "De 5.000 a 50.000",
		"o.tickets_per_year.50k+":   "Más de 50.000",

		"o.previous_system.bil24": "Bil24",
		"o.previous_system.other": "Otro sistema",
		"o.previous_system.none":  "Aún no vendo entradas en línea",

		"o.website_platform.wordpress": "WordPress",
		"o.website_platform.other":     "Otra cosa",
		"o.website_platform.none":      "No tengo sitio web",

		"o.payment_provider.stripe":    "Stripe",
		"o.payment_provider.flitt":     "Flitt",
		"o.payment_provider.other":     "Otro proveedor",
		"o.payment_provider.undecided": "Aún no lo he decidido",
	},
}

// FormSchema is the JSON the website and the bot render the form from.
type FormSchema struct {
	Version           int          `json:"version"`
	Locale            string       `json:"locale"`
	Steps             []SchemaStep `json:"steps"`
	AcceptedCountries []string     `json:"accepted_countries"`
	TermsVersion      string       `json:"terms_version"`
	PrivacyVersion    string       `json:"privacy_version"`
}

// SchemaStep is one page of the form.
type SchemaStep struct {
	Key    string        `json:"key"`
	Title  string        `json:"title"`
	Fields []SchemaField `json:"fields"`
}

// SchemaField is one question.
type SchemaField struct {
	Key         string         `json:"key"`
	Type        Kind           `json:"type"`
	Label       string         `json:"label"`
	Hint        string         `json:"hint,omitempty"`
	Required    bool           `json:"required"`
	ReadOnly    bool           `json:"read_only,omitempty"`
	MaxLength   int            `json:"max_length,omitempty"`
	MaxItems    int            `json:"max_items,omitempty"`
	DefaultFrom string         `json:"default_from,omitempty"`
	Options     []SchemaOption `json:"options,omitempty"`
}

// SchemaOption is one choice.
type SchemaOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// BuildSchema renders the form in a language. countries is the accepted list
// from the settings (empty = every country).
func BuildSchema(locale string, countries []string, termsVersion, privacyVersion string) FormSchema {
	locale = NormalizeLocale(locale)
	if countries == nil {
		countries = []string{}
	}
	out := FormSchema{
		Version:           SchemaVersion,
		Locale:            locale,
		AcceptedCountries: countries,
		TermsVersion:      termsVersion,
		PrivacyVersion:    privacyVersion,
	}
	for _, step := range Steps {
		s := SchemaStep{Key: step, Title: label(locale, "s."+step)}
		for _, f := range Fields {
			if f.Step != step {
				continue
			}
			sf := SchemaField{
				Key:         f.Key,
				Type:        f.Kind,
				Label:       label(locale, "f."+f.Key),
				Required:    f.Required,
				ReadOnly:    f.ReadOnly,
				DefaultFrom: f.DefaultFrom,
			}
			if h, ok := labels[locale]["h."+f.Key]; ok {
				sf.Hint = h
			} else if h, ok := labels["en"]["h."+f.Key]; ok {
				sf.Hint = h
			}
			switch f.Kind {
			case KindText, KindTextarea, KindEmail, KindPhone:
				sf.MaxLength = f.MaxLen
				if sf.MaxLength == 0 {
					sf.MaxLength = defaultTextMax
				}
			case KindURLList:
				sf.MaxItems = maxListItems
			}
			for _, o := range f.Options {
				sf.Options = append(sf.Options, SchemaOption{Value: o, Label: label(locale, "o."+f.Key+"."+o)})
			}
			s.Fields = append(s.Fields, sf)
		}
		out.Steps = append(out.Steps, s)
	}
	return out
}
