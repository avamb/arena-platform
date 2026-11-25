// @vitest-environment jsdom
/**
 * Phone keyboard on the "Your details" step: key hints, Enter walking to the
 * next field and only the last field submitting. jsdom has no visualViewport:
 * the cart sheet must mount without it.
 */

import { afterEach, describe, expect, it, vi } from 'vitest';

// The suite resolves svelte to its server build; this file needs the client one.
vi.mock('svelte', async () => await import('../node_modules/svelte/src/index-client.js'));

import { flushSync, mount, unmount } from 'svelte';
import BuyerForm from './components/BuyerForm.svelte';
import type { BuyerFieldConfig } from './lib/checkout.js';

const EMAIL: BuyerFieldConfig = { key: 'email', required: true, enabled: true };
const NAME: BuyerFieldConfig = { key: 'name', required: false, enabled: true };
const PHONE: BuyerFieldConfig = { key: 'phone', required: false, enabled: true };

let current: ReturnType<typeof mount> | null = null;

function show(buyerFields: BuyerFieldConfig[], onSubmit = vi.fn()) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  current = mount(BuyerForm, { target, props: { buyerFields, locale: 'en', onSubmit } });
  flushSync();
  const q = (id: string) => document.getElementById(id) as HTMLInputElement | null;
  return { q, onSubmit };
}

function press(el: HTMLElement, key: string): KeyboardEvent {
  const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true });
  el.dispatchEvent(ev);
  return ev;
}

afterEach(() => {
  if (current) unmount(current);
  current = null;
  document.body.innerHTML = '';
});

describe('BuyerForm keyboard attributes', () => {
  it('sets the e-mail keyboard, no capitalisation and the key label', () => {
    const { q } = show([EMAIL, NAME, PHONE]);
    const email = q('bf-email')!;
    expect(email.getAttribute('inputmode')).toBe('email');
    expect(email.getAttribute('autocapitalize')).toBe('off');
    expect(email.getAttribute('spellcheck')).toBe('false');
    expect(email.getAttribute('enterkeyhint')).toBe('next');
    expect(q('bf-name')!.getAttribute('enterkeyhint')).toBe('next');
    expect(q('bf-phone')!.getAttribute('enterkeyhint')).toBe('go');
  });

  it('says "go" where no field follows', () => {
    const only = show([EMAIL]);
    expect(only.q('bf-email')!.getAttribute('enterkeyhint')).toBe('go');
    unmount(current!);
    current = null;
    document.body.innerHTML = '';
    const nameLast = show([EMAIL, NAME]);
    expect(nameLast.q('bf-name')!.getAttribute('enterkeyhint')).toBe('go');
  });
});

describe('BuyerForm Enter key', () => {
  it('moves focus from e-mail to the name field and does not submit', () => {
    const { q, onSubmit } = show([EMAIL, NAME, PHONE]);
    const ev = press(q('bf-email')!, 'Enter');
    expect(ev.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(q('bf-name'));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('skips to the phone when there is no name field', () => {
    const { q } = show([EMAIL, PHONE]);
    press(q('bf-email')!, 'Enter');
    expect(document.activeElement).toBe(q('bf-phone'));
  });

  it('leaves Enter alone in the last field, so the form submits', () => {
    const { q, onSubmit } = show([EMAIL]);
    const email = q('bf-email')!;
    email.value = 'a@example.com';
    email.dispatchEvent(new Event('input', { bubbles: true }));
    const ev = press(email, 'Enter');
    expect(ev.defaultPrevented).toBe(false);
    // jsdom does not run implicit submission, so submit the form as the browser would.
    email.form!.requestSubmit();
    flushSync();
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });
});

describe('CartSheet without visualViewport', () => {
  it('mounts without throwing', async () => {
    expect((window as { visualViewport?: unknown }).visualViewport ?? null).toBeNull();
    const { default: CartSheet } = await import('./components/CartSheet.svelte');
    const target = document.createElement('div');
    document.body.appendChild(target);
    const inst = mount(CartSheet, {
      target,
      props: {
        cart: { lines: [], expiresAt: null } as never,
        buyerFields: [EMAIL],
        onClose: () => {},
        onRemoveLine: () => {},
        onCheckout: () => {},
      },
    });
    flushSync();
    expect(document.querySelector('.sheet')).not.toBeNull();
    unmount(inst);
  });
});
