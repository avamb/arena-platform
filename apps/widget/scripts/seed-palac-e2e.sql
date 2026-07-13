-- =============================================================================
-- Palác Akropolis E2E seed — WID-R3
--
-- Idempotent (ON CONFLICT DO NOTHING). Run AFTER arena-seed has seeded:
--   OrgA              fe000001-0000-7000-8000-000000000001
--   VenueA1           fe000002-0000-7000-8000-00000000000a
--   ChannelAStripe    fe000005-0000-7000-8000-000000000001
--
-- Fixed IDs (fe000007-* range):
--   event             fe000007-0000-7000-8000-000000000001
--   session           fe000007-0000-7000-8000-000000000002
--   seating_plan      fe000007-0000-7000-8000-000000000003
--   seating_plan_ver  fe000007-0000-7000-8000-000000000004
--   tier Parket       fe000007-0000-7000-8000-000000000005
--   tier Galérie      fe000007-0000-7000-8000-000000000006
--   agent_feed_token  fe000007-0000-7000-8000-000000000007
--   ledger session    fe000007-0000-7000-8000-000000000008
--   ledger parket     fe000007-0000-7000-8000-000000000009
--   ledger galerie    fe000007-0000-7000-8000-00000000000a
-- =============================================================================

-- ── 1. Event ─────────────────────────────────────────────────────────────────

INSERT INTO events (
    id,
    org_id,
    venue_id,
    name,
    description,
    status,
    start_at,
    end_at,
    visibility
)
VALUES (
    'fe000007-0000-7000-8000-000000000001',
    'fe000001-0000-7000-8000-000000000001',  -- OrgA
    'fe000002-0000-7000-8000-00000000000a',  -- VenueA1
    'Palác Akropolis — E2E Test Event',
    'Auto-seeded E2E fixture for WID-R3 real-backend acceptance tests.',
    'published',
    now() + interval '30 days',
    now() + interval '30 days' + interval '3 hours',
    'public'
)
ON CONFLICT (id) DO NOTHING;

-- ── 2. Seating plan ──────────────────────────────────────────────────────────

INSERT INTO seating_plans (
    id,
    venue_id,
    owner_org_id,
    name,
    plan_type,
    visibility,
    status
)
VALUES (
    'fe000007-0000-7000-8000-000000000003',
    'fe000002-0000-7000-8000-00000000000a',  -- VenueA1
    'fe000001-0000-7000-8000-000000000001',  -- OrgA
    'Palác Akropolis — Main Hall',
    'mixed',
    'private',
    'active'
)
ON CONFLICT (id) DO NOTHING;

-- ── 3. Seating plan version (geometry via PostgreSQL JSON functions) ──────────
--
-- Geometry layout:
--   sections: "Parket" — 10 rows (A–J) × 26 seats = 260 assigned seats
--   standing_zones: "Galérie" — capacity 100
--
-- Seat key format: chr(64+r) || lpad(s::text, 2, '0')
--   e.g. row 1 seat 1 → 'A01', row 2 seat 3 → 'B03'
-- Seat coordinates: x = 80 + (s-1)*28,  y = 40 + (r-1)*32,  radius = 10

INSERT INTO seating_plan_versions (
    id,
    seating_plan_id,
    version_number,
    geometry,
    geometry_checksum,
    capacity_seated,
    capacity_standing
)
SELECT
    'fe000007-0000-7000-8000-000000000004',
    'fe000007-0000-7000-8000-000000000003',
    1,
    jsonb_build_object(
        'schema_version', 1,
        'canvas',         jsonb_build_object('width', 1000, 'height', 380),
        'categories',     jsonb_build_array(
            jsonb_build_object(
                'index', 0, 'name', 'Parket',
                'color', '#4F46E5', 'price_hint', '22.00', 'currency_hint', 'EUR'
            ),
            jsonb_build_object(
                'index', 1, 'name', 'Galérie',
                'color', '#10B981', 'price_hint', '12.00', 'currency_hint', 'EUR'
            )
        ),
        'sections',       jsonb_build_array(
            jsonb_build_object(
                'key',  'parket',
                'name', 'Parket',
                'rows', (
                    SELECT jsonb_agg(
                        jsonb_build_object(
                            'key',   'parket-row-' || chr(64 + r),
                            'name',  chr(64 + r),
                            'seats', (
                                SELECT jsonb_agg(
                                    jsonb_build_object(
                                        'key',            chr(64 + r) || lpad(s::text, 2, '0'),
                                        'number',         lpad(s::text, 2, '0'),
                                        'x',              80 + (s - 1) * 28,
                                        'y',              40 + (r - 1) * 32,
                                        'radius',         10,
                                        'category_index', 0,
                                        'barcode_hint',   NULL
                                    )
                                    ORDER BY s
                                )
                                FROM generate_series(1, 26) s
                            )
                        )
                        ORDER BY r
                    )
                    FROM generate_series(1, 10) r
                )
            )
        ),
        'standing_zones', jsonb_build_array(
            jsonb_build_object(
                'key',      'galerie',
                'name',     'Galérie',
                'capacity', 100
            )
        ),
        'tables',    '[]'::jsonb,
        'decor_svg', ''
    ),
    'sha256-palac-akropolis-e2e-260seats-v1',
    260,
    100
