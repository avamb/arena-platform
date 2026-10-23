-- +goose Up
-- =====================================================================
-- arena_new — the organizer's terms of sale: templates and renderings
-- (line P, task P3a; plan docs/plans/hosted_page_legal_web_center_wave_plan_2026-10-13_ru.md
-- section 4, spec 08_architecture/40 section 5.P)
--
-- The organizer never edits the text of the terms. A platform superadmin
-- (permission legal.manage, granted below to platform_superadmin only and
-- refused to every organization API key by apikeys.IsForbiddenScope) keeps
-- one template per kind and language in legal_documents; the organizer
-- fills the seller and promoter data and picks a refund policy, and the
-- platform substitutes them into the template (internal/platform/legaldocs:
-- a mini-parser with an allowlist of placeholders, no code).
--
--   * legal_documents  — the versioned templates. A version is immutable
--                        once published: the trigger below refuses any
--                        change of its text, language, kind or number, and
--                        any DELETE of a published or retired row.
--                        Publishing a new version retires the previous one
--                        of the same kind and language in one transaction;
--                        at most one version is published at a time.
--   * legal_renderings — the HTML a buyer was shown: the template of one
--                        version with one organization's data substituted.
--                        Content-addressed by (document_id, inputs_hash):
--                        the same data gives the same row. Evidence for a
--                        payment dispute (spec 38): never updated (trigger)
--                        and never deleted by code (static guard), like
--                        checkout_consents.
--   * organizations.refund_policy_mode / events.refund_policy_mode — the
--                        refund policy the terms state: the event's value,
--                        else the organization's, else refund_by_law.
--   * checkout_consents.legal_rendering_id / terms_sha256 — which rendering
--                        a buyer accepted at checkout (P2 named only the
--                        platform terms version until now).
--
-- The seeded version 1 of sale_terms is an ENGLISH DRAFT in all seven
-- languages, marked "DRAFT — awaiting legal review", stored as a DRAFT: no
-- terms page exists and every checkout keeps linking the platform terms
-- (P2) until a platform superadmin publishes a version the lawyer approved
-- (ST-3; contract docs/tasks/p3_organizer_terms_execution_contract.md:
-- drafts are never public, no document is enabled by default).
-- =====================================================================

SET LOCAL lock_timeout = '5s';

CREATE TABLE legal_documents (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind           text        NOT NULL,
    locale         text        NOT NULL,
    version        integer     NOT NULL,
    status         text        NOT NULL DEFAULT 'draft',
    body_template  text        NOT NULL,
    created_by     uuid,
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    published_by   uuid,
    retired_at     timestamptz,
    CONSTRAINT legal_documents_kind_check
        CHECK (kind IN ('sale_terms')),
    CONSTRAINT legal_documents_locale_check
        CHECK (locale IN ('en', 'ru', 'cs', 'he', 'es', 'de', 'fr')),
    CONSTRAINT legal_documents_status_check
        CHECK (status IN ('draft', 'published', 'retired')),
    CONSTRAINT legal_documents_version_positive
        CHECK (version >= 1),
    CONSTRAINT legal_documents_body_length
        CHECK (char_length(body_template) BETWEEN 1 AND 100000),
    CONSTRAINT legal_documents_published_at_check
        CHECK ((status = 'draft') = (published_at IS NULL)),
    CONSTRAINT legal_documents_retired_at_check
        CHECK ((status = 'retired') = (retired_at IS NOT NULL)),
    CONSTRAINT legal_documents_kind_locale_version_uq
        UNIQUE (kind, locale, version)
);

-- At most one published version per kind and language.
CREATE UNIQUE INDEX legal_documents_one_published
    ON legal_documents (kind, locale)
    WHERE status = 'published';

