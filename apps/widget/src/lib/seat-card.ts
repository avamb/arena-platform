/**
 * seat-card.ts — ONE card that tells a person about a seat.
 *
 * The buyer's widget and the organizer's seat manager show the same card:
 * sector, row, seat, category name, price and, where it matters, a state line
 * (sold, held, closed, selected, changed and not applied yet). The event
 * center imports this module through its `@seatmap` alias.
 *
 *  - `seatCardModel()` is pure: it turns the facts about a seat into lines.
 *    The words come in from each app's own i18n (`labels` and the texts of
 *    `state` and `pending`), this module holds no language.
 *  - `createSeatCard(host)` is a small framework-free renderer. The card is a
 *    child of the map viewport, placed beside the seat and flipped so that it
 *    stays inside the viewport. It is `role="tooltip"`, never takes focus and
 *    lets pointer events through, unless it carries an action.
 *
 * Showing the card only writes to its own DOM: it never re-renders a UI
 * framework and never re-colours a seat.
 */
import SEAT_CARD_CSS from './seat-card.css?inline';

export type SeatCardTone = 'free' | 'selected' | 'sold' | 'held' | 'closed' | 'changed';

export interface SeatCardLabels {
  /** The word before a sector name, e.g. "Sector". */
  sector: string;
  /** The word before a row name, e.g. "Row". */
  row: string;
  /** The word before a seat number, e.g. "Seat". */
  seat: string;
}

export interface SeatCardInput {
  sector?: string | null;
  row?: string | null;
  seat?: string | null;
  /** Category name. */
  category?: string | null;
  /** Category colour, a hex string; drawn as a swatch beside the name. */
  color?: string | null;
  /** The price as the buyer reads it (the widget's `buildPriceLabel`). Empty or missing: no price line. */
  price?: string | null;
  /** The state line: free seats have none. */
  state?: { tone: SeatCardTone; text: string } | null;
  /** A change the organizer has made and not applied yet, already worded: "Not applied: Gold, closed". */
  pending?: string | null;
  labels: SeatCardLabels;
}

export type SeatCardLineKind = 'place' | 'category' | 'price' | 'state' | 'pending';

export interface SeatCardLine {
  kind: SeatCardLineKind;
  text: string;
  tone?: SeatCardTone;
  /** Only on the category line. */
  color?: string;
}

export interface SeatCardModel {
  lines: SeatCardLine[];
  /** An optional press the card carries; it is then the only case the card takes pointer events. */
  action?: { label: string; run: () => void };
}

function clean(s: string | null | undefined): string {
  return (s ?? '').trim();
}

/** The lines of the card. Whatever is not known is left out; the order is fixed. */
export function seatCardModel(input: SeatCardInput): SeatCardModel {
  const lines: SeatCardLine[] = [];
  const { labels } = input;

  const place: string[] = [];
  const sector = clean(input.sector);
  const row = clean(input.row);
  const seat = clean(input.seat);
  if (sector !== '') place.push(`${labels.sector} ${sector}`);
  if (row !== '') place.push(`${labels.row} ${row}`);
  if (seat !== '') place.push(`${labels.seat} ${seat}`);
  if (place.length > 0) lines.push({ kind: 'place', text: place.join(' · ') });

  const category = clean(input.category);
  if (category !== '') {
    const line: SeatCardLine = { kind: 'category', text: category };
    const color = clean(input.color);
    if (color !== '') line.color = color;
    lines.push(line);
  }

  const price = clean(input.price);
  if (price !== '') lines.push({ kind: 'price', text: price });

  const state = input.state;
  if (state && clean(state.text) !== '' && state.tone !== 'free') {
    lines.push({ kind: 'state', text: clean(state.text), tone: state.tone });
  }

  const pending = clean(input.pending);
  if (pending !== '') lines.push({ kind: 'pending', text: pending, tone: 'changed' });

  return { lines };
}

// ─── Renderer ────────────────────────────────────────────────────────────────

/** A seat's box on screen (client coordinates). */
export interface AnchorRect {
  left: number;
  top: number;
  right: number;
  bottom: number;
}

export interface SeatCard {
  /** Shows the card for a model beside the anchor (a seat's client rect). */
  show(model: SeatCardModel, anchor: AnchorRect): void;
  hide(): void;
  isOpen(): boolean;
  /** Removes the card from the page. */
  destroy(): void;
  readonly element: HTMLElement;
}

