-- control_only_events.sql — which events are control events (MA-40).
--
-- A control event is an event that holds a live (deleted_at IS NULL) session
-- with sales_mode = 'control_only' (migration 0228). There is no column for
-- it: the migration's triggers keep every live session of such an event
-- control_only, so one live control_only session decides.

-- name: ListControlOnlyEventIDs :many
-- ListControlOnlyEventIDs returns, of the given events, the ids of those that
-- hold a live control_only session. One round trip for a whole list. The
-- caller has already authorized the ids (an organizer list, an event read).
SELECT DISTINCT s.event_id
FROM   sessions s
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.sales_mode = 'control_only';
