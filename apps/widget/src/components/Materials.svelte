<script lang="ts">
  /**
   * Materials — the event's gallery, videos, map point, "where else to buy"
   * links and photo report (spec 40 task 2-2).
   *
   * The block is drawn only when the event has something to show, section by
   * section. Nothing third-party loads before a click: a video is a button
   * and only the press creates an <iframe> (to one of three embed hosts, see
   * lib/materials.ts); the map is two links and the coordinates as text.
   * Photos are plain lazy <img> from the platform's signed media address with
   * a small lightbox (Escape, arrow keys, focus kept inside and returned).
   * Everything is mirrored for right-to-left locales.
   */
  import { buildMaterialsView, hasMaterials, stepIndex } from '../lib/materials.js';
  import type { MaterialPhoto } from '../lib/materials.js';
  import { getMaterialsStrings } from '../lib/materialsI18n.js';
  import type { FeedMaterials } from '../types.js';

  interface Props {
    materials?: FeedMaterials | null;
    locale?: string;
    /** API base, to resolve a host-relative picture address. */
    apiBase?: string;
    /** Venue names, the map's caption when the organizer gave no label. */
    venueNames?: string[];
    rtl?: boolean;
  }
  const { materials = null, locale = 'en', apiBase = '', venueNames = [], rtl = false }: Props = $props();

  const s = $derived(getMaterialsStrings(locale));
  const view = $derived(
    buildMaterialsView(materials, apiBase, venueNames, typeof location !== 'undefined' ? location.href : undefined),
  );
  const visible = $derived(hasMaterials(view));

  // ── Lightbox ────────────────────────────────────────────────────────────────
  type ListName = 'gallery' | 'report';
  let lightbox = $state<{ list: ListName; index: number } | null>(null);
  let opener: HTMLElement | null = null;
  let closeBtn = $state<HTMLButtonElement | null>(null);
  let prevBtn = $state<HTMLButtonElement | null>(null);
  let nextBtn = $state<HTMLButtonElement | null>(null);

  const lightList = $derived<MaterialPhoto[]>(lightbox ? view[lightbox.list] : []);
  const lightPhoto = $derived(lightbox ? (lightList[lightbox.index] ?? null) : null);

  function photoLabel(p: MaterialPhoto, i: number): string {
    return p.caption || `${s.photoN} ${i + 1}`;
  }

  function openLightbox(list: ListName, index: number, el: HTMLElement | null): void {
    opener = el;
    lightbox = { list, index };
  }

  function closeLightbox(): void {
    lightbox = null;
    const back = opener;
    opener = null;
    if (back && back.isConnected) back.focus();
  }

  function step(delta: number): void {
    if (!lightbox) return;
    lightbox = { list: lightbox.list, index: stepIndex(lightbox.index, delta, lightList.length) };
  }

  $effect(() => {
    // Move the focus into the dialog when it opens.
    if (lightbox && closeBtn) closeBtn.focus();
  });

  function onLightKey(e: KeyboardEvent): void {
    if (!lightbox) return;
    const many = lightList.length > 1;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      closeLightbox();
    } else if (many && e.key === (rtl ? 'ArrowRight' : 'ArrowLeft')) {
      e.preventDefault();
      step(-1);
    } else if (many && e.key === (rtl ? 'ArrowLeft' : 'ArrowRight')) {
      e.preventDefault();
      step(1);
    } else if (e.key === 'Tab') {
      const buttons = [closeBtn, many ? prevBtn : null, many ? nextBtn : null].filter(
        (b): b is HTMLButtonElement => b !== null,
      );
      if (buttons.length === 0) return;
      const first = buttons[0]!;
      const last = buttons[buttons.length - 1]!;
      const active = (e.currentTarget as HTMLElement).getRootNode() instanceof ShadowRoot
        ? ((e.currentTarget as HTMLElement).getRootNode() as ShadowRoot).activeElement
        : document.activeElement;
      if (e.shiftKey && active === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      } else if (!buttons.includes(active as HTMLButtonElement)) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  // ── Videos: an iframe exists only after the press ───────────────────────────
  let played = $state<Set<number>>(new Set());
  function play(i: number): void {
    played = new Set(played).add(i);
  }
</script>

{#if visible}
  <div class="materials" data-arena-materials>
    {#if view.gallery.length > 0}
      <section class="mat-section" data-materials-section="gallery" aria-labelledby="mat-gallery">
        <h2 class="mat-title" id="mat-gallery">{s.gallery}</h2>
        <ul class="mat-photos">
          {#each view.gallery as photo, i (photo.url + i)}
            <li class="mat-photos-item">
              <button
                type="button"
                class="mat-photo-btn"
                aria-label="{s.photoOpen}: {photoLabel(photo, i)}"
                onclick={(e) => openLightbox('gallery', i, e.currentTarget as HTMLElement)}
              >
                <img
                  class="mat-photo-img"
                  src={photo.url}
                  alt={photoLabel(photo, i)}
                  loading="lazy"
                  decoding="async"
                  width={photo.width ?? undefined}
                  height={photo.height ?? undefined}
                />
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if view.videos.length > 0}
      <section class="mat-section" data-materials-section="videos" aria-labelledby="mat-videos">
        <h2 class="mat-title" id="mat-videos">{s.videos}</h2>
        <ul class="mat-videos">
          {#each view.videos as video, i (video.embed ?? video.link ?? i)}
            <li class="mat-video">
              {#if video.embed}
                <div class="mat-video-stage">
                  {#if played.has(i)}
                    <!-- The only third-party request of the block, made after the press. -->
                    <iframe
                      class="mat-video-frame"
                      src={video.embed}
                      title="{s.videoLabel} {i + 1}"
                      allow="autoplay; fullscreen; picture-in-picture"
                      allowfullscreen
                      referrerpolicy="strict-origin-when-cross-origin"
                      sandbox="allow-scripts allow-same-origin allow-presentation allow-popups allow-popups-to-escape-sandbox"
                    ></iframe>
                  {:else}
                    <button type="button" class="mat-video-play" onclick={() => play(i)}>
                      <span class="mat-video-icon" aria-hidden="true">▶</span>
                      <span>{s.videoPlay} · {video.label}</span>
                    </button>
                  {/if}
                </div>
              {/if}
              {#if video.link}
                <a class="mat-link" href={video.link} target="_blank" rel="noopener noreferrer">
                  {video.embed ? `${s.videoOpenOn} ${video.label}` : `${s.videoLabel}: ${new URL(video.link).hostname}`}
                  <span class="mat-sr"> ({s.newTab})</span>
                </a>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if view.map}
      <section class="mat-section" data-materials-section="map" aria-labelledby="mat-map">
        <h2 class="mat-title" id="mat-map">{s.map}</h2>
        {#if view.map.label}<p class="mat-map-place">{view.map.label}</p>{/if}
        <p class="mat-map-coords">{s.mapCoordinates}: <bdi>{view.map.text}</bdi></p>
        <p class="mat-map-links">
          <a class="mat-link" href={view.map.osm} target="_blank" rel="noopener noreferrer">
            {s.mapOpenOsm}<span class="mat-sr"> ({s.newTab})</span>
          </a>
          <a class="mat-link" href={view.map.google} target="_blank" rel="noopener noreferrer">
            {s.mapOpenGoogle}<span class="mat-sr"> ({s.newTab})</span>
          </a>
        </p>
      </section>
    {/if}

    {#if view.partners.length > 0}
      <section class="mat-section" data-materials-section="partners" aria-labelledby="mat-partners">
        <h2 class="mat-title" id="mat-partners">{s.partners}</h2>
        <ul class="mat-partners">
          {#each view.partners as partner (partner.url)}
            <li class="mat-partner">
              <span class="mat-partner-seller">{s.sellerLabel}: {partner.seller}</span>
              <a class="mat-link" href={partner.url} target="_blank" rel="noopener nofollow">
                {s.partnerOpen} · {partner.host}<span class="mat-sr"> ({s.newTab})</span>
              </a>
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    {#if view.report.length > 0}
      <section class="mat-section" data-materials-section="report" aria-labelledby="mat-report">
        <h2 class="mat-title" id="mat-report">{s.report}</h2>
        <ul class="mat-photos">
          {#each view.report as photo, i (photo.url + i)}
            <li class="mat-photos-item">
              <button
                type="button"
                class="mat-photo-btn"
                aria-label="{s.photoOpen}: {photoLabel(photo, i)}"
                onclick={(e) => openLightbox('report', i, e.currentTarget as HTMLElement)}
              >
                <img
                  class="mat-photo-img"
                  src={photo.url}
                  alt={photoLabel(photo, i)}
                  loading="lazy"
                  decoding="async"
                  width={photo.width ?? undefined}
                  height={photo.height ?? undefined}
                />
              </button>
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  </div>

  {#if lightbox && lightPhoto}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div
      class="mat-lightbox"
      role="dialog"
      aria-modal="true"
      aria-label={lightbox.list === 'gallery' ? s.gallery : s.report}
      tabindex="-1"
      onkeydown={onLightKey}
      onclick={(e) => {
        if (e.target === e.currentTarget) closeLightbox();
      }}
    >
      <figure class="mat-lightbox-figure">
        <img class="mat-lightbox-img" src={lightPhoto.url} alt={photoLabel(lightPhoto, lightbox.index)} />
        {#if lightPhoto.caption}<figcaption class="mat-lightbox-caption">{lightPhoto.caption}</figcaption>{/if}
      </figure>
      {#if lightList.length > 1}
        <button type="button" class="mat-lb-btn mat-lb-prev" bind:this={prevBtn} aria-label={s.lightboxPrev} onclick={() => step(-1)}>
          {rtl ? '›' : '‹'}
        </button>
        <button type="button" class="mat-lb-btn mat-lb-next" bind:this={nextBtn} aria-label={s.lightboxNext} onclick={() => step(1)}>
          {rtl ? '‹' : '›'}
        </button>
      {/if}
      <button type="button" class="mat-lb-btn mat-lb-close" bind:this={closeBtn} aria-label={s.lightboxClose} onclick={closeLightbox}>×</button>
    </div>
  {/if}
{/if}

<style>
  .materials {
    display: flex;
    flex-direction: column;
    gap: 1.25rem;
    margin-top: 1.25rem;
    color: var(--arena-color-primary, #1a1a1a);
  }
  .mat-title {
    margin: 0 0 0.5rem;
    font-size: 1rem;
    font-weight: 700;
  }
  .mat-link {
    color: var(--arena-accent, #4f46e5);
    text-decoration: underline;
    text-underline-offset: 2px;
    font-size: 0.875rem;
  }
  .mat-link:focus-visible,
  .mat-photo-btn:focus-visible,
  .mat-video-play:focus-visible,
  .mat-lb-btn:focus-visible {
    outline: 2px solid var(--arena-focus-ring, var(--arena-accent, #4f46e5));
    outline-offset: 2px;
  }
  .mat-sr {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    border: 0;
  }

  /* Photos */
  .mat-photos {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(8rem, 1fr));
    gap: 0.5rem;
  }
  .mat-photo-btn {
    display: block;
    width: 100%;
    padding: 0;
    border: 1px solid var(--arena-border-color, #e5e7eb);
    border-radius: var(--arena-radius, 8px);
    overflow: hidden;
    background: transparent;
    cursor: zoom-in;
  }
  .mat-photo-img {
    display: block;
    width: 100%;
    height: auto;
    aspect-ratio: 4 / 3;
    object-fit: cover;
  }

  /* Videos */
  .mat-videos,
  .mat-partners {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .mat-video {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  .mat-video-stage {
    position: relative;
    aspect-ratio: 16 / 9;
    width: 100%;
    border-radius: var(--arena-radius, 8px);
    overflow: hidden;
    background: #111827;
  }
  .mat-video-play {
    position: absolute;
    inset: 0;
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    border: 0;
    background: transparent;
    color: #fff;
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .mat-video-icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2.5rem;
    height: 2.5rem;
    border-radius: 50%;
    background: var(--arena-accent, #4f46e5);
  }
  .mat-video-frame {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    border: 0;
  }

  /* Map and partners */
  .mat-map-place,
  .mat-map-coords {
    margin: 0 0 0.25rem;
    font-size: 0.875rem;
  }
  .mat-map-coords {
    color: var(--arena-color-secondary, #6b7280);
  }
  .mat-map-links {
    margin: 0;
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem 1rem;
  }
  .mat-partner {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: 0.25rem 0.75rem;
    font-size: 0.875rem;
  }
  .mat-partner-seller {
    font-weight: 600;
  }

  /* Lightbox */
  .mat-lightbox {
    position: fixed;
    inset: 0;
    z-index: 2147483000;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(0, 0, 0, 0.88);
  }
  .mat-lightbox-figure {
    margin: 0;
    max-width: 92vw;
    max-height: 92vh;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
  }
  .mat-lightbox-img {
    max-width: 92vw;
    max-height: 80vh;
    object-fit: contain;
  }
  .mat-lightbox-caption {
    color: #f3f4f6;
    font-size: 0.875rem;
    text-align: center;
  }
  .mat-lb-btn {
    position: absolute;
    width: 2.75rem;
    height: 2.75rem;
    border: 0;
    border-radius: 50%;
    background: rgba(255, 255, 255, 0.16);
    color: #fff;
    font-size: 1.75rem;
    line-height: 1;
    cursor: pointer;
  }
  .mat-lb-btn:hover {
    background: rgba(255, 255, 255, 0.3);
  }
  .mat-lb-close {
    top: 0.75rem;
    inset-inline-end: 0.75rem;
  }
  .mat-lb-prev {
    top: 50%;
    inset-inline-start: 0.75rem;
    transform: translateY(-50%);
  }
  .mat-lb-next {
    top: 50%;
    inset-inline-end: 0.75rem;
    transform: translateY(-50%);
  }
</style>
