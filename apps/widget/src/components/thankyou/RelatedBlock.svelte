<script lang="ts">
  /** RelatedBlock - "You may also like": up to 3 future events of the same feed. */
  import type { RelatedEvent } from '../../lib/thankYou.js';
  import type { ThankYouStrings } from '../../lib/thankYouI18n.js';

  interface Props {
    s: ThankYouStrings;
    events: RelatedEvent[];
    locale?: string;
  }
  const { s, events, locale = 'en' }: Props = $props();

  function when(iso: string, timeZone: string | null): string {
    try {
      // Plain English reads as en-GB with a 24-hour clock, like the rest of the widget.
      const plainEn = !locale || locale.toLowerCase() === 'en';
      return new Intl.DateTimeFormat(plainEn ? 'en-GB' : locale, {
        dateStyle: 'long',
        timeStyle: 'short',
        ...(plainEn ? { hourCycle: 'h23' as const } : {}),
        ...(timeZone ? { timeZone } : {}),
      }).format(new Date(iso));
    } catch {
      return iso.slice(0, 10);
    }
  }
</script>

{#if events.length > 0}
  <section class="ty-block" data-testid="ty-related">
    <h3 class="ty-title">{s.related_title}</h3>
    <ul class="ty-cards">
      {#each events as e (e.id)}
        <li>
          <a class="ty-ev" href={e.pageUrl} target="_blank" rel="noopener noreferrer">
            <b>{e.name}</b>
            <small>{when(e.date, e.timezone)}</small>
          </a>
        </li>
      {/each}
    </ul>
  </section>
{/if}
