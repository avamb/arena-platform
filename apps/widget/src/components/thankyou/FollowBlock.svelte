<script lang="ts">
  /** FollowBlock - pills with the organizer social links; nothing when empty. */
  import { NET } from '../../lib/thankYou.js';
  import type { SocialLink } from '../../lib/thankYou.js';
  import type { ThankYouStrings } from '../../lib/thankYouI18n.js';
  import SocialIcon from './SocialIcon.svelte';

  interface Props {
    s: ThankYouStrings;
    socials: SocialLink[];
  }
  const { s, socials }: Props = $props();
</script>

{#if socials.length > 0}
  <section class="ty-block" data-testid="ty-follow">
    <h3 class="ty-title">{s.follow_title}</h3>
    <ul class="ty-row">
      {#each socials as l (l.network + l.url)}
        <li>
          <a class="ty-pill" href={l.url} target="_blank" rel="noopener noreferrer">
            <SocialIcon network={l.network} color={NET[l.network]![1]} />
            {NET[l.network]![0]}
          </a>
        </li>
      {/each}
    </ul>
  </section>
{/if}
