-- 0216_venue_plan_intake.sql — the organizer sends the files of a hall plan
-- (PDF, Excel, a photo, an SVG), arena builds a draft, the organizer checks
-- it, a platform administrator helps when the draft cannot be built, and a
-- platform superadmin verifies the finished plan (spec 08_architecture/64,
-- sections 4.2, 4.3 and 4.7, wave 1).
--
--   * venue_plan_intakes         — one request of an organization for one
--                                  venue, with its state (spec 4.2).
--   * venue_plan_intake_sources  — the files sent, stored in media_objects
--                                  with the closed owner type venue_plan_source.
--   * venue_plan_drafts          — every draft of the plan (n = 1, 2, ...); a
--                                  draft is never overwritten.
--   * venue_plan_questions       — the questions of a draft to the organizer
--                                  (written by the generator job, wave 2).
--   * venue_plan_corrections     — answers and text corrections, kept for the
--                                  training corpus (spec section 5).
--   * venue_plan_notices         — what the bot must tell the organizer; the
--                                  bot claims the rows.
--   * venue_plan_notify_log      — the dedup keys of the administrators'
--                                  notifications.
--
-- seating_plans gets a verification state: NULL = never sent for
-- verification (every plan made before this migration), unverified = built by
-- an intake and waiting for a superadmin, verified, needs_fix.
--
-- media_objects.owner_type gets venue_plan_source. It is NOT in
-- mediastore.AllowedOwnerTypes: only the intake routes create one.
--
-- billable_events.kind gets venue_plan_intake: an approved intake is a
-- billing fact rated 0 with the reason free (spec 4.7, owner decision
-- 2026-11-09). No price-list rule is needed: billing.Rate prices it itself.
--
-- Permission venue_plan_intake.curate (the administrators' intake queue) is
-- granted to platform_superadmin ONLY, in this same migration (the 532 parity
-- guard). An organization API key never carries it (apikeys.ForbiddenScopeExact).

-- +goose Up

-- The foreign keys lock organizations, venues, users, media_objects and
-- seating_plans; a deploy that meets a long transaction fails fast instead of
-- queueing every request behind it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE venue_plan_intakes (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    venue_id            uuid        NOT NULL REFERENCES venues(id),
    created_by          uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    channel             text        NOT NULL,
    state               text        NOT NULL DEFAULT 'received',
    escalation_reason   text        NULL,
    escalation_text     text        NULL,
    operator_id         uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    operator_claimed_at timestamptz NULL,
    current_draft       integer     NULL,
    plan_id             uuid        NULL REFERENCES seating_plans(id),
    plan_version        integer     NULL,
    reject_reason       text        NULL,
    return_note         text        NULL,
    idempotency_key     text        NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    closed_at           timestamptz NULL,
    CONSTRAINT venue_plan_intakes_channel_check
        CHECK (channel IN ('telegram_bot', 'event_center_web')),
    CONSTRAINT venue_plan_intakes_state_check
        CHECK (state IN ('received', 'generating', 'review', 'operator', 'approved', 'rejected', 'cancelled')),
    CONSTRAINT venue_plan_intakes_escalation_reason_check
        CHECK (escalation_reason IS NULL OR escalation_reason IN (
            'no_generator', 'generator_failed', 'draft_invalid', 'too_many_questions',
            'organizer_request', 'organizer_text', 'answers', 'unsupported_format')),
    CONSTRAINT venue_plan_intakes_escalation_text_check
        CHECK (escalation_text IS NULL OR char_length(escalation_text) <= 1000),
    CONSTRAINT venue_plan_intakes_current_draft_check
        CHECK (current_draft IS NULL OR current_draft > 0),
    CONSTRAINT venue_plan_intakes_plan_version_check
        CHECK (plan_version IS NULL OR plan_version > 0),
    CONSTRAINT venue_plan_intakes_approved_plan_check
        CHECK (state <> 'approved' OR (plan_id IS NOT NULL AND plan_version IS NOT NULL)),
    CONSTRAINT venue_plan_intakes_reject_reason_check
        CHECK (reject_reason IS NULL OR char_length(reject_reason) <= 2000),
    CONSTRAINT venue_plan_intakes_return_note_check
        CHECK (return_note IS NULL OR char_length(return_note) <= 2000),
    CONSTRAINT venue_plan_intakes_idempotency_key_check
        CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 200),
    CONSTRAINT venue_plan_intakes_closed_check
        CHECK ((closed_at IS NOT NULL) = (state IN ('approved', 'rejected', 'cancelled')))
);

