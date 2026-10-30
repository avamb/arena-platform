/**
 * links.ts - which hosts count as "this shop" for links typed into CMS content. The domains come from
 * the theme (the organizer's own site) and from the shop's configured domain, never from a literal.
 */

/** True when `hostname` is one of `domains` or a subdomain of it ("www.einatwinery.com" for "einatwinery.com"). */
export function isShopHost(hostname: string, domains: (string | null | undefined)[]): boolean {
  const h = hostname.toLowerCase();
  return domains.some((d) => {
    const dom = (d ?? '').trim().toLowerCase().replace(/^www\./, '');
    return dom !== '' && (h === dom || h.endsWith('.' + dom));
  });
}
