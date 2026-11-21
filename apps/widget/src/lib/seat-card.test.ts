// @vitest-environment jsdom
import { describe, it, expect } from 'vitest';
import { createSeatCard, seatCardModel, type SeatCardInput } from './seat-card.js';

const labels = { sector: 'Sector', row: 'Row', seat: 'Seat' };

function base(extra: Partial<SeatCardInput> = {}): SeatCardInput {
  return {
    sector: 'Parter',
    row: '3',
    seat: '12',
    category: 'Gold',
    color: '#d97706',
    price: '22.00 EUR',
    labels,
    ...extra,
  };
}

const texts = (input: SeatCardInput) => seatCardModel(input).lines.map((l) => `${l.kind}:${l.text}`);

describe('seatCardModel', () => {
  it('a free seat: place, category, price and no state line', () => {
    expect(texts(base())).toEqual(['place:Sector Parter · Row 3 · Seat 12', 'category:Gold', 'price:22.00 EUR']);
  });

  it('keeps the category colour on the category line', () => {
    expect(seatCardModel(base()).lines[1]?.color).toBe('#d97706');
  });

  it('a selected seat says so', () => {
    const l = seatCardModel(base({ state: { tone: 'selected', text: 'Selected' } })).lines;
    expect(l[l.length - 1]).toEqual({ kind: 'state', text: 'Selected', tone: 'selected' });
  });

  it('a sold seat carries the sold state', () => {
    expect(texts(base({ state: { tone: 'sold', text: 'Sold' } }))).toContain('state:Sold');
  });

  it('a held seat carries the held state', () => {
    expect(texts(base({ state: { tone: 'held', text: 'In a hold' } }))).toContain('state:In a hold');
  });

  it('a closed seat carries the closed state', () => {
    expect(texts(base({ state: { tone: 'closed', text: 'Not on sale' } }))).toContain('state:Not on sale');
  });

  it('a state of tone free adds no line', () => {
    expect(texts(base({ state: { tone: 'free', text: 'Free' } })).some((x) => x.startsWith('state:'))).toBe(false);
  });

  it('a pending change is its own last line', () => {
    const l = seatCardModel(base({ state: { tone: 'closed', text: 'Closed' }, pending: 'Not applied yet: Silver, closed' })).lines;
    expect(l[l.length - 1]).toEqual({ kind: 'pending', text: 'Not applied yet: Silver, closed', tone: 'changed' });
  });

  it('a GA unit has no row and no seat', () => {
    expect(texts(base({ sector: null, row: null, seat: undefined, category: 'Standing' }))).toEqual([
      'category:Standing',
      'price:22.00 EUR',
    ]);
  });

  it('a sector alone is a place line of its own', () => {
    expect(texts(base({ row: '', seat: '' }))[0]).toBe('place:Sector Parter');
  });

  it('a missing or blank price adds no price line', () => {
    expect(texts(base({ price: null })).some((x) => x.startsWith('price:'))).toBe(false);
    expect(texts(base({ price: '  ' })).some((x) => x.startsWith('price:'))).toBe(false);
  });

  it('takes the words from the labels it is given', () => {
    expect(texts(base({ labels: { sector: 'Sektor', row: 'Řada', seat: 'Sedadlo' } }))[0]).toBe(
      'place:Sektor Parter · Řada 3 · Sedadlo 12',
    );
  });

  it('knows nothing: no lines', () => {
    expect(seatCardModel({ labels }).lines).toEqual([]);
  });
});

describe('createSeatCard', () => {
  function mount() {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const card = createSeatCard(host);
    return { host, card };
  }
  const anchor = { left: 100, top: 100, right: 110, bottom: 110 };

  it('is a hidden tooltip that cannot take focus', () => {
    const { card } = mount();
    expect(card.element.getAttribute('role')).toBe('tooltip');
    expect(card.element.hidden).toBe(true);
    expect(card.element.hasAttribute('tabindex')).toBe(false);
  });

  it('shows the lines and hides again', () => {
    const { card } = mount();
    card.show(seatCardModel(base()), anchor);
    expect(card.isOpen()).toBe(true);
    expect(card.element.hidden).toBe(false);
    expect(card.element.textContent).toContain('Sector Parter · Row 3 · Seat 12');
    expect(card.element.textContent).toContain('22.00 EUR');
    card.hide();
    expect(card.isOpen()).toBe(false);
    expect(card.element.hidden).toBe(true);
  });

  it('an empty model hides the card', () => {
    const { card } = mount();
    card.show(seatCardModel(base()), anchor);
    card.show(seatCardModel({ labels }), anchor);
    expect(card.isOpen()).toBe(false);
  });

  it('puts an action button in the card only when the model carries one', () => {
    const { card } = mount();
    let pressed = 0;
    card.show({ ...seatCardModel(base()), action: { label: 'Open', run: () => pressed++ } }, anchor);
    card.element.querySelector('button')?.click();
    expect(pressed).toBe(1);
    expect(card.element.classList.contains('has-action')).toBe(true);
    card.show(seatCardModel(base()), anchor);
    expect(card.element.querySelector('button')).toBeNull();
    expect(card.element.classList.contains('has-action')).toBe(false);
  });

  it('destroy removes the card from the page', () => {
    const { host, card } = mount();
    card.destroy();
    expect(host.contains(card.element)).toBe(false);
  });

  it('leaves its stylesheet out when the app loads it itself', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    document.head.querySelectorAll('style[data-arena-seat-card-css]').forEach((n) => n.remove());
    createSeatCard(host, { injectStyles: false });
    expect(document.head.querySelector('style[data-arena-seat-card-css]')).toBeNull();
  });
});