ON CONFLICT (id) DO NOTHING;

-- ── 4. Point seating_plan.current_version_id at the version we just inserted ─

UPDATE seating_plans
SET    current_version_id = 'fe000007-0000-7000-8000-000000000004',
       updated_at         = now()
WHERE  id                 = 'fe000007-0000-7000-8000-000000000003'
  AND  current_version_id IS NULL;

-- ── 5. Session ────────────────────────────────────────────────────────────────

INSERT INTO sessions (
    id,
    event_id,
    start_at,
    end_at,
    capacity_total,
    status,
    admission_mode,
    seating_plan_version_id
)
VALUES (
    'fe000007-0000-7000-8000-000000000002',
    'fe000007-0000-7000-8000-000000000001',
    now() + interval '30 days',
    now() + interval '30 days' + interval '3 hours',
    360,
    'scheduled',
    'hybrid',
    'fe000007-0000-7000-8000-000000000004'
)
ON CONFLICT (id) DO NOTHING;

-- ── 6. Ticket tiers ──────────────────────────────────────────────────────────

INSERT INTO ticket_tiers (
    id,
    session_id,
    name,
    pricing_mode,
    price_amount,
    currency,
    capacity,
    sort_order
)
VALUES
    (
        'fe000007-0000-7000-8000-000000000005',
        'fe000007-0000-7000-8000-000000000002',
        'Parket',
        'fixed',
        2200,   -- EUR 22.00 in minor units
        'EUR',
        260,
        0
    ),
    (
        'fe000007-0000-7000-8000-000000000006',
        'fe000007-0000-7000-8000-000000000002',
        'Galérie',
        'fixed',
        1200,   -- EUR 12.00 in minor units
        'EUR',
        100,
        1
    )
ON CONFLICT (id) DO NOTHING;

-- ── 7. Inventory ledger ───────────────────────────────────────────────────────
--
-- The unique index is: (session_id, tier_id) NULLS NOT DISTINCT
-- so ON CONFLICT (id) DO NOTHING avoids touching the partial unique index.

INSERT INTO inventory_ledger (id, session_id, tier_id, capacity_total)
VALUES
    -- session-level aggregate (tier_id NULL = total pool)
    (
        'fe000007-0000-7000-8000-000000000008',
        'fe000007-0000-7000-8000-000000000002',
        NULL,
        360
    ),
    -- Parket tier
    (
        'fe000007-0000-7000-8000-000000000009',
        'fe000007-0000-7000-8000-000000000002',
        'fe000007-0000-7000-8000-000000000005',
        260
    ),
    -- Galérie tier
    (
        'fe000007-0000-7000-8000-00000000000a',
        'fe000007-0000-7000-8000-000000000002',
        'fe000007-0000-7000-8000-000000000006',
        100
    )
ON CONFLICT DO NOTHING;

-- ── 8. Feed token ─────────────────────────────────────────────────────────────

INSERT INTO agent_feed_tokens (
    id,
    token,
    sales_channel_id,
    label,
    is_active
)
VALUES (
    'fe000007-0000-7000-8000-000000000007',
    'palac-akropolis-e2e-token-v1',
    'fe000005-0000-7000-8000-000000000001',  -- ChannelAStripe
    'Palác Akropolis E2E Widget Token',
    true
)
ON CONFLICT DO NOTHING;

-- ── 9. Session seats (260 assigned seats for Parket) ─────────────────────────
--
-- Key format: chr(64+r) || lpad(s::text, 2, '0')
--   row 1 (r=1) → 'A', row 10 (r=10) → 'J'
--   seat 1–26 padded to 2 digits
-- All seats start as 'available', assigned to the Parket tier.

INSERT INTO session_seats (
    id,
    session_id,
    seat_key,
    sector_name,
    row_name,
    seat_number,
    tier_id,
    status
)
SELECT
    gen_random_uuid(),
    'fe000007-0000-7000-8000-000000000002'::uuid,
    chr(64 + r) || lpad(s::text, 2, '0'),
    'Parket',
    chr(64 + r),
    lpad(s::text, 2, '0'),
    'fe000007-0000-7000-8000-000000000005'::uuid,
    'available'
FROM generate_series(1, 10) r,
     generate_series(1, 26) s
ON CONFLICT (session_id, seat_key) DO NOTHING;
