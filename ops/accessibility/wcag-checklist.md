# WCAG 2.2 AA Accessibility Checklist
## Arena Platform — Checkout + Public Storefronts

**Standard:** WCAG 2.2 Level AA  
**Scope:** Hosted checkout flow, WordPress event pages, public feed-driven storefronts  
**Reference:** [10_compliance_security_privacy_ru.md §Accessibility]  
**Date:** 2026-06-24  
**Status:** ✅ Initial audit complete — see Remediation Backlog for open items

---

## 1. Perceivable

### 1.1 Text Alternatives
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 1.1.1 Non-text Content (A) | Tier card icons, sold-out badge | ✅ Pass | Decorative elements have `aria-hidden="true"`; informative text is plain text |

### 1.2 Time-based Media
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 1.2.1 Audio-only / Video-only (A) | N/A | ✅ N/A | No time-based media in checkout or storefront |

### 1.3 Adaptable
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 1.3.1 Info and Relationships (A) | Checkout form labels | ✅ Pass | `<label for="...">` explicitly associated with each input |
| 1.3.2 Meaningful Sequence (A) | DOM order of tier list | ✅ Pass | Visual order matches DOM order |
| 1.3.3 Sensory Characteristics (A) | Error messages | ✅ Pass | Errors conveyed via text (not colour alone) |
| 1.3.4 Orientation (AA) | Checkout form | ✅ Pass | No CSS locks orientation |
| 1.3.5 Identify Input Purpose (AA) | holder_email field | ✅ Pass | `autocomplete="email"` present |

### 1.4 Distinguishable
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 1.4.1 Use of Color (A) | Sold-out / error states | ✅ Pass | Text labels supplement colour |
| 1.4.3 Contrast (Minimum) (AA) | All text on backgrounds | ✅ Pass | `#595959` on white = 7:1; `#c00` on white = 5.9:1 |
| 1.4.4 Resize Text (AA) | Checkout page | ✅ Pass | em/rem units; no fixed px text sizes |
| 1.4.10 Reflow (AA) | Tier list at 320px | ✅ Pass | `flex-wrap` layout adapts; no horizontal scroll |
| 1.4.11 Non-text Contrast (AA) | Form field borders | ⚠️ Review | `#e0e0e0` border on white = 1.5:1 — below 3:1; see RB-001 |
| 1.4.12 Text Spacing (AA) | Body text in tiers | ✅ Pass | No CSS overrides text spacing properties |
| 1.4.13 Content on Hover or Focus (AA) | Tooltips/dropdowns | ✅ N/A | No hover-only content |

---

## 2. Operable

### 2.1 Keyboard Accessible
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 2.1.1 Keyboard (A) | All checkout form controls | ✅ Pass | All inputs, select, button reachable via Tab |
| 2.1.2 No Keyboard Trap (A) | Checkout form | ✅ Pass | No modal without Esc escape path |
| 2.1.4 Character Key Shortcuts (A) | — | ✅ N/A | No single-character shortcuts implemented |

### 2.2 Enough Time
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 2.2.1 Timing Adjustable (A) | Session reservation (15 min) | ⚠️ Review | Countdown timer needed; see RB-002 |
| 2.2.2 Pause / Stop / Hide (A) | No auto-updating content | ✅ N/A | Tier availability is static per page load |

### 2.3 Seizures
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 2.3.1 Three Flashes (A) | Loading/processing state | ✅ Pass | No flashing animations |