export interface SeatCardOptions {
  /**
   * Put the card's stylesheet into the root the host lives in (the widget's
   * shadow root). Default true. The event center imports `seat-card.css` as a
   * normal file instead, because its page allows only same-origin styles.
   */
  injectStyles?: boolean;
}

const GAP = 8;
const EDGE = 4;

const installed = new WeakSet<object>();

function installStyles(host: HTMLElement): void {
  const root = host.getRootNode();
  const target: Document | ShadowRoot | null =
    root instanceof ShadowRoot ? root : host.ownerDocument ?? null;
  if (!target) return;
  // A constructable stylesheet is not an inline style: a strict page policy lets it through.
  try {
    const sheets = (target as DocumentOrShadowRoot).adoptedStyleSheets;
    if (Array.isArray(sheets) && typeof CSSStyleSheet === 'function' && 'replaceSync' in CSSStyleSheet.prototype) {
      if (!installed.has(target)) {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(SEAT_CARD_CSS);
        (target as DocumentOrShadowRoot).adoptedStyleSheets = [...sheets, sheet];
        installed.add(target);
      }
      return;
    }
  } catch {
    // Fall through to a <style> element.
  }
  const marker = 'data-arena-seat-card-css';
  const owner = target instanceof ShadowRoot ? target : target.head;
  if (!owner || owner.querySelector(`style[${marker}]`)) return;
  const style = (host.ownerDocument ?? document).createElement('style');
  style.setAttribute(marker, '');
  style.textContent = SEAT_CARD_CSS;
  owner.appendChild(style);
}

export function createSeatCard(host: HTMLElement, options: SeatCardOptions = {}): SeatCard {
  const doc = host.ownerDocument ?? document;
  if (options.injectStyles !== false) installStyles(host);

  const el = doc.createElement('div');
  el.className = 'arena-seat-card';
  el.setAttribute('role', 'tooltip');
  el.hidden = true;
  host.appendChild(el);

  let open = false;
  let signature = '';

  function build(model: SeatCardModel): void {
    el.replaceChildren();
    for (const line of model.lines) {
      const div = doc.createElement('div');
      div.className = `arena-seat-card__line arena-seat-card__line--${line.kind}`;
      if (line.tone) div.setAttribute('data-tone', line.tone);
      if (line.kind === 'category' && line.color) {
        const sw = doc.createElement('span');
        sw.className = 'arena-seat-card__swatch';
        sw.setAttribute('aria-hidden', 'true');
        // CSSOM, not a style attribute: allowed by a strict style-src.
        sw.style.backgroundColor = line.color;
        div.appendChild(sw);
      }
      div.appendChild(doc.createTextNode(line.text));
      el.appendChild(div);
    }
    if (model.action) {
      const btn = doc.createElement('button');
      btn.type = 'button';
      btn.className = 'arena-seat-card__action';
      btn.textContent = model.action.label;
      const run = model.action.run;
      btn.addEventListener('click', () => run());
      el.appendChild(btn);
    }
    el.classList.toggle('has-action', model.action !== undefined);
  }

  function place(anchor: AnchorRect): void {
    const h = host.getBoundingClientRect();
    const w = el.offsetWidth;
    const ht = el.offsetHeight;
    const maxLeft = Math.max(EDGE, h.width - w - EDGE);
    const centre = (anchor.left + anchor.right) / 2 - h.left;
    const left = Math.min(Math.max(EDGE, centre - w / 2), maxLeft);
    // Above the seat; below it when there is no room above.
    let top = anchor.top - h.top - ht - GAP;
    if (top < EDGE) top = anchor.bottom - h.top + GAP;
    const maxTop = Math.max(EDGE, h.height - ht - EDGE);
    top = Math.min(Math.max(EDGE, top), maxTop);
    el.style.left = `${Math.round(left)}px`;
    el.style.top = `${Math.round(top)}px`;
  }

  return {
    element: el,
    show(model, anchor) {
      if (model.lines.length === 0) {
        this.hide();
        return;
      }
      const sig = JSON.stringify([model.lines, model.action?.label ?? null]);
      if (sig !== signature || !open) {
        build(model);
        signature = sig;
      }
      el.hidden = false;
      open = true;
      place(anchor);
    },
    hide() {
      if (!open) return;
      open = false;
      el.hidden = true;
    },
    isOpen() {
      return open;
    },
    destroy() {
      open = false;
      el.remove();
    },
  };
}
