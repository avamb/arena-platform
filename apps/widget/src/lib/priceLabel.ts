/**
 * priceLabel.ts — the price text printed next to a category name.
 *
 * A free category is often simply named "Free", and the price label of a free
 * category is also "Free": the row then read "Free Free". When the label
 * repeats the name (case-insensitive, trimmed) it is shown once, as the name.
 */

/** True when the price label would only repeat the category name. */
export function priceLabelRepeatsName(name: string | null | undefined, label: string | null | undefined): boolean {
  const n = (name ?? '').trim().toLowerCase();
  const l = (label ?? '').trim().toLowerCase();
  return n !== '' && n === l;
}

/** The price label to print beside `name`, or '' when it would repeat the name. */
export function priceLabelBesideName(name: string | null | undefined, label: string): string {
  return priceLabelRepeatsName(name, label) ? '' : label;
}
