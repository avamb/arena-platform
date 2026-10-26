import { describe, it, expect } from 'vitest';
import { priceLabelBesideName, priceLabelRepeatsName } from './priceLabel.js';

describe('priceLabelBesideName (UXH-07)', () => {
  it('drops a label that repeats the category name', () => {
    expect(priceLabelBesideName('Free', 'Free')).toBe('');
  });

  it('compares case-insensitively and trimmed', () => {
    expect(priceLabelBesideName('  free ', 'Free')).toBe('');
    expect(priceLabelRepeatsName('FREE', ' free')).toBe(true);
  });

  it('keeps the label when the name differs', () => {
    expect(priceLabelBesideName('Standing', 'Free')).toBe('Free');
    expect(priceLabelBesideName('Free entry', 'Free')).toBe('Free');
    expect(priceLabelBesideName('VIP', '50 EUR')).toBe('50 EUR');
  });

  it('keeps the label for an empty name', () => {
    expect(priceLabelBesideName('', 'Free')).toBe('Free');
    expect(priceLabelBesideName(undefined, 'Free')).toBe('Free');
  });
});
