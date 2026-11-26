-- hold_policy.sql — seat hold times and limits (spec 67 section 7.1,
-- migration 0226). A profile is copied into reservations.hold_policy when a
-- hold is created, so an edit here moves only new holds.

-- name: ListHoldPolicyProfiles :many
SELECT name, select_seconds, payment_seconds, extend_seconds, extend_count, paying_seconds,
       max_seats_per_hold, max_active_per_device, max_active_per_ip, rehold_pause_seconds,
       poll_interval_ms, updated_at, updated_by
FROM   hold_policy_profiles
ORDER  BY name;

-- name: GetHoldPolicyProfile :one
SELECT name, select_seconds, payment_seconds, extend_seconds, extend_count, paying_seconds,
       max_seats_per_hold, max_active_per_device, max_active_per_ip, rehold_pause_seconds,
       poll_interval_ms, updated_at, updated_by
FROM   hold_policy_profiles
WHERE  name = $1;

-- name: GetSessionHoldPolicyProfile :one
-- The profile of the session's event in one round trip; an event without
-- its own profile reads standard.
SELECT p.name, p.select_seconds, p.payment_seconds, p.extend_seconds, p.extend_count, p.paying_seconds,
       p.max_seats_per_hold, p.max_active_per_device, p.max_active_per_ip, p.rehold_pause_seconds,
       p.poll_interval_ms, p.updated_at, p.updated_by
FROM   sessions s
JOIN   events e ON e.id = s.event_id
JOIN   hold_policy_profiles p ON p.name = COALESCE(e.hold_profile, 'standard')
WHERE  s.id = $1;

-- name: UpdateHoldPolicyProfile :one
UPDATE hold_policy_profiles
SET    select_seconds        = $2,
       payment_seconds       = $3,
       extend_seconds        = $4,
       extend_count          = $5,
       paying_seconds        = $6,
       max_seats_per_hold    = $7,
       max_active_per_device = $8,
       max_active_per_ip     = $9,
       rehold_pause_seconds  = $10,
       poll_interval_ms      = $11,
       updated_by            = $12,
       updated_at            = now()
WHERE  name = $1
RETURNING name, select_seconds, payment_seconds, extend_seconds, extend_count, paying_seconds,
          max_seats_per_hold, max_active_per_device, max_active_per_ip, rehold_pause_seconds,
          poll_interval_ms, updated_at, updated_by;

-- name: GetEventHoldProfile :one
-- NULL means the event uses standard.
SELECT hold_profile FROM events WHERE id = $1;

-- name: SetEventHoldProfile :execrows
-- NULL returns the event to standard. updated_at is left alone: the hold
-- profile is not event content, and the bot's "changed elsewhere" check
-- compares updated_at.
UPDATE events SET hold_profile = $2 WHERE id = $1;
