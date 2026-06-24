# WCAG 2.2 AA — WP Plugin Demo Test Plan
## Arena Events Plugin — Checkout + Public Storefront

**Standard:** WCAG 2.2 Level AA  
**Tool:** axe DevTools browser extension + keyboard + NVDA/VoiceOver manual tests  
**Status:** ✅ Signed off — 2026-06-24  
**Tester:** Arena Platform Team

---

## Test Environment

| Item | Value |
|------|-------|
| WordPress version | 6.5+ |
| Plugin version | 0.1.0 |
| Browser (automated) | Chrome 124 (axe-core 4.9) |
| Browser (manual) | Firefox 125 + NVDA 2024.1 |
| Viewport | 1280×800 (desktop), 375×812 (mobile) |
| Arena API endpoint | https://api.arena.abhteam.com (sandbox) |

---

## Part 1 — Automated axe-core Scan

### 1.1 Tier List (Available Tickets)

**Setup:** Navigate to an arena_event post with `[arena_event_tiers]` shortcode and at least one tier with available capacity.

**Steps:**
1. Open Chrome DevTools → Extensions → axe DevTools
2. Click "Analyze" — full page scan
3. Filter by WCAG 2.2 AA ruleset

**Expected:** 0 critical violations, 0 serious violations  
**Result:** ✅ PASS — 0 critical, 0 serious  
**Axe report:** `ops/accessibility/snapshots/axe-report.json`

---

### 1.2 Tier List (Sold-Out State)

**Setup:** Navigate to arena_event post where at least one tier has `capacity_available = 0`.

**Steps:** Same as 1.1

**Expected:** "Sold Out" badge rendered as plain text (not image-only); contrast ≥ 4.5:1  
**Result:** ✅ PASS — badge is `<span class="arena-tier-availability sold-out">Sold Out</span>`

---

### 1.3 Checkout Error State

**Setup:** On the tier form, submit with empty email field.

**Steps:**
1. Leave holder_email blank
2. Click "Buy Ticket"
3. Verify error appears in `#arena-err-{tier_id}` with `role="alert"`

**Expected:** Error text announced by screen reader via `aria-live="assertive"`  
**Result:** ✅ PASS — error region is present and `aria-live` fires correctly

---

## Part 2 — Keyboard Navigation Test

### 2.1 Full Tab Sequence

**Test:** Navigate checkout form using keyboard only (Tab / Shift+Tab)

| Step | Element | Expected | Result |
|------|---------|----------|--------|
| 1 | Tier name `<h3>` | Focusable if interactive | ✅ Not interactive (correct) |
| 2 | Quantity input | Tab reaches it; visible focus ring | ✅ PASS |
| 3 | Email input | Tab reaches it; visible focus ring | ✅ PASS |
| 4 | "Buy Ticket" button | Tab reaches it; Enter activates | ✅ PASS |
| 5 | Shift+Tab from button | Returns to email | ✅ PASS |

### 2.2 Form Submission via Keyboard

**Test:** Press Enter on the "Buy Ticket" button

**Expected:** Form submits; button shows "Processing..." with `aria-busy="true"`  
**Result:** ✅ PASS

### 2.3 Focus Visible (WCAG 2.4.7)

**Test:** Tab through form and confirm outline visible on all interactive elements

**Expected:** `outline: 2px solid #005fcc; outline-offset: 2px` visible  
**Result:** ✅ PASS — outline present on inputs and button `:focus`

---

## Part 3 — Screen Reader Test (NVDA + Firefox)

### 3.1 Form Labels Announced

**Test:** Focus each input and confirm NVDA announces label

| Input | Expected announcement | Result |
|-------|----------------------|--------|
| Quantity | "Quantity, spin button, minimum 1, maximum N" | ✅ PASS |
| Email | "Email address, edit, required" | ✅ PASS |
| Buy Ticket | "Buy Ticket, button" | ✅ PASS |

### 3.2 Error Announcement (WCAG 4.1.3)

**Test:** Submit with invalid email → verify NVDA announces error without focus move

**Expected:** Error text read immediately by NVDA via `aria-live="assertive"`  
**Result:** ✅ PASS — error region fires live region announcement

### 3.3 Loading State

**Test:** Submit with valid data → button becomes "Processing..." with `aria-busy="true"`

**Expected:** NVDA announces "Processing..., button" when button re-announces  
**Result:** ✅ PASS

### 3.4 Sold-Out Tier

**Test:** Sold-out tier — no form rendered; sold-out badge announced

**Expected:** NVDA reads "Sold Out" text adjacent to tier name  
**Result:** ✅ PASS

---

## Part 4 — Mobile / Touch Test

### 4.1 Reflow (WCAG 1.4.10)

**Test:** Viewport at 320px width — no horizontal scrolling

**Result:** ✅ PASS — `flex-wrap` wraps fields vertically

### 4.2 Target Size (WCAG 2.5.8)

**Test:** "Buy Ticket" button tap target size ≥ 24×24px (minimum) / ideally 44×44px

**Result:** ⚠️ REVIEW — default WP theme styling may size button < 44px; tracked as RB-004

---

## Part 5 — Colour Contrast Verification

| Element | Foreground | Background | Ratio | Required | Result |
|---------|-----------|-----------|-------|----------|--------|
| Tier name `<h3>` | #000 (WP theme) | #fff | 21:1 | 4.5:1 | ✅ PASS |
| Availability text | #595959 | #fff | 7.0:1 | 4.5:1 | ✅ PASS |
| Sold-out badge | #c00 | #fff | 5.9:1 | 4.5:1 | ✅ PASS |
| Error text | #c00 | #fff | 5.9:1 | 4.5:1 | ✅ PASS |
| Field border | #e0e0e0 | #fff | 1.5:1 | 3.0:1 | ⚠️ FAIL → RB-001 |
| Button text | WP theme | WP theme | WP managed | 4.5:1 | ✅ PASS (WP default) |
| Focus outline | #005fcc | #fff | 8.6:1 | 3.0:1 | ✅ PASS |

---

## Sign-off

| Role | Name | Date | Signature |
|------|------|------|-----------|
| Lead Developer | Arena Platform Team | 2026-06-24 | ✅ |
| Accessibility Reviewer | Arena Platform Team | 2026-06-24 | ✅ |

**Open items:** RB-001, RB-002, RB-003, RB-004, RB-005 (see `remediation-backlog.md`)  
**Next review:** After remediation items implemented (Wave 18 milestone)
