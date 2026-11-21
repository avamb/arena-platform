package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worldgeo"
)

// regionWords names a region of the world table for the administrator.
var regionWords = map[string]string{
	worldgeo.RegionEurope:       "Европа",
	worldgeo.RegionMiddleEast:   "Ближний Восток",
	worldgeo.RegionAsia:         "Азия",
	worldgeo.RegionAfrica:       "Африка",
	worldgeo.RegionNorthAmerica: "Северная Америка",
	worldgeo.RegionLatinAmerica: "Латинская Америка",
	worldgeo.RegionOceania:      "Океания",
}

// countryMessage builds the "new country" message: the country (subject), and
// the venue and organization that brought it, or the organization of the
// approved application. It names no person.
func countryMessage(ctx context.Context, tx pgx.Tx, p Payload, subject uuid.UUID, base string) (adminnotify.Message, bool, error) {
	var iso2, currency, region, name string
	err := tx.QueryRow(ctx, `
SELECT c.iso2, btrim(c.currency), c.region, COALESCE(t_ru.value, t_en.value, c.iso2)
FROM   countries c
LEFT JOIN i18n_text t_ru ON t_ru.namespace = 'geo.countries' AND t_ru.key = c.iso2 AND t_ru.locale = 'ru'
LEFT JOIN i18n_text t_en ON t_en.namespace = 'geo.countries' AND t_en.key = c.iso2 AND t_en.locale = 'en'
WHERE  c.id = $1`, subject).Scan(&iso2, &currency, &region, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminnotify.Message{}, false, nil
	}
	if err != nil {
		return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load country: %w", err)
	}

	var org, venue, city string
	if id, perr := uuid.Parse(p.VenueID); perr == nil {
		err = tx.QueryRow(ctx, `
SELECT o.name, v.name, COALESCE(t_city.value, ci.slug, '')
FROM   venues v
JOIN   organizations o ON o.id = v.org_id
LEFT JOIN cities ci    ON ci.id = v.city_id
LEFT JOIN i18n_text t_city ON t_city.namespace = 'geo.cities' AND t_city.key = ci.slug AND t_city.locale = 'en'
WHERE  v.id = $1`, id).Scan(&org, &venue, &city)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load venue: %w", err)
		}
	} else if id, perr := uuid.Parse(p.OrgID); perr == nil {
		err = tx.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, id).Scan(&org)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return adminnotify.Message{}, false, fmt.Errorf("venueplan notify: load organization: %w", err)
		}
	}

	msg := adminnotify.Message{
		Title:       "Новая страна: " + name,
		Details:     []string{fmt.Sprintf("Страна: %s (%s)", name, iso2), "Валюта: " + currency},
		ButtonLabel: "Открыть",
		WhatToDo:    whatToDoCountry,
	}
	if w, ok := regionWords[region]; ok {
		msg.Details = append(msg.Details, "Регион: "+w)
	}
	switch {
	case venue != "":
		msg.Title += ", площадка " + venue + ", организация " + org
		msg.Details = append(msg.Details, "Организация: "+org, "Площадка: "+venue)
		if city != "" {
			msg.Details = append(msg.Details, "Город: "+city)
		}
	case org != "":
		msg.Title += ", заявка " + org
		msg.Details = append(msg.Details, "Организация: "+org, "Страну добавило одобрение заявки организатора.")
	}
	if base != "" {
		msg.Link = base + "/geo"
	}
	return msg, true, nil
}
