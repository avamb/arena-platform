/**
 * generate-snapshots.js
 *
 * Generates static HTML snapshots that mirror the output of the Arena Events
 * WordPress plugin's render_tiers_html() method. These files are scanned by
 * axe-core in CI without requiring a live WordPress instance.
 *
 * Usage: node ops/accessibility/generate-snapshots.js
 *
 * Output files (created in ops/accessibility/snapshots/):
 *   - checkout-tiers.html      — two available tiers with checkout forms
 *   - checkout-sold-out.html   — one available + one sold-out tier
 *   - checkout-error-state.html — tier form in error state (aria-live fired)
 */

'use strict';

const fs   = require('fs');
const path = require('path');

const snapshotsDir = path.join(__dirname, 'snapshots');
fs.mkdirSync(snapshotsDir, { recursive: true });

// ─────────────────────────────────────────────────────────────────────────────
// Shared wrapper — mirrors WP page context (lang, <title>, etc.)
// ─────────────────────────────────────────────────────────────────────────────
function page(title, bodyContent) {
  return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>${title} — Arena Events</title>
  <style>
    /* Mirror of class-checkout.php get_checkout_inline_css() */
    .arena-tiers{margin:1.5em 0}
    .arena-tier{border:1px solid #767676;padding:1em;margin:.75em 0;border-radius:4px}
    .arena-tier-name{margin:0 0 .25em}
    .arena-tier-price{font-weight:bold}
    .arena-tier-availability{color:#595959;font-size:.9em;margin-left:.5em}
    .arena-tier-availability.sold-out{color:#c00}
    .arena-checkout-form{margin-top:.75em}
    .arena-field{display:flex;flex-direction:column;gap:.25em;margin-bottom:.5em}
    .arena-field label{font-weight:600;font-size:.9em}
    .arena-qty{width:60px}
    .arena-email{width:100%;max-width:320px}
    .arena-qty:focus,.arena-email:focus,.arena-checkout-btn:focus{outline:2px solid #005fcc;outline-offset:2px}
    .arena-checkout-btn{cursor:pointer;min-height:44px;padding:8px 16px;margin-top:.5em}
    .arena-error{color:#c00;margin:.5em 0;font-size:.9em}
    .arena-error:empty{display:none}
  </style>
</head>
<body>
  <a class="arena-skip-link" href="#arena-tiers-main">Skip to ticket tiers</a>
  <main id="arena-tiers-main">
    <h1>${title}</h1>
    ${bodyContent}
  </main>
</body>
</html>`;
}

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot 1: checkout-tiers.html — two available tiers
// ─────────────────────────────────────────────────────────────────────────────
const tiersHtml = `
<div class="arena-tiers" data-event-id="01906a1b-0000-7000-0000-000000000001">
  <div class="arena-tiers-list">

    <!-- Tier 1: General Admission -->
    <div class="arena-tier">
      <div class="arena-tier-info">
        <h3 class="arena-tier-name">General Admission</h3>
        <span class="arena-tier-price">USD 25.00</span>
        <span class="arena-tier-availability">50 remaining</span>
      </div>
      <form class="arena-checkout-form"
            id="arena-form-tier-001"
            aria-label="Checkout form for General Admission"
            data-rest-url="/wp-json/arena-events/v1/checkout/start"
            data-nonce="test-nonce-001"
            data-error-id="arena-err-tier-001"
            novalidate>
        <input type="hidden" name="tier_id" value="tier-001">
        <input type="hidden" name="session_id" value="session-001">
        <div class="arena-field">
          <label for="arena-qty-tier-001">Quantity</label>
          <input type="number" id="arena-qty-tier-001" name="qty"
                 value="1" min="1" max="10" class="arena-qty"
                 aria-describedby="arena-err-tier-001">
        </div>
        <div class="arena-field">
          <label for="arena-email-tier-001">Email address</label>
          <input type="email" id="arena-email-tier-001" name="holder_email"
                 placeholder="you@example.com" required aria-required="true"
                 class="arena-email" autocomplete="email"
                 aria-describedby="arena-err-tier-001">
        </div>
        <div id="arena-err-tier-001" class="arena-error"
             role="alert" aria-live="assertive" aria-atomic="true"></div>
        <button type="submit" class="arena-checkout-btn">Buy Ticket</button>
      </form>
    </div>

    <!-- Tier 2: VIP -->
    <div class="arena-tier">
      <div class="arena-tier-info">
        <h3 class="arena-tier-name">VIP</h3>
        <span class="arena-tier-price">USD 75.00</span>
        <span class="arena-tier-availability">10 remaining</span>
      </div>
      <form class="arena-checkout-form"
            id="arena-form-tier-002"
            aria-label="Checkout form for VIP"
            data-rest-url="/wp-json/arena-events/v1/checkout/start"
            data-nonce="test-nonce-002"
            data-error-id="arena-err-tier-002"
            novalidate>
        <input type="hidden" name="tier_id" value="tier-002">
        <input type="hidden" name="session_id" value="session-001">
        <div class="arena-field">
          <label for="arena-qty-tier-002">Quantity</label>
          <input type="number" id="arena-qty-tier-002" name="qty"
                 value="1" min="1" max="10" class="arena-qty"
                 aria-describedby="arena-err-tier-002">
        </div>
        <div class="arena-field">
          <label for="arena-email-tier-002">Email address</label>
          <input type="email" id="arena-email-tier-002" name="holder_email"
                 placeholder="you@example.com" required aria-required="true"
                 class="arena-email" autocomplete="email"
                 aria-describedby="arena-err-tier-002">
        </div>
        <div id="arena-err-tier-002" class="arena-error"
             role="alert" aria-live="assertive" aria-atomic="true"></div>
        <button type="submit" class="arena-checkout-btn">Buy Ticket</button>
      </form>
    </div>

  </div>
</div>`;

fs.writeFileSync(path.join(snapshotsDir, 'checkout-tiers.html'), page('Rock Concert 2026', tiersHtml));
console.log('Generated: checkout-tiers.html');

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot 2: checkout-sold-out.html — one available + one sold-out
// ─────────────────────────────────────────────────────────────────────────────
const soldOutHtml = `
<div class="arena-tiers" data-event-id="01906a1b-0000-7000-0000-000000000002">
  <div class="arena-tiers-list">

    <!-- Tier 1: General Admission (sold out) -->
    <div class="arena-tier">
      <div class="arena-tier-info">
        <h3 class="arena-tier-name">General Admission</h3>
        <span class="arena-tier-price">USD 25.00</span>
        <span class="arena-tier-availability sold-out">Sold Out</span>
      </div>
      <!-- No form rendered for sold-out tier -->
    </div>

    <!-- Tier 2: VIP (available) -->
    <div class="arena-tier">
      <div class="arena-tier-info">
        <h3 class="arena-tier-name">VIP</h3>
        <span class="arena-tier-price">USD 75.00</span>
        <span class="arena-tier-availability">5 remaining</span>
      </div>
      <form class="arena-checkout-form"
            id="arena-form-tier-003"
            aria-label="Checkout form for VIP"
            data-rest-url="/wp-json/arena-events/v1/checkout/start"
            data-nonce="test-nonce-003"
            data-error-id="arena-err-tier-003"
            novalidate>
        <input type="hidden" name="tier_id" value="tier-003">
        <input type="hidden" name="session_id" value="session-002">
        <div class="arena-field">
          <label for="arena-qty-tier-003">Quantity</label>
          <input type="number" id="arena-qty-tier-003" name="qty"
                 value="1" min="1" max="5" class="arena-qty"
                 aria-describedby="arena-err-tier-003">
        </div>
        <div class="arena-field">
          <label for="arena-email-tier-003">Email address</label>
          <input type="email" id="arena-email-tier-003" name="holder_email"
                 placeholder="you@example.com" required aria-required="true"
                 class="arena-email" autocomplete="email"
                 aria-describedby="arena-err-tier-003">
        </div>
        <div id="arena-err-tier-003" class="arena-error"
             role="alert" aria-live="assertive" aria-atomic="true"></div>
        <button type="submit" class="arena-checkout-btn">Buy Ticket</button>
      </form>
    </div>

  </div>
</div>`;

fs.writeFileSync(path.join(snapshotsDir, 'checkout-sold-out.html'), page('Jazz Night — Tickets', soldOutHtml));
console.log('Generated: checkout-sold-out.html');

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot 3: checkout-error-state.html — form in error state
// ─────────────────────────────────────────────────────────────────────────────
const errorStateHtml = `
<div class="arena-tiers" data-event-id="01906a1b-0000-7000-0000-000000000003">
  <div class="arena-tiers-list">
    <div class="arena-tier">
      <div class="arena-tier-info">
        <h3 class="arena-tier-name">General Admission</h3>
        <span class="arena-tier-price">USD 20.00</span>
        <span class="arena-tier-availability">20 remaining</span>
      </div>
      <form class="arena-checkout-form"
            id="arena-form-tier-004"
            aria-label="Checkout form for General Admission"
            data-rest-url="/wp-json/arena-events/v1/checkout/start"
            data-nonce="test-nonce-004"
            data-error-id="arena-err-tier-004"
            novalidate>
        <input type="hidden" name="tier_id" value="tier-004">
        <input type="hidden" name="session_id" value="session-003">
        <div class="arena-field">
          <label for="arena-qty-tier-004">Quantity</label>
          <input type="number" id="arena-qty-tier-004" name="qty"
                 value="1" min="1" max="10" class="arena-qty"
                 aria-describedby="arena-err-tier-004">
        </div>
        <div class="arena-field">
          <label for="arena-email-tier-004">Email address</label>
          <input type="email" id="arena-email-tier-004" name="holder_email"
                 placeholder="you@example.com" required aria-required="true"
                 class="arena-email" autocomplete="email"
                 aria-invalid="true"
                 aria-describedby="arena-err-tier-004">
        </div>
        <!-- Error state: message populated in aria-live region -->
        <div id="arena-err-tier-004" class="arena-error"
             role="alert" aria-live="assertive" aria-atomic="true">
          Please enter a valid email address.
        </div>
        <button type="submit" class="arena-checkout-btn">Buy Ticket</button>
      </form>
    </div>
  </div>
</div>`;

fs.writeFileSync(path.join(snapshotsDir, 'checkout-error-state.html'), page('Summer Festival', errorStateHtml));
console.log('Generated: checkout-error-state.html');

console.log('\nAll 3 HTML snapshots generated in ops/accessibility/snapshots/');
