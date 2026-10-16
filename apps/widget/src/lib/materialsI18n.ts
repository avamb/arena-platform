/**
 * materialsI18n.ts — the words of the event materials block (task 2-2):
 * gallery, videos, map, "where else to buy" and the photo report.
 *
 * Like the promo and consent words, this table carries all seven buyer
 * languages (en, ru, cs, he, es, de, fr) and is looked up by language subtag
 * ("es-ES" -> es), so the block reads in the buyer's language even where the
 * rest of the widget falls back to English. Every locale must carry every key
 * (materials.test.ts checks it).
 */

export interface MaterialsStrings {
  /** Section headings. */
  gallery: string;
  videos: string;
  map: string;
  partners: string;
  report: string;
  /** Screen-reader prefix of a gallery thumbnail button: "Open photo: <caption>". */
  photoOpen: string;
  /** Label of the lightbox when a photo has no caption. */
  photoN: string;
  lightboxClose: string;
  lightboxPrev: string;
  lightboxNext: string;
  /** The play button before a video is loaded: "Play video · YouTube". */
  videoPlay: string;
  /** Title of the iframe and name of a plain video link: "Video 2". */
  videoLabel: string;
  /** "Open on YouTube". */
  videoOpenOn: string;
  mapCoordinates: string;
  mapOpenOsm: string;
  mapOpenGoogle: string;
  /** "Seller: <name>" beside a partner link. */
  sellerLabel: string;
  partnerOpen: string;
  /** Screen-reader hint on an outbound link. */
  newTab: string;
}

