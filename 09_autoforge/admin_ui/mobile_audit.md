# Admin-web mobile responsiveness audit (Wave M-1)

> Status: baseline / **worklist for Wave M-2..M-N**.
> Author: AutoForge (#261 — Wave M-1).
> Date: 2026-06-29.
>
> The admin shell at `apps/admin-web` ships today as a desktop-only layout
> (fixed 240 px sidebar + 1fr main pane, multi-column tables, right-side
> drawers). This document is the **baseline** map of every existing route
> against every role preset and the **minimum viewport width currently
> supported**. The columns marked "Target" capture the M-series goal: full
> usability at the **360 × 640** smallest-supported handset and
> **1280 × 800** desktop reference window.
>
> Subsequent Wave M tickets (M-2 onwards) consume this matrix as their
> work backlog: each "current min viewport" row > `md` (768 px) becomes a
> follow-up to redesign with the primitives introduced under this ticket
> (`<ResponsiveTable>`, `<ResponsiveDrawer>`).

---

## 1. Canonical breakpoints

| Token | Min width | Use                                                         |
| ----- | --------- | ----------------------------------------------------------- |
| `sm`  | 640 px    | Small phone landscape / large phone portrait                |
| `md`  | 768 px    | **Desktop/mobile cut.** Tablets portrait, small laptops     |
| `lg`  | 1024 px   | Tablets landscape, small laptops                            |
| `xl`  | 1280 px   | Standard desktop. Current admin-web design target.          |

The Tailwind defaults are used verbatim even though `apps/admin-web` does
not depend on Tailwind today; this keeps the M-series goal aligned with
the rest of the platform documentation (master spec, architecture brief)
and with the storefront / public-site work that does ship Tailwind.

**Source of truth:** `apps/admin-web/src/components/layout/breakpoints.ts`.
Tests in `apps/admin-web/src/components/layout/layout.test.tsx` pin the
numeric values to guard against drift.

`md` (**768 px**) is the desktop/mobile cut. At or above `md` the admin
shell renders the full chrome (sidebar nav, multi-column `<table>`s,
right-side drawers). Below `md` surfaces collapse to a single-column
mobile shell:
- top-bar collapses; sidebar becomes a hamburger sheet;
- `<ResponsiveTable>` renders the same column list as a stacked card list;
- `<ResponsiveDrawer>` expands to a full-screen sheet with a back arrow.

---

## 2. Role presets

The admin shell is permission-driven (see `src/lib/auth/navConfig.ts`).
For the audit we use four canonical role presets to span the perms space:

| Preset                    | Scope kinds  | Permission set (representative)                                                |
| ------------------------- | ------------ | ------------------------------------------------------------------------------ |
| `platform_superadmin`     | global       | `superadmin.*`, `network.*`, `org.*`, `geo.admin`, `payment_config.*` …       |
| `network_operator`        | network      | `network.read`, `network.view_sales`, `network.view_reports`, `channel.*`     |
| `organization_admin`      | organization | `org.read`, `org.update`, `event.*`, `venue.*`, `channel.*`, `payment_config.*` |
| `org_pos_cashier`         | organization | `pos.execute`, `event.read`, `org.read`                                       |

Operator-only / scoped surfaces appear only when the active scope's kind
matches `NavEntry.scopeKinds`. The matrix below collapses presets per
route to the **highest fidelity** view each preset can reach; if a route
is invisible to a preset that is recorded as `n/a`.

---

## 3. Route × role × current min viewport matrix

> **Legend:**
> *Min viewport* = smallest viewport width (px) at which the surface
> remains usable today (no horizontal scroll on primary content, no
> overlapping controls, all rows reachable). Anything > 768 means the
> surface fails at the canonical desktop/mobile cut.
>
> *Layout shape* = the dominant component(s) on the route.

| Route                  | File                                  | Layout shape                              | platform_superadmin | network_operator | organization_admin | org_pos_cashier | Current min viewport | M-series target           | Wave M ticket (planned) |
| ---------------------- | ------------------------------------- | ----------------------------------------- | ------------------- | ---------------- | ------------------ | --------------- | --------------------- | -------------------------- | ----------------------- |
| `/`                    | `routes/index.tsx`                    | Marketing-style cards + nav cues          | full                | full             | full               | full            | ~640 (already fluid)  | 360 (light polish)         | M-2                     |
| `/login`               | `routes/login.tsx`                    | Centered form card                        | full                | full             | full               | full            | 360 (already mobile)  | 360 (no work)              | —                       |
| `/networks`            | `routes/networks.tsx`                 | Filter bar + `<table>` + create modal     | full                | full             | n/a (scope hides)  | n/a             | ~1100                 | 360 (ResponsiveTable)       | M-3                     |
| `/networks/{id}`       | `routes/networkDetail.tsx`            | Two-column overview + nested tables       | full                | full             | n/a                | n/a             | ~1280                 | 360 (single-col stack)     | M-3                     |
| `/users`               | `routes/users.tsx`                    | Filter bar + wide `<table>` + drawer      | full                | n/a              | n/a                | n/a             | ~1180                 | 360 (Table+Drawer)         | M-4                     |
| `/organizations`       | `routes/organizations.tsx`            | Filter bar + `<table>` + multi-tab drawer | full                | n/a              | full               | n/a             | ~1280                 | 360 (Table+Drawer)         | M-4                     |
| `/venues`              | `routes/venues.tsx`                   | Filter bar + 8-col `<table>` + form modal | full                | full             | full               | n/a             | ~1280                 | 360 (ResponsiveTable)      | M-5                     |
| `/orders`              | `routes/orders.tsx`                   | Filter bar + `<table>` + detail drawer    | full                | n/a              | n/a                | n/a             | ~1180                 | 360 (Table+Drawer)         | M-6                     |
| `/tickets`             | `routes/tickets.tsx`                  | Filter bar + `<table>`                    | full                | n/a              | n/a                | n/a             | ~1180                 | 360 (ResponsiveTable)      | M-6                     |
| `/refunds`             | `routes/refunds.tsx`                  | Filter bar + `<table>`                    | full                | n/a              | n/a                | n/a             | ~1100                 | 360 (ResponsiveTable)      | M-6                     |
| `/channels`            | `routes/channels.tsx`                 | Filter bar + `<table>`                    | full                | full             | full               | n/a             | ~1100                 | 360 (ResponsiveTable)      | M-7                     |
| `/payments`            | `routes/payments.tsx`                 | Filter bar + `<table>` + drawer           | full                | full             | full               | n/a             | ~1180                 | 360 (Table+Drawer)         | M-7                     |
| `/events`              | `routes/legacyPlaceholders.tsx`       | Shell placeholder                          | full                | full             | full               | full            | 360 (already fluid)   | 360 (no work)              | —                       |
| `/reports`             | `routes/legacyPlaceholders.tsx`       | Shell placeholder                          | full                | full             | full               | n/a             | 360 (already fluid)   | 360 (no work)              | —                       |
| `/content`             | `routes/legacyPlaceholders.tsx`       | Shell placeholder                          | full                | full             | full               | n/a             | 360 (already fluid)   | 360 (no work)              | —                       |
| `/pos`                 | `routes/legacyPlaceholders.tsx`       | Shell placeholder                          | full                | full             | n/a                | full            | 360 (already fluid)   | **must reach 360** when    | M-9 (POS dedicated)     |
|                        |                                       |                                            |                     |                  |                    |                 |                       | real POS lands             |                         |
| `/audit`               | `routes/audit.tsx`                    | Filter bar + `<table>` + JSON drawer       | full                | n/a              | n/a                | n/a             | ~1180                 | 360 (Table+Drawer)         | M-6                     |
| `/observability`       | `routes/observability.tsx`            | Probe panels + link cards                  | full                | n/a              | n/a                | n/a             | ~900                  | 360 (single-col stack)     | M-8                     |
| `/geo`                 | not yet implemented                   | n/a                                        | full (planned)      | n/a              | n/a                | n/a             | n/a                   | 360 from day one           | M-10                    |

### 3.1. Cross-cutting chrome (applies to every authenticated route)

| Surface                          | Defined in                                              | Current min viewport | Target | Wave M ticket |
| -------------------------------- | ------------------------------------------------------- | --------------------- | ------ | ------------- |
| Sidebar nav (240 px fixed)       | `src/components/AppLayout.tsx`                          | ~1024                 | 360 (collapses to hamburger sheet < md) | M-2 |
| Top bar (scope selector + reason badge + locale switcher) | `src/components/AppLayout.tsx#AuthenticatedTopBar` | ~960 | 360 (wraps, dense) | M-2 |
| Reason-prompt modal              | `src/components/ReasonPromptModal.tsx`                  | ~600                  | 360                                     | M-2 |
| Dev diagnostics panel (dev only) | `src/components/DevDiagnosticsPanel.tsx`                | ~640                  | 360 (collapsed by default on mobile)    | M-8 |
| Forbidden page                   | `src/components/Forbidden.tsx`                          | 360 (already mobile)  | —                                       | —   |
| Loading screen                   | `src/components/LoadingScreen.tsx`                      | 360 (already mobile)  | —                                       | —   |

### 3.2. Drawers (sub-surfaces inside routes)

| Drawer                                 | Host route        | Current behaviour below `md`                                  | M-1 primitive       | Wave M ticket |
| -------------------------------------- | ----------------- | ------------------------------------------------------------- | -------------------- | ------------- |
| Organization detail drawer (6 tabs)    | `/organizations`  | Right-side panel overflows viewport; tabs scroll horizontally | `<ResponsiveDrawer>` | M-4           |
| Order detail drawer                    | `/orders`         | Right-side panel overflows viewport                           | `<ResponsiveDrawer>` | M-6           |
| Payment config drawer                  | `/payments`       | Right-side panel overflows viewport                           | `<ResponsiveDrawer>` | M-7           |
| Audit detail JSON drawer               | `/audit`          | Right-side panel overflows viewport                           | `<ResponsiveDrawer>` | M-6           |
| Network detail drawer (in tables)      | `/networks`       | Right-side panel overflows viewport                           | `<ResponsiveDrawer>` | M-3           |
| Support drawer                         | (cross-cutting)   | Right-side panel mostly OK at < md but action bar wraps       | `<ResponsiveDrawer>` | M-2           |

---

## 4. Worklist for M-2..M-N

Generated directly from the matrix above. Each ticket should:

1. Adopt the new primitives (`<ResponsiveTable>`, `<ResponsiveDrawer>`)
   without changing the underlying data contract.
2. Take 360 × 640 and 1280 × 800 screenshots (Wave M-8 gate).
3. Be permission-aware: hide actions not granted to the active scope
   even when they fit on screen.

| Wave   | Scope                                                                         | Owner / status  |
| ------ | ----------------------------------------------------------------------------- | --------------- |
| **M-1**| Layout primitives + this audit baseline                                       | **done (#261)** |
| M-2    | Cross-cutting shell: sidebar -> hamburger, top bar wrap, reason modal at 360  | pending         |
| M-3    | `/networks` + `/networks/{id}`                                                | pending         |
| M-4    | `/users` and `/organizations` (incl. organization drawer's 6 tabs)            | pending         |
| M-5    | `/venues`                                                                     | pending         |
| M-6    | `/orders`, `/tickets`, `/refunds`, `/audit`                                   | pending         |
| M-7    | `/channels`, `/payments`                                                      | pending         |
| M-8    | Observability route + dev-diagnostics-panel mobile collapsing; screenshot CI  | pending         |
| M-9    | POS mode (when the real POS executes lands)                                   | deferred        |
| M-10   | Geo registry (greenfield, built mobile-first from day one)                    | pending         |

---

## 5. Verification gates (Wave M-8)

Every Wave M ticket MUST attach:

- a 360 × 640 screenshot of the route post-change;
- a 1280 × 800 screenshot of the same route post-change;
- proof that `npm --prefix apps/admin-web run type-check` and
  `npm --prefix apps/admin-web test -- --run` still pass.

These artifacts are tracked in the PR body; CI does not currently capture
screenshots automatically (that is the M-8 deliverable).