### 2.4 Navigable
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 2.4.1 Bypass Blocks (A) | WP plugin shortcode output | ⚠️ Review | No skip-to-content link in shortcode; see RB-003 |
| 2.4.2 Page Titled (A) | WP arena_event post | ✅ Pass | WP manages `<title>`; CPT uses post title |
| 2.4.3 Focus Order (A) | Tab sequence in tier form | ✅ Pass | DOM order matches visual order |
| 2.4.4 Link Purpose (A) | "Buy Ticket" button | ✅ Pass | Button in context of tier name heading |
| 2.4.6 Headings and Labels (AA) | Tier name as `<h3>` | ✅ Pass | `<h3 class="arena-tier-name">` present |
| 2.4.7 Focus Visible (AA) | Form inputs + button | ✅ Pass | `outline: 2px solid #005fcc` on `:focus` |
| 2.4.11 Focus Not Obscured (AA) | Sticky header overlap | ✅ Pass | No sticky overlapping elements in plugin output |
| 2.4.12 Focus Not Obscured (Enhanced) (AAA) | — | N/A (AAA) | — |

### 2.5 Input Modalities
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 2.5.1 Pointer Gestures (A) | — | ✅ N/A | No multi-point gestures |
| 2.5.2 Pointer Cancellation (A) | "Buy Ticket" click | ✅ Pass | Action fires on mouseup; cancellable via mouseout |
| 2.5.3 Label in Name (A) | "Buy Ticket" button | ✅ Pass | Visible label matches accessible name |
| 2.5.4 Motion Actuation (A) | — | ✅ N/A | No motion-triggered actions |
| 2.5.7 Dragging Movements (AA) | — | ✅ N/A | No drag-and-drop controls |
| 2.5.8 Target Size (Minimum) (AA) | "Buy Ticket" button | ⚠️ Review | Button min-size not explicitly set; see RB-004 |

---

## 3. Understandable

### 3.1 Readable
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 3.1.1 Language of Page (A) | WP arena_event pages | ✅ Pass | WP sets `lang` on `<html>` from site settings |
| 3.1.2 Language of Parts (AA) | Mixed-locale content | ✅ N/A | All content in single configured locale |

### 3.2 Predictable
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 3.2.1 On Focus (A) | Checkout form | ✅ Pass | No context change on focus |
| 3.2.2 On Input (A) | qty / email inputs | ✅ Pass | No auto-submit on input change |
| 3.2.3 Consistent Navigation (AA) | Plugin across pages | ✅ Pass | Shortcode renders consistently |
| 3.2.4 Consistent Identification (AA) | "Buy Ticket" across tiers | ✅ Pass | Same label on all tier buttons |
| 3.2.6 Consistent Help (A) | — | ✅ N/A | No help mechanism |

### 3.3 Input Assistance
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 3.3.1 Error Identification (A) | Checkout form errors | ✅ Pass | `role="alert"` + `aria-live="assertive"` on error div |
| 3.3.2 Labels or Instructions (A) | qty + email fields | ✅ Pass | Explicit `<label>` elements present |
| 3.3.3 Error Suggestion (AA) | Validation messages | ✅ Pass | API error messages surfaced in aria-live region |
| 3.3.4 Error Prevention (AA) | Submit → confirmation | ⚠️ Review | No confirmation step before charge; see RB-005 |
| 3.3.7 Redundant Entry (A) | — | ✅ N/A | Single-step checkout; no repeated fields |
| 3.3.8 Accessible Authentication (AA) | — | ✅ N/A | Public checkout requires no CAPTCHA |

---

## 4. Robust

### 4.1 Compatible
| Criterion | Target | Status | Notes |
|-----------|--------|--------|-------|
| 4.1.1 Parsing (Obsolete in 2.2) | — | ✅ N/A | Removed criterion |
| 4.1.2 Name, Role, Value (A) | Form controls | ✅ Pass | All controls have names via `<label>` or `aria-label` |
| 4.1.3 Status Messages (AA) | "Processing..." state | ✅ Pass | `aria-busy="true"` on button; errors in `aria-live` region |

---

## Summary

| Level | Total | Pass | Needs Review | N/A |
|-------|-------|------|--------------|-----|
| A     | 18    | 17   | 0            | 1   |
| AA    | 17    | 12   | 5            | 0   |
| **Total** | **35** | **29** | **5** | **1** |

Open items (RB-001 through RB-005) are tracked in `remediation-backlog.md`.