-- +goose StatementBegin
CREATE FUNCTION legal_documents_guard() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.status <> 'draft' THEN
            RAISE EXCEPTION 'legal_documents: a % version cannot be deleted', OLD.status
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF NEW.id <> OLD.id OR NEW.kind <> OLD.kind OR NEW.locale <> OLD.locale
       OR NEW.version <> OLD.version OR NEW.created_at <> OLD.created_at
       OR NEW.created_by IS DISTINCT FROM OLD.created_by THEN
        RAISE EXCEPTION 'legal_documents: the identity of a version never changes'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'draft' THEN
        IF NEW.status NOT IN ('draft', 'published') THEN
            RAISE EXCEPTION 'legal_documents: a draft can only be published'
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
        RETURN NEW;
    END IF;
    -- A published or retired version is frozen: its text and its
    -- publication stay as the buyers saw them.
    IF NEW.body_template <> OLD.body_template
       OR NEW.published_at IS DISTINCT FROM OLD.published_at
       OR NEW.published_by IS DISTINCT FROM OLD.published_by THEN
        RAISE EXCEPTION 'legal_documents: a published version is immutable'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'published' AND NEW.status NOT IN ('published', 'retired') THEN
        RAISE EXCEPTION 'legal_documents: a published version can only be retired'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    IF OLD.status = 'retired' AND (NEW.status <> 'retired' OR NEW.retired_at IS DISTINCT FROM OLD.retired_at) THEN
        RAISE EXCEPTION 'legal_documents: a retired version stays retired'
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER legal_documents_guard
    BEFORE UPDATE OR DELETE ON legal_documents
    FOR EACH ROW EXECUTE FUNCTION legal_documents_guard();

