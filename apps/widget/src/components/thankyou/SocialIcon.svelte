<script lang="ts">
  /** SocialIcon - one inline brand glyph from a single path table, 24x24 box. */
  import { LINK_PATH, NET } from '../../lib/thankYou.js';

  interface Props {
    network: string;
    /** Glyph colour; white on a coloured round button. */
    color?: string;
  }
  const { network, color = '#fff' }: Props = $props();
</script>

<svg viewBox="0 0 24 24" aria-hidden="true" fill={color}>
  <path fill-rule="evenodd" d={NET[network]?.[2] || LINK_PATH} />
</svg>
