# WCAG 2.2 AA Remediation Backlog
## Arena Platform — Checkout + Public Storefronts

**Source:** `wcag-checklist.md` + `wp-demo-test-plan.md`  
**Created:** 2026-06-24  
**Status:** Open — feeds into Wave 18 UI milestone

---

## Open Items

### RB-001 — Form Field Border Contrast (WCAG 1.4.11)

| Field | Value |
|-------|-------|
| **Criterion** | 1.4.11 Non-text Contrast (AA) |
| **Severity** | Serious |
| **Component** | `class-checkout.php` — `.arena-tier` border |
| **Issue** | `border: 1px solid #e0e0e0` on white background = 1.5:1 contrast ratio (required ≥ 3:1) |
| **Fix** | Change border colour to `#767676` (3.0:1 on white) or darker |
| **Effort** | 1 line CSS change |
| **Target milestone** | Wave 18 |
| **Status** | 🔴 Open |

**Proposed fix:**
```css
/* Before */
.arena-tier { border: 1px solid #e0e0e0; }

/* After — 3.0:1 on white */
.arena-tier { border: 1px solid #767676; }
```

---

### RB-002 — Reservation Timer Accessibility (WCAG 2.2.1)

| Field | Value |
|-------|-------|
| **Criterion** | 2.2.1 Timing Adjustable (A) |
| **Severity** | Serious |
| **Component** | Checkout session (15-minute reservation) |
| **Issue** | When a checkout session expires, the user is not warned in advance (no countdown timer with extend option in WP plugin storefront) |
| **Fix** | Add countdown timer to shortcode output with at least 20-second warning before expiry; provide "Extend" button that calls the API; announce via `aria-live="polite"` |
| **Effort** | Medium (JS + API endpoint for session extension) |
| **Target milestone** | Wave 18 |
| **Status** | 🔴 Open |

---

### RB-003 — Skip Navigation Link (WCAG 2.4.1)

| Field | Value |
|-------|-------|
| **Criterion** | 2.4.1 Bypass Blocks (A) |
| **Severity** | Moderate |
| **Component** | `[arena_event_tiers]` shortcode output |
| **Issue** | Users navigating by keyboard must Tab through all WP theme navigation before reaching the tier list; no skip link provided by the plugin |
| **Fix** | Inject a visually-hidden skip link at the top of shortcode output: `<a class="arena-skip-link" href="#arena-tiers-main">Skip to ticket tiers</a>` and add `id="arena-tiers-main"` to the `.arena-tiers` wrapper |
| **Effort** | Small (2 HTML lines + 3 CSS lines) |
| **Target milestone** | Wave 18 |
| **Status** | 🔴 Open |

**Proposed CSS:**
```css
.arena-skip-link {
  position: absolute;
  top: -40px;
  left: 0;
  background: #005fcc;
  color: #fff;
  padding: 8px;
  z-index: 1000;
  transition: top 0.2s;
}
.arena-skip-link:focus {
  top: 0;
}
```

---

### RB-004 — Button Target Size (WCAG 2.5.8)

| Field | Value |
|-------|-------|
| **Criterion** | 2.5.8 Target Size Minimum (AA) |
| **Severity** | Moderate |
| **Component** | "Buy Ticket" button in `.arena-checkout-form` |
| **Issue** | Button height may be < 24px on some WP themes that set aggressive `line-height` resets |
| **Fix** | Add `min-height: 44px; padding: 8px 16px;` to `.arena-checkout-btn` CSS |
| **Effort** | 1 line CSS change |
| **Target milestone** | Wave 18 |
| **Status** | 🔴 Open |

**Proposed fix:**
```css
.arena-checkout-btn {
  cursor: pointer;
  min-height: 44px;
  padding: 8px 16px;
  margin-top: .5em;
}
```

---

### RB-005 — Error Prevention for Payments (WCAG 3.3.4)

| Field | Value |
|-------|-------|
| **Criterion** | 3.3.4 Error Prevention (Legal, Financial, Data) (AA) |
| **Severity** | Serious |
| **Component** | Checkout submit flow |
| **Issue** | Once "Buy Ticket" is clicked and checkout session is created, there is no confirmation step — users cannot review, correct, or cancel the order before the charge is initiated |
| **Fix** | Add a review step: after initial API call, show an "Order Summary" modal/section with the tier name, quantity, total price, and email; require an explicit "Confirm Purchase" button. OR implement the cancellation endpoint and surface it as "Cancel" on the review step. |
| **Effort** | Medium (UI + API cancellation endpoint) |
| **Target milestone** | Wave 18 |
| **Status** | 🔴 Open |

---

## Closed Items

*(None yet — this is the initial audit)*

---

## Metrics

| Severity | Count |
|----------|-------|
| 🔴 Critical | 0 |
| 🔴 Serious | 3 (RB-001, RB-002, RB-005) |
| 🟡 Moderate | 2 (RB-003, RB-004) |
| 🟢 Minor | 0 |
| **Total** | **5** |
