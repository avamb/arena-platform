<script lang="ts">
  /** ShareBlock - round brand-coloured share links (plain links, no scripts). */
  import { NET, buildShareLinks } from '../../lib/thankYou.js';
  import type { ThankYouStrings } from '../../lib/thankYouI18n.js';
  import SocialIcon from './SocialIcon.svelte';

  interface Props {
    s: ThankYouStrings;
    /** Address to share. */
    url: string;
    /** Event name. */
    name: string;
  }
  const { s, url, name }: Props = $props();

  const links = $derived(url ? buildShareLinks(url, name) : []);
</script>

{#if links.length > 0}
  <section class="ty-block" data-testid="ty-share">
    <h3 class="ty-title">{s.share_title}</h3>
    <ul class="ty-row">
      {#each links as l (l.network)}
        <li>
          <a
            class="ty-round"
            style="background: {NET[l.network]![1]}"
            href={l.href}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={NET[l.network]![0]}
           
          >
            <SocialIcon network={l.network} />
          </a>
        </li>
      {/each}
    </ul>
  </section>
{/if}
