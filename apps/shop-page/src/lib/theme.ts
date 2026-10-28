/**
 * theme.ts - what the Shopify theme editor held for Einat Winery
 * (config/settings_data.json, header-group.json, footer-group.json) and the
 * public API has no field for. It is a static default, merged under whatever
 * the API sends later; moving it into organization data is a follow-up.
 *
 * Images still point at the old shop's CDN. Before the domain moves they have
 * to be re-uploaded as arena media (README, "Before the domain moves").
 */

export interface ThemeImage {
  url: string;
  width: number;
  height: number;
}

export interface ThemeConfig {
  shopName: string;
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
  ageGate: { heading: string; mark: string; allow: string; deny: string; exitUrl: string };
}

export const EINAT_THEME: ThemeConfig = {
  shopName: 'Einat Winery',
  logo: {
    url: 'https://www.einatwinery.com/cdn/shop/files/vinoandco_1.webp?v=1699887012',
    width: 110,
    height: 110.20257826887661,
    displayWidth: 110,
  },
  banner: {
    url: 'https://www.einatwinery.com/cdn/shop/files/c2a345_37bd7d92a3734c319ce31fc30c75adfc_mv2_1.webp?v=1699885588',
    width: 1835,
    height: 288,
  },
  footerLogo: {
    url: 'https://www.einatwinery.com/cdn/shop/files/Logo_Vino_CoWhite_240fe8fb-8bf7-4dfa-a631-3504d1983243.png?v=1701091456',
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
  },
};