CREATE TABLE legal_renderings (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    channel_id        uuid,
    event_id          uuid,
    document_id       uuid        NOT NULL REFERENCES legal_documents(id),
    document_version  integer     NOT NULL,
    locale            text        NOT NULL,
    refund_mode       text        NOT NULL,
    inputs            jsonb       NOT NULL,
    inputs_hash       text        NOT NULL,
    body_html         text        NOT NULL,
    body_sha256       text        NOT NULL,
    rendered_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT legal_renderings_refund_mode_check
        CHECK (refund_mode IN ('no_refund', 'refund_until_sales_end', 'refund_by_law')),
    CONSTRAINT legal_renderings_inputs_hash_format
        CHECK (inputs_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT legal_renderings_body_sha256_format
        CHECK (body_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT legal_renderings_document_inputs_uq
        UNIQUE (document_id, inputs_hash)
);

CREATE INDEX legal_renderings_org ON legal_renderings (org_id, rendered_at);

-- +goose StatementBegin
CREATE FUNCTION legal_renderings_frozen() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'legal_renderings: a rendering is evidence and never changes'
        USING ERRCODE = 'integrity_constraint_violation';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER legal_renderings_frozen
    BEFORE UPDATE ON legal_renderings
    FOR EACH ROW EXECUTE FUNCTION legal_renderings_frozen();

ALTER TABLE organizations
    ADD COLUMN refund_policy_mode text,
    ADD CONSTRAINT organizations_refund_policy_mode_check
        CHECK (refund_policy_mode IS NULL OR refund_policy_mode IN ('no_refund', 'refund_until_sales_end', 'refund_by_law'));

ALTER TABLE events
    ADD COLUMN refund_policy_mode text,
    ADD CONSTRAINT events_refund_policy_mode_check
        CHECK (refund_policy_mode IS NULL OR refund_policy_mode IN ('no_refund', 'refund_until_sales_end', 'refund_by_law'));

ALTER TABLE checkout_consents
    ADD COLUMN legal_rendering_id uuid REFERENCES legal_renderings(id),
    ADD COLUMN terms_sha256 text,
    ADD CONSTRAINT checkout_consents_terms_sha256_format
        CHECK (terms_sha256 IS NULL OR terms_sha256 ~ '^[0-9a-f]{64}$');

COMMENT ON TABLE legal_documents IS
    'Versioned templates of the organizer terms (P3a). Immutable once published (trigger legal_documents_guard). Edited by legal.manage (platform superadmin) only.';
COMMENT ON TABLE legal_renderings IS
    'The terms HTML a buyer was shown: one template version with one organization''s data. Dispute evidence: never updated, never deleted by code.';
COMMENT ON COLUMN legal_renderings.inputs IS
    'The resolved placeholder values and the context (organization, channel, event) the HTML was rendered from; inputs_hash is its SHA-256.';
COMMENT ON COLUMN organizations.refund_policy_mode IS
    'Refund policy the terms state for the organization: no_refund, refund_until_sales_end or refund_by_law. NULL = refund_by_law.';
COMMENT ON COLUMN events.refund_policy_mode IS
    'Refund policy of one event, overriding the organization''s. NULL = the organization''s.';
COMMENT ON COLUMN checkout_consents.legal_rendering_id IS
    'The rendering of the organizer terms the buyer accepted (P3a). NULL for consents that named only the platform terms.';
COMMENT ON COLUMN checkout_consents.terms_sha256 IS
    'SHA-256 of the accepted rendering''s HTML (legal_renderings.body_sha256 at that moment).';

INSERT INTO permissions (name, description) VALUES
  ('legal.manage', 'Write and publish the versioned templates of the organizer terms');

-- Superadmin parity (TestSuperadminPermissionParity532): legal.manage is a
-- platform permission and is granted to platform_superadmin only.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'legal.manage'
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- Version 1 of sale_terms in every language: an English DRAFT that waits
-- for the lawyer (ST-3); it is not published. legaldocs.TestSeededTemplates_Parse
-- parses it.
-- +goose StatementBegin
INSERT INTO legal_documents (kind, locale, version, status, body_template)
SELECT 'sale_terms', l.locale, 1, 'draft', $seed$DRAFT — awaiting legal review. This is a working draft of the terms of sale. It is not final and it is not legal advice.

## Terms of sale

These terms apply to every ticket you buy on this sales page. Version {{document.version}}.

## Who sells the tickets

{{#if seller.is_platform}}
The seller of the tickets is {{seller.legal_name}}, the operator of the Arena Sold Out platform. It sells the tickets as merchant of record for the organizer of the event.
{{else}}
The seller of the tickets is {{seller.legal_name}}{{#if seller.legal_form}} ({{seller.legal_form}}){{/if}}, the organizer of the event. Arena Sold Out provides the ticketing platform and acts as an intermediary. Arena Sold Out is not the seller.
{{/if}}
{{#if seller.address}}
Address: {{seller.address}}.
{{/if}}
{{#if seller.ids_line}}
{{seller.ids_line}}.
{{/if}}
{{#if seller.phone}}
Phone: {{seller.phone}}.
{{/if}}
To contact the seller, use the form "Write to the organizer" on the sales page.

{{#if promoter.name}}
## Who presents the event

The event is presented by {{promoter.name}}{{#if promoter.legal_id}} ({{promoter.legal_id}}){{/if}}{{#if promoter.address}}, {{promoter.address}}{{/if}}. The seller named above sells the tickets and is responsible for the sale.
{{/if}}

## Price and fees

The price you see before you pay includes all taxes. A service fee, if there is one, is shown as a separate line before you pay.

## Tickets

After payment you get your tickets by e-mail. Show the ticket at the entrance. One ticket admits one person once.

## Refunds

{{refund.mode_text}}

If the organizer cancels the event, you get the price of the ticket back. If the organizer moves the event to another date, you get an e-mail about it.

{{#if law.no_withdrawal}}
## No right of withdrawal

The tickets are for a leisure event on a specific date. For this reason the 14-day right of withdrawal for distance contracts does not apply to them.
{{/if}}

## Personal data

The privacy policy linked on the checkout page explains how your personal data is used.

## Applicable law

{{#if law.country}}
These terms are governed by the law of {{law.country}}. This does not take away the protection that the law of your country of residence gives you as a consumer.
{{else}}
These terms are governed by the law of the country of the seller. This does not take away the protection that the law of your country of residence gives you as a consumer.
{{/if}}$seed$
FROM (VALUES ('en'), ('ru'), ('cs'), ('he'), ('es'), ('de'), ('fr')) AS l(locale);
-- +goose StatementEnd

-- +goose Down
ALTER TABLE checkout_consents
    DROP CONSTRAINT IF EXISTS checkout_consents_terms_sha256_format,
    DROP COLUMN IF EXISTS terms_sha256,
    DROP COLUMN IF EXISTS legal_rendering_id;
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_refund_policy_mode_check,
    DROP COLUMN IF EXISTS refund_policy_mode;
ALTER TABLE organizations
    DROP CONSTRAINT IF EXISTS organizations_refund_policy_mode_check,
    DROP COLUMN IF EXISTS refund_policy_mode;
DROP TABLE IF EXISTS legal_renderings;
DROP FUNCTION IF EXISTS legal_renderings_frozen();
DROP TABLE IF EXISTS legal_documents;
DROP FUNCTION IF EXISTS legal_documents_guard();
DELETE FROM permissions WHERE name = 'legal.manage';