export const MATERIALS_I18N: Record<string, MaterialsStrings> = {
  en: {
    gallery: 'Photos',
    videos: 'Videos',
    map: 'How to get there',
    partners: 'Where else to buy',
    report: 'Photo report',
    photoOpen: 'Open photo',
    photoN: 'Photo',
    lightboxClose: 'Close',
    lightboxPrev: 'Previous photo',
    lightboxNext: 'Next photo',
    videoPlay: 'Play video',
    videoLabel: 'Video',
    videoOpenOn: 'Open on',
    mapCoordinates: 'Coordinates',
    mapOpenOsm: 'Open in OpenStreetMap',
    mapOpenGoogle: 'Open in Google Maps',
    sellerLabel: 'Seller',
    partnerOpen: 'Go to the seller',
    newTab: 'opens in a new tab',
  },
  ru: {
    gallery: 'Фотографии',
    videos: 'Видео',
    map: 'Как добраться',
    partners: 'Где ещё купить билеты',
    report: 'Фотоотчёт',
    photoOpen: 'Открыть фото',
    photoN: 'Фото',
    lightboxClose: 'Закрыть',
    lightboxPrev: 'Предыдущее фото',
    lightboxNext: 'Следующее фото',
    videoPlay: 'Смотреть видео',
    videoLabel: 'Видео',
    videoOpenOn: 'Открыть на',
    mapCoordinates: 'Координаты',
    mapOpenOsm: 'Открыть в OpenStreetMap',
    mapOpenGoogle: 'Открыть в Google Картах',
    sellerLabel: 'Продавец',
    partnerOpen: 'Перейти к продавцу',
    newTab: 'откроется в новой вкладке',
  },
  cs: {
    gallery: 'Fotografie',
    videos: 'Videa',
    map: 'Jak se tam dostat',
    partners: 'Kde ještě koupit vstupenky',
    report: 'Fotoreportáž',
    photoOpen: 'Otevřít fotografii',
    photoN: 'Fotografie',
    lightboxClose: 'Zavřít',
    lightboxPrev: 'Předchozí fotografie',
    lightboxNext: 'Další fotografie',
    videoPlay: 'Přehrát video',
    videoLabel: 'Video',
    videoOpenOn: 'Otevřít na',
    mapCoordinates: 'Souřadnice',
    mapOpenOsm: 'Otevřít v OpenStreetMap',
    mapOpenGoogle: 'Otevřít v Mapách Google',
    sellerLabel: 'Prodejce',
    partnerOpen: 'Přejít k prodejci',
    newTab: 'otevře se na nové kartě',
  },
  he: {
    gallery: 'תמונות',
    videos: 'סרטונים',
    map: 'איך מגיעים',
    partners: 'איפה עוד אפשר לקנות',
    report: 'תיעוד בתמונות',
    photoOpen: 'פתיחת תמונה',
    photoN: 'תמונה',
    lightboxClose: 'סגירה',
    lightboxPrev: 'התמונה הקודמת',
    lightboxNext: 'התמונה הבאה',
    videoPlay: 'הפעלת סרטון',
    videoLabel: 'סרטון',
    videoOpenOn: 'פתיחה ב־',
    mapCoordinates: 'קואורדינטות',
    mapOpenOsm: 'פתיחה ב־OpenStreetMap',
    mapOpenGoogle: 'פתיחה ב־Google Maps',
    sellerLabel: 'המוכר',
    partnerOpen: 'מעבר אל המוכר',
    newTab: 'ייפתח בכרטיסייה חדשה',
  },
  es: {
    gallery: 'Fotos',
    videos: 'Vídeos',
    map: 'Cómo llegar',
    partners: 'Dónde más comprar',
    report: 'Reportaje fotográfico',
    photoOpen: 'Abrir foto',
    photoN: 'Foto',
    lightboxClose: 'Cerrar',
    lightboxPrev: 'Foto anterior',
    lightboxNext: 'Foto siguiente',
    videoPlay: 'Reproducir vídeo',
    videoLabel: 'Vídeo',
    videoOpenOn: 'Abrir en',
    mapCoordinates: 'Coordenadas',
    mapOpenOsm: 'Abrir en OpenStreetMap',
    mapOpenGoogle: 'Abrir en Google Maps',
    sellerLabel: 'Vendedor',
    partnerOpen: 'Ir al vendedor',
    newTab: 'se abre en una pestaña nueva',
  },
  de: {
    gallery: 'Fotos',
    videos: 'Videos',
    map: 'Anfahrt',
    partners: 'Wo es sonst Tickets gibt',
    report: 'Fotobericht',
    photoOpen: 'Foto öffnen',
    photoN: 'Foto',
    lightboxClose: 'Schließen',
    lightboxPrev: 'Vorheriges Foto',
    lightboxNext: 'Nächstes Foto',
    videoPlay: 'Video abspielen',
    videoLabel: 'Video',
    videoOpenOn: 'Öffnen auf',
    mapCoordinates: 'Koordinaten',
    mapOpenOsm: 'In OpenStreetMap öffnen',
    mapOpenGoogle: 'In Google Maps öffnen',
    sellerLabel: 'Verkäufer',
    partnerOpen: 'Zum Verkäufer',
    newTab: 'öffnet sich in einem neuen Tab',
  },
  fr: {
    gallery: 'Photos',
    videos: 'Vidéos',
    map: 'Comment venir',
    partners: 'Où acheter ailleurs',
    report: 'Reportage photo',
    photoOpen: 'Ouvrir la photo',
    photoN: 'Photo',
    lightboxClose: 'Fermer',
    lightboxPrev: 'Photo précédente',
    lightboxNext: 'Photo suivante',
    videoPlay: 'Lire la vidéo',
    videoLabel: 'Vidéo',
    videoOpenOn: 'Ouvrir sur',
    mapCoordinates: 'Coordonnées',
    mapOpenOsm: 'Ouvrir dans OpenStreetMap',
    mapOpenGoogle: 'Ouvrir dans Google Maps',
    sellerLabel: 'Vendeur',
    partnerOpen: 'Aller chez le vendeur',
    newTab: "s'ouvre dans un nouvel onglet",
  },
};

/** The words for a locale ("es-ES" -> es), English when the language has none. */
export function getMaterialsStrings(locale: string): MaterialsStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return MATERIALS_I18N[lang] ?? MATERIALS_I18N.en!;
}