-- A repeated upload with the same Idempotency-Key returns the same intake.
CREATE UNIQUE INDEX venue_plan_intakes_idempotency_uq
    ON venue_plan_intakes (org_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
-- The organization's list, filtered by state.
CREATE INDEX venue_plan_intakes_org_state_idx
    ON venue_plan_intakes (org_id, state, created_at DESC);
-- The intakes of one venue.
CREATE INDEX venue_plan_intakes_venue_idx
    ON venue_plan_intakes (venue_id, created_at DESC);
-- The administrators' queue: state, then who took it.
CREATE INDEX venue_plan_intakes_state_operator_idx
    ON venue_plan_intakes (state, operator_id, created_at);
-- Every intake, newest first (the admin list without filters).
CREATE INDEX venue_plan_intakes_created_idx
    ON venue_plan_intakes (created_at DESC);

COMMENT ON TABLE venue_plan_intakes IS
    'A request of an organization to build the hall plan of a venue from the files it sent (spec 64). State machine of spec 4.2; closed_at is set exactly when the state is final (approved, rejected, cancelled).';

CREATE TABLE venue_plan_intake_sources (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    intake_id           uuid        NOT NULL REFERENCES venue_plan_intakes(id) ON DELETE CASCADE,
    media_id            uuid        NOT NULL REFERENCES media_objects(id),
    normalized_media_id uuid        NULL REFERENCES media_objects(id),
    sha256              text        NOT NULL,
    kind                text        NOT NULL,
    file_name           text        NOT NULL,
    byte_size           bigint      NOT NULL,
    pages               integer     NULL,
    width               integer     NULL,
    height              integer     NULL,
    needs_conversion    boolean     NOT NULL DEFAULT false,
    position            integer     NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_plan_intake_sources_kind_check
        CHECK (kind IN ('pdf', 'xlsx', 'xls', 'csv', 'jpeg', 'png', 'webp', 'heic', 'tiff', 'bmp', 'gif', 'svg')),
    CONSTRAINT venue_plan_intake_sources_sha256_check
        CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT venue_plan_intake_sources_file_name_check
        CHECK (char_length(file_name) BETWEEN 1 AND 255),
    CONSTRAINT venue_plan_intake_sources_byte_size_check
        CHECK (byte_size >= 0),
    CONSTRAINT venue_plan_intake_sources_pages_check
        CHECK (pages IS NULL OR pages > 0),
    CONSTRAINT venue_plan_intake_sources_size_check
        CHECK ((width IS NULL OR width > 0) AND (height IS NULL OR height > 0)),
    CONSTRAINT venue_plan_intake_sources_position_check
        CHECK (position >= 0),
    CONSTRAINT venue_plan_intake_sources_position_uq UNIQUE (intake_id, position)
);

CREATE INDEX venue_plan_intake_sources_media_idx
    ON venue_plan_intake_sources (media_id);

COMMENT ON TABLE venue_plan_intake_sources IS
    'A file of a plan intake. The bytes are a media_objects row of owner type venue_plan_source (no public link); normalized_media_id is a PNG copy when the original needs conversion.';

CREATE TABLE venue_plan_drafts (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    intake_id         uuid        NOT NULL REFERENCES venue_plan_intakes(id) ON DELETE CASCADE,
    n                 integer     NOT NULL,
    author            text        NOT NULL,
    author_user_id    uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    generator_version text        NULL,
    model             jsonb       NULL,
    report            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    svg_media_id      uuid        NOT NULL REFERENCES media_objects(id),
    preview_media_id  uuid        NULL REFERENCES media_objects(id),
    note              text        NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_plan_drafts_n_check CHECK (n > 0),
    CONSTRAINT venue_plan_drafts_author_check
        CHECK (author IN ('generator', 'organizer_answers', 'organizer_text', 'operator', 'operator_svg', 'source_svg')),
    CONSTRAINT venue_plan_drafts_note_check
        CHECK (note IS NULL OR char_length(note) <= 2000),
    CONSTRAINT venue_plan_drafts_n_uq UNIQUE (intake_id, n)
);

COMMENT ON TABLE venue_plan_drafts IS
    'A draft of a plan intake, numbered n = 1, 2, ... per intake and never overwritten. report is the summary (seats, rows, sections, plan_type, warnings).';

CREATE TABLE venue_plan_questions (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    draft_id    uuid        NOT NULL REFERENCES venue_plan_drafts(id) ON DELETE CASCADE,
    kind        text        NOT NULL,
    target      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    region      jsonb       NULL,
    options     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    answer      jsonb       NULL,
    answered_by uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    answered_at timestamptz NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_plan_questions_kind_check CHECK (char_length(kind) BETWEEN 1 AND 64),
    CONSTRAINT venue_plan_questions_answered_check CHECK ((answer IS NULL) = (answered_at IS NULL))
);

CREATE INDEX venue_plan_questions_draft_idx
    ON venue_plan_questions (draft_id, created_at);

CREATE TABLE venue_plan_corrections (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    intake_id  uuid        NOT NULL REFERENCES venue_plan_intakes(id) ON DELETE CASCADE,
    draft_id   uuid        NULL REFERENCES venue_plan_drafts(id) ON DELETE CASCADE,
    kind       text        NOT NULL,
    text       text        NULL,
    operations jsonb       NULL,
    status     text        NOT NULL DEFAULT 'pending',
    created_by uuid        NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_plan_corrections_kind_check
        CHECK (kind IN ('organizer_text', 'operator_edit', 'answer')),
    CONSTRAINT venue_plan_corrections_status_check
        CHECK (status IN ('pending', 'applied', 'rejected')),
    CONSTRAINT venue_plan_corrections_text_check
        CHECK (text IS NULL OR char_length(text) <= 4000)
);

CREATE INDEX venue_plan_corrections_intake_idx
    ON venue_plan_corrections (intake_id, created_at);

CREATE TABLE venue_plan_notices (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    intake_id  uuid        NOT NULL REFERENCES venue_plan_intakes(id) ON DELETE CASCADE,
    kind       text        NOT NULL,
    note       text        NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz NULL,
    CONSTRAINT venue_plan_notices_kind_check
        CHECK (kind IN ('draft_ready', 'returned', 'rejected', 'approved')),
    CONSTRAINT venue_plan_notices_note_check
        CHECK (note IS NULL OR char_length(note) <= 2000)
);

-- The bot claims the oldest unclaimed notices.
CREATE INDEX venue_plan_notices_unclaimed_idx
    ON venue_plan_notices (created_at)
    WHERE claimed_at IS NULL;

COMMENT ON TABLE venue_plan_notices IS
    'What the bot must tell the creator of an intake (draft_ready, returned, rejected, approved). POST /v1/bot/venue-plan-notices/claim sets claimed_at; a notice is told at most once.';

CREATE TABLE venue_plan_notify_log (
    -- An intake id or a venue id (venue_created has no intake): no FK on purpose.
    subject_id uuid        NOT NULL,
    notify_key text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_plan_notify_log_key_check CHECK (char_length(notify_key) BETWEEN 1 AND 200),
    PRIMARY KEY (subject_id, notify_key)
);

COMMENT ON TABLE venue_plan_notify_log IS
    'Dedup of the administrators'' notifications about an intake or a venue: a (subject, key) pair is sent once, so a retried worker job never repeats a message. subject_id is an intake id or a venue id and has no foreign key.';

ALTER TABLE seating_plans
    ADD COLUMN verification_state text        NULL,
    ADD COLUMN verification_note  text        NULL,
    ADD COLUMN verified_at        timestamptz NULL,
    ADD COLUMN verified_by        uuid        NULL REFERENCES users(id) ON DELETE SET NULL;

ALTER TABLE seating_plans
    ADD CONSTRAINT seating_plans_verification_state_check
        CHECK (verification_state IS NULL OR verification_state IN ('unverified', 'verified', 'needs_fix')),
    ADD CONSTRAINT seating_plans_verification_note_check
        CHECK (verification_note IS NULL OR char_length(verification_note) <= 2000);

-- The superadmin's verification queue.
CREATE INDEX seating_plans_unverified_idx
    ON seating_plans (created_at)
    WHERE verification_state = 'unverified' AND deleted_at IS NULL;

COMMENT ON COLUMN seating_plans.verification_state IS
    'NULL = never sent for verification (plans made before migration 0216, or by hand); unverified = built by a plan intake and waiting for a platform superadmin; verified; needs_fix (verification_note says why).';

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report',
            'shop_product',
            'shop_digital',
            'payment_receipt',
            'venue_plan_source'
        ));

