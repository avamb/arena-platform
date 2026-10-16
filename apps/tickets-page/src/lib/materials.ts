import type { PageLocale } from './locale.ts';
import { t } from './i18n.ts';
import type { PageStrings } from './i18n.ts';
import { SCROLL_LOCK_CLASS } from './contact.ts';
import type {
  HostedPageMapPoint,
  HostedPageMaterials,
  HostedPagePartnerLink,
  HostedPagePhoto,
  HostedPageVideo,
} from './api.ts';

/**
 * materials.ts — the event page's gallery, videos, map, "where else to buy"
 * links and photo report (spec 40 task 2-1, plan stage 2, owner decision 7).
 *
 * The rule that shapes everything here: the page must not make a buyer's
 * browser talk to a third party, or set a cookie, before the buyer chooses
 * to. So
 *   - a video is a button until it is pressed; only then an <iframe> to one
 *     of three embed hosts (youtube-nocookie, player.vimeo.com with dnt=1,
 *     rutube) is created, and the address is checked against that allow-list
 *     again here, whatever the API said;
 *   - the map is TWO LINKS (OpenStreetMap, Google Maps) and the coordinates
 *     as text, never an embedded map or a map script;
 *   - photos are plain <img loading="lazy"> from the platform's own signed
 *     media address and a small lightbox written in this file;
 *   - partner links open in a new tab with rel="noopener nofollow".
 * Everything is written with textContent / setAttribute, never innerHTML.
 */

/** Hooks the page owner can attach (analytics, task 2-3). Both are optional
 * and a throwing hook never breaks the page. */
export interface MaterialsHooks {
  onVideoPlay?: (provider: string) => void;
  onPartnerClick?: (host: string) => void;
}

interface EmbedRule {
  /** Display name of the provider. */
  label: string;
  host: string;
  pathPrefix: string;
  /** Query parameters set on the embed address when it is created. */
  params: Record<string, string>;
}

/** The three hosts the CSP `frame-src` allows (security-headers.conf.template).
 * Change both together. */
export const EMBED_RULES: Readonly<Record<string, EmbedRule>> = {
  youtube: { label: 'YouTube', host: 'www.youtube-nocookie.com', pathPrefix: '/embed/', params: { autoplay: '1', rel: '0' } },
  vimeo: { label: 'Vimeo', host: 'player.vimeo.com', pathPrefix: '/video/', params: { autoplay: '1', dnt: '1' } },
  rutube: { label: 'Rutube', host: 'rutube.ru', pathPrefix: '/play/embed/', params: {} },
};

/** Parses an https address; anything else (http, javascript:, a relative
 * path, an address with credentials) is null. */
export function httpsURL(raw: string | null | undefined): URL | null {
  if (typeof raw !== 'string' || raw.trim() === '') return null;
  try {
    const u = new URL(raw.trim());
    if (u.protocol !== 'https:' || u.username !== '' || u.password !== '') return null;
    return u;
  } catch {
    return null;
  }
}

/** The address to put in an iframe for a video, or null when the video has no
 * trustworthy embed (the page then shows a plain link). */
export function safeEmbedURL(video: HostedPageVideo): string | null {
  const rule = EMBED_RULES[video.provider];
  if (!rule) return null;
  const u = httpsURL(video.embed_url);
  if (!u || u.hostname !== rule.host || !u.pathname.startsWith(rule.pathPrefix) || u.pathname.length <= rule.pathPrefix.length) return null;
  for (const [k, v] of Object.entries(rule.params)) u.searchParams.set(k, v);
  u.hash = '';
  return u.toString();
}

/** The two map links for a point, or null for coordinates that are not
 * numbers on the globe. */
