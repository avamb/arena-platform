# P3 — organizer-specific sales terms: implementation contract

## Status

P2 is already implemented in `master`: per-channel consent flag, buyer checkbox,
server-side enforcement, and immutable checkout-consent evidence. Until P3 has an
published document rendering, the widget must keep linking to platform terms and
must not label them as organizer-authored terms.

## Non-negotiable publication rule

A public organizer terms page may exist only for an immutable rendering created
from a published template. Drafts, placeholders, generated legal prose and
organizer free text are never public and never selectable at checkout.

## Data model

1. `legal_documents`: superadmin-owned template version, locale, jurisdiction,
   refund mode, content, status (`draft`, `published`, `retired`), content hash,
   author and approval audit fields.
2. `legal_renderings`: immutable rendered document bound to organization,
   optional promoter, channel, document version, locale, seller-data snapshot,
   refund-mode snapshot, rendered body, content hash, generated timestamp.
3. `checkout_consents`: add nullable `legal_rendering_id`; old P2 rows remain
   valid platform-terms evidence. New rows reference a rendering only when it
   was present in the public feed and accepted at checkout.

## API and authorization

- Only superadmins may create, edit, approve or retire templates. Approval must
  create an audit event with document id, version and content hash.
- Organizers can read the rendering selected for their channel, but cannot edit
  legal text.
- `GET /v1/public/pages/{slug}/terms` returns the immutable rendering only.
  Missing, draft, retired or mismatched documents return 404 without legal text.
- The public feed returns organizer `terms_url` and `terms_version` only when a
  current published rendering is available; otherwise it keeps the P2 platform
  terms URL and version.

## Rendering inputs

Only explicit values may substitute placeholders: `sellerinfo.Profile`, public
seller identity, promoter name when applicable, channel, jurisdiction, locale,
and an enumerated refund mode. Reject unknown placeholders and missing required
seller data. Free-text refund language belongs only to a superadmin-approved
legal document version, never to an organizer input.

## Release gates

- migration and rollback-safe nullability for existing P2 evidence;
- template approval, rendering immutability, cross-organization isolation and
  public 404 tests;
- public-feed/checkout consistency test proving the accepted version equals the
  page rendering;
- translations/legal text supplied and signed off by counsel for every enabled
  jurisdiction and language;
- no document enabled by default.

## Required owner/legal inputs before activation

- jurisdictions to enable first;
- counsel-approved text and version identifier per locale;
- exact permitted refund modes and their jurisdiction applicability;
- retention period and legal owner of the template approval process.