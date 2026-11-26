// @vitest-environment jsdom
/**
 * SeatMapView expand (full screen inside the page) and rotate: the phone case
 * (Telegram in-app browser has no element fullscreen).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The suite resolves svelte to its server build; this file needs the client one.
vi.mock('svelte', async () => await import('../../node_modules/svelte/src/index-client.js'));

const schemaState = { w: 800, h: 600 };

vi.mock('../api.js', () => ({
  fetchSessionSchema: vi.fn(async () => ({
    geometry: {
      schema_version: 1,
      canvas: { width: schemaState.w, height: schemaState.h },
      categories: [],
      sections: [],
    },
    category_prices: [],
  })),
}));

vi.mock('../lib/poller.js', () => ({
  SeatStatusPoller: class {
    start(): void {}
    stop(): void {}
    async refresh(): Promise<void> {}
  },
}));

import { createRawSnippet, flushSync, mount, tick, unmount } from 'svelte';
import SeatMapView from './SeatMapView.svelte';
import { CHECKOUT_I18N } from '../lib/checkout.js';

type Props = Record<string, unknown>;

let current: ReturnType<typeof mount> | null = null;

async function show(canvas: { w: number; h: number }, extra: Props = {}) {
  schemaState.w = canvas.w;
  schemaState.h = canvas.h;
  const target = document.createElement('div');
  document.body.appendChild(target);
  current = mount(SeatMapView, {
    target,
    props: { session: { id: 's1', schema_url: '/x' } as never, locale: 'en', ...extra },
  });
  flushSync();
  await vi.waitFor(() => {
    if (!target.querySelector('.seat-map-inner')) throw new Error('schema not drawn yet');
  });
  const wrap = target.querySelector('.seat-map-wrap') as HTMLElement;
  const btn = (text: string) =>
    Array.from(target.querySelectorAll('button')).find((b) => b.textContent?.trim() === text) as
      | HTMLButtonElement
      | undefined;
  return { target, wrap, btn };
}

async function settle(): Promise<void> {
  flushSync();
  await tick();
  await tick();
}

function setViewport(width: number, height: number): void {
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: width });
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: height });
}

beforeEach(() => {
  vi.stubGlobal(
    'ResizeObserver',
    class {
      observe(): void {}
      unobserve(): void {}
      disconnect(): void {}
    },
  );
  setViewport(390, 800);
  document.body.style.overflow = '';
  document.documentElement.style.overflow = '';
});

afterEach(() => {
  if (current) unmount(current);
  current = null;
  document.body.innerHTML = '';
});

describe('SeatMapView expand', () => {
  it('opens as a dialog and locks the body scroll', async () => {
    document.body.style.overflow = 'auto';
    const { wrap, btn } = await show({ w: 800, h: 600 });
    expect(wrap.classList.contains('expanded')).toBe(false);
    expect(wrap.getAttribute('role')).toBeNull();
    btn('Expand')!.click();
    await settle();
    expect(wrap.classList.contains('expanded')).toBe(true);
    expect(wrap.getAttribute('role')).toBe('dialog');
    expect(wrap.getAttribute('aria-modal')).toBe('true');
    expect(wrap.getAttribute('aria-label')).toBe(CHECKOUT_I18N.en.seatmap_dialog_label);
    expect(document.body.style.overflow).toBe('hidden');
    expect(document.documentElement.style.overflow).toBe('hidden');
    expect(btn('Expand')).toBeUndefined();
  });

  it('keeps the same plan DOM while expanded', async () => {
    const { target, btn } = await show({ w: 800, h: 600 });
    const inner = target.querySelector('.seat-map-inner');
    btn('Expand')!.click();
    await settle();
    expect(target.querySelector('.seat-map-inner')).toBe(inner);
  });

  it('closes with the Close button and restores the scroll', async () => {
    document.body.style.overflow = 'auto';
    const { wrap, btn } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    btn('Close')!.click();
    await settle();
    expect(wrap.classList.contains('expanded')).toBe(false);
    expect(wrap.getAttribute('role')).toBeNull();
    expect(document.body.style.overflow).toBe('auto');
    expect(document.documentElement.style.overflow).toBe('');
  });

  it('closes on Escape', async () => {
    const { wrap, btn } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    wrap.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
    await settle();
    expect(wrap.classList.contains('expanded')).toBe(false);
    expect(document.body.style.overflow).toBe('');
  });

  it('moves focus to Close on open and back to Expand on close', async () => {
    const { btn } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    expect(document.activeElement).toBe(btn('Close'));
    btn('Close')!.click();
    await settle();
    expect(document.activeElement).toBe(btn('Expand'));
  });

  it('wraps Tab from the last focusable to the first', async () => {
    const { wrap, btn } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    const items = Array.from(
      wrap.querySelectorAll<HTMLElement>('button:not([disabled]), [tabindex]:not([tabindex="-1"])'),
    );
    const first = items[0]!;
    const last = items[items.length - 1]!;
    last.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    last.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(first);
    const back = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true });
    first.dispatchEvent(back);
    expect(back.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(last);
  });

  it('collapse() closes the expanded map', async () => {
    const target = document.createElement('div');
    document.body.appendChild(target);
    schemaState.w = 800;
    schemaState.h = 600;
    const inst = mount(SeatMapView, {
      target,
      props: { session: { id: 's1', schema_url: '/x' } as never, locale: 'en' },
    }) as unknown as { collapse(): void };
    current = inst as never;
    flushSync();
    await vi.waitFor(() => {
      if (!target.querySelector('.seat-map-inner')) throw new Error('not drawn');
    });
    (Array.from(target.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Expand') as HTMLElement).click();
    await settle();
    expect(target.querySelector('.seat-map-wrap')!.classList.contains('expanded')).toBe(true);
    inst.collapse();
    await settle();
    expect(target.querySelector('.seat-map-wrap')!.classList.contains('expanded')).toBe(false);
  });
});

describe('SeatMapView rotate', () => {
  it('is absent while collapsed', async () => {
    const { btn } = await show({ w: 800, h: 600 });
    expect(btn('Rotate plan')).toBeUndefined();
  });

  it('is offered for a wide plan on a portrait screen when expanded', async () => {
    const { btn, target } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    const rot = btn('Rotate plan')!;
    expect(rot).toBeDefined();
    expect(rot.getAttribute('aria-pressed')).toBe('false');
    rot.click();
    await settle();
    expect(btn('Rotate plan')!.getAttribute('aria-pressed')).toBe('true');
    const style = (target.querySelector('.seat-map-inner') as HTMLElement).getAttribute('style') ?? '';
    expect(style).toContain('translate(600px, 0) rotate(90deg)');
    btn('Close')!.click();
    await settle();
    expect((target.querySelector('.seat-map-inner') as HTMLElement).getAttribute('style') ?? '').not.toContain(
      'rotate(90deg)',
    );
  });

  it('is absent for a tall plan', async () => {
    const { btn } = await show({ w: 600, h: 800 });
    btn('Expand')!.click();
    await settle();
    expect(btn('Rotate plan')).toBeUndefined();
  });

  it('is absent on a landscape screen', async () => {
    setViewport(900, 400);
    const { btn } = await show({ w: 800, h: 600 });
    btn('Expand')!.click();
    await settle();
    expect(btn('Rotate plan')).toBeUndefined();
  });
});

describe('SeatMapView footer', () => {
  it('is drawn only while expanded', async () => {
    const footer = createRawSnippet(() => ({ render: () => '<div id="the-footer">cart</div>' }));
    const { target, btn } = await show({ w: 800, h: 600 }, { footer });
    expect(target.querySelector('#the-footer')).toBeNull();
    btn('Expand')!.click();
    await settle();
    expect(target.querySelector('#the-footer')).not.toBeNull();
    btn('Close')!.click();
    await settle();
    expect(target.querySelector('#the-footer')).toBeNull();
  });
});

describe('seat map i18n keys', () => {
  const keys = [
    'seatmap_expand',
    'seatmap_expand_aria',
    'seatmap_close',
    'seatmap_close_aria',
    'seatmap_rotate',
    'seatmap_rotate_aria',
    'seatmap_dialog_label',
  ] as const;
  for (const [locale, strings] of Object.entries(CHECKOUT_I18N)) {
    it(`${locale} has every key`, () => {
      for (const k of keys) {
        expect((strings as Record<string, string>)[k], `${locale}.${k}`).toBeTruthy();
      }
    });
  }
});
