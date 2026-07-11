# Arena Tickets Widget — Embed Guide

The Arena Tickets widget is a self-contained ES-module custom element (`<arena-tickets>`) that lets you embed a full ticket-purchase flow on any web page.

---

## Quick Start — Script Tag

```html
<script type="module"
  src="https://cdn.jsdelivr.net/gh/avamb/arena-platform@master/apps/widget/dist/v1/arena-tickets.js">
</script>

<arena-tickets feed-token="YOUR_FEED_TOKEN"></arena-tickets>
```

Replace `YOUR_FEED_TOKEN` with the feed token obtained from the Arena platform.

---

## Attributes

| Attribute     | Required | Default | Description |
|---------------|----------|---------|-------------|
| `feed-token`  | Yes      | —       | Feed token from the Arena platform. Identifies the event catalog to display. |
| `session-id`  | No       | —       | Pre-select a specific session. When omitted the user sees a session picker. |
| `locale`      | No       | `en`    | UI locale. Supported: `en`, `ru`, `cs`, `he`. |

---

## CDN Versioning Strategy

| CDN URL form | Behaviour |
|---|---|
| `@master/apps/widget/dist/v1/arena-tickets.js` | Rolling — always latest v1. Easy to start, but may include breaking changes between minor versions. |
| `@<commit-sha>/apps/widget/dist/v1/arena-tickets.js` | Pinned — immutable. Recommended for production. |

The `v1` path prefix indicates the major API version. Breaking changes (new required attributes, removed attributes, renamed element) will bump the prefix to `v2`.

### Pinning to a specific commit (recommended for production)

```html
<script type="module"
  src="https://cdn.jsdelivr.net/gh/avamb/arena-platform@abc1234/apps/widget/dist/v1/arena-tickets.js">
</script>
```

---

## Content Security Policy (CSP)

If your site uses a `Content-Security-Policy` header, add the following directives:

```
script-src https://cdn.jsdelivr.net;
style-src  'unsafe-inline';
```

The `style-src 'unsafe-inline'` allowance is required because the widget uses Shadow DOM with inline styles for encapsulation. No external stylesheets are loaded.

If you self-host the widget (see below), replace `cdn.jsdelivr.net` with your own CDN domain.

---

## Iframe Fallback

For environments where custom elements or ES modules are blocked (strict CSPs, some CMS editors), use the iframe fallback page:

```html
<iframe
  src="https://cdn.jsdelivr.net/gh/avamb/arena-platform@master/apps/widget/dist/v1/iframe.html?feed-token=YOUR_FEED_TOKEN&locale=en"
  width="100%"
  height="700"
  frameborder="0"
  allow="payment">
</iframe>
```

### Iframe URL parameters

| Parameter    | Required | Description |
|---|---|---|
| `feed-token` | Yes      | Feed token. |
| `session-id` | No       | Pre-select a session. |
| `locale`     | No       | UI locale (default: `en`). |

> The iframe page is served from the same CDN path as the widget JS and is included in the GitHub Release artifact.

---

## WordPress Shortcode

After installing and activating the **Arena Events** plugin:

```
[arena_tickets feed_token="YOUR_FEED_TOKEN"]
```

### Shortcode attributes

| Attribute    | Required | Default | Description |
|---|---|---|---|
| `feed_token` | Yes      | —       | Feed token. |
| `session_id` | No       | —       | Pre-select a session. |
| `locale`     | No       | `en`    | UI locale. |
| `cdn_base`   | No       | Plugin setting → jsDelivr | Override the CDN base URL for this embed only. |

Example with all attributes:

```
[arena_tickets feed_token="abc123" session_id="sess_456" locale="ru"]
```

The plugin enqueues the widget JS as an ES module (`type="module" defer`). No additional configuration is required.

---

## Gutenberg Block

The **Arena Tickets** block is available in the **Embed** category of the Gutenberg block inserter. It provides the same attributes as the shortcode, configurable through the block Inspector Controls panel on the right side of the editor.

The block is server-side rendered — the `save()` function returns `null` and WordPress calls `render_callback` (which is `Arena_Events_Widget::render_shortcode()`) on every page load.

---

## Self-Hosting

Download the latest widget artifact from the GitHub Releases page (release tag: `widget-v1`) and serve the two files from your own CDN or web server:

```
arena-tickets.js   — the widget bundle (ES module)
iframe.html        — the iframe fallback page
```

Then pass your CDN base URL when embedding:

**Script tag:**
```html
<script type="module" src="https://your-cdn.example.com/v1/arena-tickets.js"></script>
<arena-tickets feed-token="YOUR_FEED_TOKEN"></arena-tickets>
```

**WordPress shortcode:**
```
[arena_tickets feed_token="YOUR_FEED_TOKEN" cdn_base="https://your-cdn.example.com/v1"]
```

Or configure the default CDN base in the Arena Events plugin settings page (Settings → Arena Events → Widget CDN Base URL). All shortcodes and blocks on the site will use that URL unless overridden per-embed.
