"""Service tests. Importing `service.app` builds the module-level `app`, which needs a token or the
test-only switch; the tests pass their own token to `create_app`."""
import os

os.environ.setdefault("VENUE_GEN_ALLOW_NO_TOKEN", "test-only")
