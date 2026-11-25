import { describe, it, expect, vi } from 'vitest';
import {
  attemptLabel,
  createPaymentSteps,
  createPaymentStepsRegistry,
  methodKind,
  type PaymentStepEvent,
} from './paymentSteps.js';

function fakeStorage(initial: Record<string, string> = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (k: string) => data[k] ?? null,
    setItem: (k: string, v: string) => {
      data[k] = v;
    },
  };
}

describe('"sent once" marks in session storage, per checkout token', () => {
  it('keeps the marks, so a reload of the same checkout does not report the form view again', () => {
    const store = fakeStorage();
    const seen: string[] = [];
    createPaymentStepsRegistry((e) => seen.push(e.step), store).forToken('tok-1').emit('checkout_form_view');
    // A reload: a new registry over the same storage.
    createPaymentStepsRegistry((e) => seen.push(e.step), store).forToken('tok-1').emit('checkout_form_view');
    expect(seen).toEqual(['checkout_form_view']);
    expect(Object.keys(store.data)).toEqual(['arena_checkout_steps:tok-1']);
    expect(JSON.parse(store.data['arena_checkout_steps:tok-1']!)).toEqual(['checkout_form_view']);
  });

  it('does not carry the marks over to another token', () => {
    const store = fakeStorage();
    const seen: string[] = [];
    const reg = createPaymentStepsRegistry((e) => seen.push(e.step), store);
    reg.forToken('a').emit('payment_succeeded');
    reg.forToken('b').emit('payment_succeeded');
    expect(seen).toEqual(['payment_succeeded', 'payment_succeeded']);
  });

  it('survives a storage that throws on read and on write', () => {
    const broken = {
      getItem: () => {
        throw new Error('blocked');
      },
      setItem: () => {
        throw new Error('full');
      },
    };
    const seen: string[] = [];
    const bus = createPaymentStepsRegistry((e) => seen.push(e.step), broken).forToken('t');
    bus.emit('checkout_form_view');
    bus.emit('checkout_form_view');
    expect(seen).toEqual(['checkout_form_view']);
  });

  it('ignores a corrupt stored value', () => {
    const store = fakeStorage({ 'arena_checkout_steps:t': 'not json{' });
    const seen: string[] = [];
    createPaymentStepsRegistry((e) => seen.push(e.step), store).forToken('t').emit('checkout_form_view');
    expect(seen).toEqual(['checkout_form_view']);
  });

  it('works with no storage at all', () => {
    const seen: string[] = [];
    const bus = createPaymentStepsRegistry((e) => seen.push(e.step), null).forToken('t');
    bus.emit('payment_succeeded');
    bus.emit('payment_succeeded');
    expect(seen).toEqual(['payment_succeeded']);
  });
});

describe('step bus per checkout token', () => {
  it('reports the form view again for a second purchase (a new token) in the same tab', () => {
    const seen: string[] = [];
    const reg = createPaymentStepsRegistry((e) => seen.push(e.step));
    reg.forToken('tok-1').emit('checkout_form_view');
    reg.forToken('tok-1').emit('checkout_form_view');
    reg.forToken('tok-2').emit('checkout_form_view');
    expect(seen).toEqual(['checkout_form_view', 'checkout_form_view']);
  });

  it('hands back the same bus for the same token, so a resume does not repeat once-only steps', () => {
    const reg = createPaymentStepsRegistry(() => {});
    expect(reg.forToken('a')).toBe(reg.forToken('a'));
    expect(reg.forToken('b')).not.toBe(reg.forToken('a'));
  });

  it('counts the details-complete step once per checkout', () => {
    const seen: string[] = [];
    const reg = createPaymentStepsRegistry((e) => seen.push(e.step));
    reg.forToken('a').emit('payment_details_complete');
    reg.forToken('a').emit('payment_details_complete');
    expect(seen).toEqual(['payment_details_complete']);
  });
});

describe('payment step bus', () => {
  it('hands each step to every subscriber, with its qualifiers', () => {
    const bus = createPaymentSteps();
    const a: PaymentStepEvent[] = [];
    const b: PaymentStepEvent[] = [];
    bus.subscribe((e) => a.push(e));
    bus.subscribe((e) => b.push(e));
    bus.emit('payment_declined', { group: 'no_funds', attempt: '2' });
    expect(a).toEqual([{ step: 'payment_declined', group: 'no_funds', attempt: '2' }]);
    expect(b).toEqual(a);
  });

  it('counts the form shown and the success once, but every submit and failure', () => {
    const bus = createPaymentSteps();
    const seen: string[] = [];
    bus.subscribe((e) => seen.push(e.step));
    bus.emit('checkout_form_view');
    bus.emit('checkout_form_view');
    bus.emit('pay_clicked');
    bus.emit('pay_clicked');
    bus.emit('payment_declined', { group: 'other', attempt: '1' });
    bus.emit('payment_succeeded');
    bus.emit('payment_succeeded');
    expect(seen).toEqual([
      'checkout_form_view',
      'pay_clicked',
      'pay_clicked',
      'payment_declined',
      'payment_succeeded',
    ]);
  });

  it('drops a repeated pick of the same method but reports a change', () => {
    const bus = createPaymentSteps();
    const methods: Array<string | undefined> = [];
    bus.subscribe((e) => methods.push(e.method));
    bus.emit('payment_method_selected', { method: 'card' });
    bus.emit('payment_method_selected', { method: 'card' });
    bus.emit('payment_method_selected', { method: 'link' });
    bus.emit('payment_method_selected', { method: 'card' });
    expect(methods).toEqual(['card', 'link', 'card']);
  });

  it('survives a listener that throws and supports unsubscribe', () => {
    const bus = createPaymentSteps();
    const ok = vi.fn();
    bus.subscribe(() => {
      throw new Error('broken');
    });
    const off = bus.subscribe(ok);
    bus.emit('pay_clicked');
    expect(ok).toHaveBeenCalledTimes(1);
    off();
    bus.emit('pay_clicked');
    expect(ok).toHaveBeenCalledTimes(1);
  });

  it('maps methods onto the closed list and attempts onto 1, 2, 3+', () => {
    expect(methodKind('card')).toBe('card');
    expect(methodKind('apple_pay')).toBe('apple_pay');
    expect(methodKind('googlePay')).toBe('google_pay');
    expect(methodKind('link')).toBe('link');
    expect(methodKind('klarna')).toBe('other');
    expect(methodKind(undefined)).toBe('other');
    expect([1, 2, 3, 9].map(attemptLabel)).toEqual(['1', '2', '3+', '3+']);
  });
});