export function mapLinks(point: HostedPageMapPoint): { osm: string; google: string; text: string } | null {
  const lat = point.latitude;
  const lon = point.longitude;
  if (typeof lat !== 'number' || typeof lon !== 'number' || !Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  if (lat < -90 || lat > 90 || lon < -180 || lon > 180) return null;
  const la = lat.toFixed(6).replace(/0+$/, '').replace(/\.$/, '');
  const lo = lon.toFixed(6).replace(/0+$/, '').replace(/\.$/, '');
  return {
    osm: `https://www.openstreetmap.org/?mlat=${la}&mlon=${lo}#map=17/${la}/${lo}`,
    google: `https://www.google.com/maps/search/?api=1&query=${la},${lo}`,
    text: `${lat.toFixed(5)}, ${lon.toFixed(5)}`,
  };
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

function safely(fn: (() => void) | undefined): void {
  if (!fn) return;
  try {
    fn();
  } catch {
    // A tracking hook must never break the page.
  }
}

function externalLink(href: string, text: string, rel: string, className: string): HTMLAnchorElement {
  const a = el('a', className, text);
  a.href = href;
  a.target = '_blank';
  a.rel = rel;
  return a;
}

function section(className: string, heading: string, id: string): HTMLElement {
  const s = el('section', `asa-materials__section ${className}`);
  s.setAttribute('aria-labelledby', id);
  const h = el('h2', 'asa-materials__title', heading);
  h.id = id;
  s.appendChild(h);
  return s;
}

// ---- Photos and the lightbox ----

function photoLabel(photo: HostedPagePhoto, index: number, heading: string): string {
  const caption = typeof photo.caption === 'string' ? photo.caption.trim() : '';
  return caption || `${heading} ${index + 1}`;
}

/** True on the mirrored (Hebrew) page, where "previous" is on the right. */
function rtl(): boolean {
  return document.documentElement.dir === 'rtl';
}

/** A small modal viewer for one photo at a time. No library: an overlay,
 * three buttons, Escape / arrow keys, focus kept inside and returned. */
export function openLightbox(photos: HostedPagePhoto[], start: number, strings: PageStrings, heading: string, opener: HTMLElement | null): void {
  if (photos.length === 0) return;
  let index = Math.min(Math.max(start, 0), photos.length - 1);

  const overlay = el('div', 'asa-lightbox');
  overlay.setAttribute('role', 'dialog');
  overlay.setAttribute('aria-modal', 'true');
  overlay.setAttribute('aria-label', heading);

  const figure = el('figure', 'asa-lightbox__figure');
  const img = el('img', 'asa-lightbox__image');
  const caption = el('figcaption', 'asa-lightbox__caption');
  figure.appendChild(img);
  figure.appendChild(caption);

  const close = el('button', 'asa-lightbox__btn asa-lightbox__close', '×');
  close.type = 'button';
  close.setAttribute('aria-label', strings.lightboxClose);
  const prev = el('button', 'asa-lightbox__btn asa-lightbox__prev', '‹');
  prev.type = 'button';
  prev.setAttribute('aria-label', strings.lightboxPrev);
  const next = el('button', 'asa-lightbox__btn asa-lightbox__next', '›');
  next.type = 'button';
  next.setAttribute('aria-label', strings.lightboxNext);

  const show = (): void => {
    const photo = photos[index]!;
    const label = photoLabel(photo, index, heading);
    img.src = photo.url;
    img.alt = label;
    caption.textContent = typeof photo.caption === 'string' ? photo.caption.trim() : '';
    caption.hidden = caption.textContent === '';
    prev.hidden = photos.length < 2;
    next.hidden = photos.length < 2;
  };
  const step = (delta: number): void => {
    index = (index + delta + photos.length) % photos.length;
    show();
  };

  const finish = (): void => {
    document.removeEventListener('keydown', onKey, true);
    overlay.remove();
    document.body.classList.remove(SCROLL_LOCK_CLASS);
    if (opener && opener.isConnected) opener.focus();
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === 'Escape') {
      e.preventDefault();
      finish();
    } else if (e.key === (rtl() ? 'ArrowRight' : 'ArrowLeft') && photos.length > 1) {
      e.preventDefault();
      step(-1);
    } else if (e.key === (rtl() ? 'ArrowLeft' : 'ArrowRight') && photos.length > 1) {
      e.preventDefault();
      step(1);
    } else if (e.key === 'Tab') {
      // Keep the focus inside the dialog.
      const buttons = [close, prev, next].filter((b) => !b.hidden);
      const first = buttons[0]!;
      const last = buttons[buttons.length - 1]!;
      const active = document.activeElement;
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
  };

  close.addEventListener('click', finish);
  prev.addEventListener('click', () => step(-1));
  next.addEventListener('click', () => step(1));
  overlay.addEventListener('click', (e) => {
    if (e.target === overlay || e.target === figure) finish();
  });

  overlay.appendChild(figure);
  overlay.appendChild(prev);
  overlay.appendChild(next);
  overlay.appendChild(close);
  show();
  document.body.appendChild(overlay);
  document.body.classList.add(SCROLL_LOCK_CLASS);
  document.addEventListener('keydown', onKey, true);
  close.focus();
}

function renderPhotoGrid(photos: HostedPagePhoto[], strings: PageStrings, heading: string, className: string, id: string): HTMLElement | null {
  const usable = photos.filter((p) => p && typeof p.url === 'string' && p.url !== '');
  if (usable.length === 0) return null;
  const s = section(className, heading, id);
  const list = el('ul', 'asa-photos');
  usable.forEach((photo, i) => {
    const li = el('li', 'asa-photos__item');
    const btn = el('button', 'asa-photos__btn');
    btn.type = 'button';
    const label = photoLabel(photo, i, heading);
    btn.setAttribute('aria-label', `${strings.photoOpen}: ${label}`);
    const img = el('img', 'asa-photos__img');
    img.src = photo.url;
    img.alt = label;
    img.loading = 'lazy';
    img.decoding = 'async';
    if (typeof photo.width === 'number' && photo.width > 0) img.width = photo.width;
    if (typeof photo.height === 'number' && photo.height > 0) img.height = photo.height;
    btn.appendChild(img);
    btn.addEventListener('click', () => openLightbox(usable, i, strings, heading, btn));
    li.appendChild(btn);
    list.appendChild(li);
  });
  s.appendChild(list);
  return s;
}

// ---- Videos ----

function renderVideo(video: HostedPageVideo, index: number, strings: PageStrings, hooks: MaterialsHooks): HTMLElement | null {
  const original = httpsURL(video.url);
  const embed = safeEmbedURL(video);
  const rule = EMBED_RULES[video.provider];
  const li = el('li', 'asa-video');

  if (!embed || !rule) {
    // A plain link: another https page, or an embed we do not trust.
    if (!original) return null;
    li.appendChild(externalLink(original.toString(), `${strings.videoLabel}: ${original.hostname}`, 'noopener noreferrer', 'asa-video__link'));
    return li;
  }

  const label = `${strings.videoLabel} ${index + 1}`;
  const stage = el('div', 'asa-video__stage');
  const play = el('button', 'asa-video__play');
  play.type = 'button';
  const icon = el('span', 'asa-video__icon', '▶');
  icon.setAttribute('aria-hidden', 'true');
  play.appendChild(icon);
  play.appendChild(el('span', 'asa-video__text', `${strings.videoPlay} · ${rule.label}`));
  play.addEventListener('click', () => {
    const frame = el('iframe', 'asa-video__frame');
    frame.src = embed;
    frame.title = label;
    frame.setAttribute('allow', 'autoplay; fullscreen; picture-in-picture');
    frame.setAttribute('allowfullscreen', '');
    frame.setAttribute('referrerpolicy', 'strict-origin-when-cross-origin');
    frame.setAttribute('sandbox', 'allow-scripts allow-same-origin allow-presentation allow-popups allow-popups-to-escape-sandbox');
    frame.loading = 'lazy';
    stage.replaceChildren(frame);
    frame.focus();
    safely(() => hooks.onVideoPlay?.(video.provider));
  });
  stage.appendChild(play);
  li.appendChild(stage);
  if (original) {
    li.appendChild(externalLink(original.toString(), `${strings.videoOpenOn} ${rule.label}`, 'noopener noreferrer', 'asa-video__link'));
  }
  return li;
}

function renderVideos(videos: HostedPageVideo[], strings: PageStrings, hooks: MaterialsHooks): HTMLElement | null {
  const items = videos.map((v, i) => renderVideo(v, i, strings, hooks)).filter((x): x is HTMLElement => x !== null);
  if (items.length === 0) return null;
  const s = section('asa-materials__videos', strings.materialsVideos, 'asa-materials-videos');
  const list = el('ul', 'asa-videos');
  items.forEach((i) => list.appendChild(i));
  s.appendChild(list);
  return s;
}

// ---- Map ----

function renderMap(point: HostedPageMapPoint | null, venueNames: string[], strings: PageStrings): HTMLElement | null {
  if (!point) return null;
  const links = mapLinks(point);
  if (!links) return null;
  const s = section('asa-materials__map', strings.materialsMap, 'asa-materials-map');
  const label = typeof point.label === 'string' ? point.label.trim() : '';
  const where = label || venueNames.join(', ');
  if (where) s.appendChild(el('p', 'asa-map__place', where));
  const coords = el('p', 'asa-map__coords');
  coords.appendChild(document.createTextNode(`${strings.mapCoordinates}: `));
  const num = el('bdi', 'asa-map__numbers', links.text);
  coords.appendChild(num);
  s.appendChild(coords);
  const actions = el('p', 'asa-map__links');
  actions.appendChild(externalLink(links.osm, strings.mapOpenOsm, 'noopener noreferrer', 'asa-map__link'));
  actions.appendChild(externalLink(links.google, strings.mapOpenGoogle, 'noopener noreferrer', 'asa-map__link'));
  s.appendChild(actions);
  return s;
}

// ---- Partners ----

function renderPartners(links: HostedPagePartnerLink[], strings: PageStrings, hooks: MaterialsHooks): HTMLElement | null {
  const rows = links
    .map((l) => ({ url: httpsURL(l.url), name: typeof l.seller_name === 'string' ? l.seller_name.trim() : '' }))
    .filter((r): r is { url: URL; name: string } => r.url !== null);
  if (rows.length === 0) return null;
  const s = section('asa-materials__partners', strings.materialsPartners, 'asa-materials-partners');
  const list = el('ul', 'asa-partners');
  for (const row of rows) {
    const host = row.url.hostname;
    const li = el('li', 'asa-partner');
    li.appendChild(el('span', 'asa-partner__seller', `${strings.sellerLabel}: ${row.name || host}`));
    const a = externalLink(row.url.toString(), `${strings.partnerOpen} · ${host}`, 'noopener nofollow', 'asa-partner__link');
    a.addEventListener('click', () => safely(() => hooks.onPartnerClick?.(host)));
    li.appendChild(a);
    list.appendChild(li);
  }
  s.appendChild(list);
  return s;
}

/**
 * Builds the materials block of an event page, or null when the event has
 * none to show (an older backend sends no `materials`, a new event has empty
 * lists). Sections appear only for data that exists.
 */
export function renderMaterials(
  materials: HostedPageMaterials | null | undefined,
  locale: PageLocale,
  venueNames: string[],
  hooks: MaterialsHooks = {},
): HTMLElement | null {
  if (!materials) return null;
  const strings = t(locale);
  const sections = [
    renderPhotoGrid(materials.gallery ?? [], strings, strings.materialsGallery, 'asa-materials__gallery', 'asa-materials-gallery'),
    renderVideos(materials.videos ?? [], strings, hooks),
    renderMap(materials.map_point ?? null, venueNames, strings),
    renderPartners(materials.partner_links ?? [], strings, hooks),
    renderPhotoGrid(materials.report_photos ?? [], strings, strings.materialsReport, 'asa-materials__report', 'asa-materials-report'),
  ].filter((s): s is HTMLElement => s !== null);
  if (sections.length === 0) return null;
  const root = el('div', 'asa-materials');
  sections.forEach((s) => root.appendChild(s));
  return root;
}
