<svelte:options
  customElement={{
    tag: 'arena-tickets',
    props: {
      feedToken: { type: 'String', attribute: 'feed-token' },
      sessionId: { type: 'String', attribute: 'session-id' },
      locale: { type: 'String', attribute: 'locale' },
    },
  }}
/>

<script lang="ts">
  import { parseLocale, parseFeedToken, parseSessionId } from './utils.js';

  interface Props {
    /** The public feed token identifying the event/catalogue. */
    feedToken?: string;
    /** Optional session ID to deep-link into a specific event session. */
    sessionId?: string;
    /** BCP-47 locale tag, e.g. "en", "ru", "de". Defaults to "en". */
    locale?: string;
  }

  const { feedToken = '', sessionId = '', locale = 'en' }: Props = $props();

  const normLocale = $derived(parseLocale(locale));
  const normFeedToken = $derived(parseFeedToken(feedToken));
  const normSessionId = $derived(parseSessionId(sessionId));
  const hasToken = $derived(normFeedToken !== '');
</script>

<!--
  Arena Tickets Widget — Shadow DOM root.
  Theme is controlled via CSS custom properties on the host:

    arena-tickets {
      --arena-font-family: 'Inter', sans-serif;
      --arena-color-primary: #1a1a1a;
      --arena-accent: #6366f1;
      --arena-bg: #fff;
      --arena-radius: 8px;
      --arena-border-color: #e5e7eb;
    }
-->
<div
  class="arena-tickets-root"
  data-locale={normLocale}
  data-feed-token={normFeedToken}
  data-session-id={normSessionId}
  aria-label="Arena Tickets"
  role="region"
>
  {#if hasToken}
    <div class="arena-tickets-frame" aria-live="polite">
      <!-- Widget content rendered here once fully wired -->
      <slot></slot>
    </div>
  {:else}
    <div class="arena-tickets-placeholder" aria-hidden="true">
      <!-- No feed-token provided -->
    </div>
  {/if}
</div>

<style>
  :host {
    display: block;
    box-sizing: border-box;
    font-family: var(--arena-font-family, system-ui, -apple-system, sans-serif);
    color: var(--arena-color-primary, #1a1a1a);
    background: var(--arena-bg, transparent);
    --_accent: var(--arena-accent, #6366f1);
    --_radius: var(--arena-radius, 8px);
    --_border: var(--arena-border-color, #e5e7eb);
  }

  .arena-tickets-root {
    display: block;
    width: 100%;
  }

  .arena-tickets-frame {
    display: block;
    border: 1px solid var(--_border);
    border-radius: var(--_radius);
    overflow: hidden;
  }

  .arena-tickets-placeholder {
    display: none;
  }
</style>