-- The billing fact of an approved intake. The drop takes ACCESS EXCLUSIVE on
-- billable_events until the migration commits (lock_timeout 5s); the re-check
-- of its rows is a short scan.
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run', 'white_label_month',
    'shop_module_month', 'venue_plan_intake'));

INSERT INTO permissions (name, description) VALUES
    ('venue_plan_intake.curate', 'Work the hall plan intakes of every organization: take, draw, return, approve or reject')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'venue_plan_intake.curate'
WHERE  r.name IN ('platform_superadmin')
ON CONFLICT DO NOTHING;

-- +goose Down

SET LOCAL lock_timeout = '5s';

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'venue_plan_intake.curate');
DELETE FROM permissions WHERE name = 'venue_plan_intake.curate';

-- Down refuses (the CHECK below fails) once a venue_plan_intake event exists:
-- billing facts are never deleted.
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run', 'white_label_month',
    'shop_module_month'));

DROP INDEX IF EXISTS seating_plans_unverified_idx;
ALTER TABLE seating_plans
    DROP CONSTRAINT IF EXISTS seating_plans_verification_note_check,
    DROP CONSTRAINT IF EXISTS seating_plans_verification_state_check,
    DROP COLUMN IF EXISTS verified_by,
    DROP COLUMN IF EXISTS verified_at,
    DROP COLUMN IF EXISTS verification_note,
    DROP COLUMN IF EXISTS verification_state;

DROP TABLE IF EXISTS venue_plan_notify_log;
DROP TABLE IF EXISTS venue_plan_notices;
DROP TABLE IF EXISTS venue_plan_corrections;
DROP TABLE IF EXISTS venue_plan_questions;
DROP TABLE IF EXISTS venue_plan_drafts;
DROP TABLE IF EXISTS venue_plan_intake_sources;
DROP TABLE IF EXISTS venue_plan_intakes;

DELETE FROM media_objects WHERE owner_type = 'venue_plan_source';

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report',
            'shop_product',
            'shop_digital',
            'payment_receipt'
        ));
