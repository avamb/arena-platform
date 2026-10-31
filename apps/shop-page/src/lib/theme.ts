/**
 * theme.ts - what the Shopify theme editor held for Einat Winery
 * (config/settings_data.json, header-group.json, footer-group.json) and the
 * public API has no field for. It is a static default, merged under whatever
 * the API sends later. TODO: this becomes per-organization settings (logo, banner,
 * social links, age-gate texts) served by the API; accepted as static for this version.
 *
 * The artwork (logo, banner, footer logo) is served from this app's own
 * /theme/brand/ folder, so nothing depends on the old shop's CDN once the
 * domain moves.
 */

export interface ThemeImage {
  url: string;
  width: number;
  height: number;
}

export interface ThemeConfig {
  shopName: string;
  /** Domains of the organizer's former/own site: links to them in CMS content stay inside the storefront. */
  domains: string[];
  logo: ThemeImage & { displayWidth: number };
  banner: ThemeImage;
  footerLogo: ThemeImage & { maxWidth: number };
  announcementUrl: string | null;
  social: { facebook: string; instagram: string };
  footerRulesHeading: string;
  brandHeadline: string;
  brandHtml: string;
  pageWidthPx: number;
  /** <title> and meta description of the home page (the live shop's SEO fields). */
  homeTitle: string;
  homeDescription: string;
  /** The age gate popup of the live site is in Hebrew (the buyers are in Israel). */
  ageGate: { heading: string; mark: string; allow: string; deny: string; exitUrl: string; lang: string };
}

export const EINAT_THEME: ThemeConfig = {
  shopName: 'Einat Winery',
  domains: ['einatwinery.com'],
  logo: {
    url: '/theme/brand/logo.jpg',
    width: 110,
    height: 110.20257826887661,
    displayWidth: 110,
  },
  banner: {
    url: '/theme/brand/banner.jpg',
    width: 1835,
    height: 288,
  },
  footerLogo: {
    url: '/theme/brand/footer-logo.png',
    width: 248,
    height: 349,
    maxWidth: 150,
  },
  announcementUrl: '/products/wineandglassesset',
  social: {
    facebook: 'https://www.facebook.com/vinoandcowinetastings',
    instagram: 'https://www.instagram.com/vino.and.co/',
  },
  footerRulesHeading: 'Наши правила',
  brandHeadline: 'Возвращайтесь снова',
  brandHtml: '<p>Мы делаем только лучшее для Вас<strong>❤️</strong></p>',
  pageWidthPx: 1600,
  homeTitle: 'Вина и винные мероприятия',
  homeDescription:
    'Андрей Макаревич и Эйнат Кляйн рады представить вам свое вино. Вина магазина Einat Winery. Мы предлагаем вам насладиться благородным напитком собственного производства, а также узнать подробности о наших винных мепроприятиях.',
  ageGate: {
    heading: 'הריני להצהיר שאני מעל גיל',
    mark: '+18',
    allow: 'מאשר',
    deny: 'לא מאשר',
    exitUrl: 'https://www.google.com/',
    lang: 'he',
  },
};
