#!/usr/bin/env bash
# Run the restart probe with a brand-new unique key so we know the full
# write path (audit row create + outbox row create + idempotency row create)
# is exercised, not just the cached-replay path.
IDEM_KEY="FRESH_KEY_$$_$RANDOM"
MSG="FRESH_MSG_$$_$RANDOM"
IDEM_KEY="$IDEM_KEY" MSG="$MSG" bash "$(dirname "$0")/restart_probe.sh"
