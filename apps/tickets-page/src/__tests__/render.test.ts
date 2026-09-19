import { describe, expect, it, beforeEach } from 'vitest';
import { renderError, renderEvent, renderLoading, renderNotFound, renderPromoterPage } from '../lib/render.ts';
import type { HostedPageEvent, HostedPageResponse, HostedPromoterPageResponse } from '../lib/api.ts';

function freshContainer(): HTMLElement {
  const el = document.createElement('main');
  document.body.appendChild(el);
  return el;
}

beforeEach(() => {
  document.body.innerHTML = '';
});

describe('renderNotFound', () => {
  it('renders the branded not-found state with a link home', () => {
    const container = freshContainer();
    renderNotFound(container, 'en');
    expect(container.querySelector('h1')?.textContent).toBe('Event not found');
    const link = container.querySelector('a.asa-button');
    expect(link).not.toBeNull();
    expect(link?.getAttribute('href')).toBe('/');
  });

  it('localizes the not-found copy for ru', () => {
    const container = freshContainer();
    renderNotFound(container, 'ru');
    expect(container.querySelector('h1')?.textContent).toBe('Событие не найдено');
  });

  it('localizes the not-found copy for es', () => {
    const container = freshContainer();
    renderNotFound(container, 'es');
    expect(container.querySelector('h1')?.textContent).toBe('Evento no encontrado');
  });
});

describe('renderLoading', () => {
  it('renders a status role element', () => {
    const container = freshContainer();
    renderLoading(container, 'en');
    const status = container.querySelector('[role="status"]');
    expect(status?.textContent).toBe('Loading event…');
  });
});

describe('renderError', () => {
  it('renders a retry button that invokes the callback', () => {
    const container = freshContainer();
    let retried = false;
    renderError(container, 'en', () => {
      retried = true;
    });
    const button = container.querySelector('button.asa-button');
    expect(button).not.toBeNull();
    (button as HTMLButtonElement).click();
    expect(retried).toBe(true);
  });
});

describe('renderEvent', () => {
  const sampleData: HostedPageResponse = {
    org: { slug: 'arenasoldout', name: 'Arena Sold Out', logo_url: null },
    event: {
      id: '01929d0e-0e47-7000-8000-000000000301',
      slug: 'summer-festival',
      title: 'Summer Festival 2026',
      description: 'Open-air festival.',
      short_description: 'One night, three stages.',
      image_url: null,
      poster_url: 'https://example.com/poster.jpg',
      age_rating: '16+',
      venue_names: ['Forum Karlin'],
      first_session_at: '2026-08-15T18:00:00Z',
      last_session_at: '2026-08-15T22:00:00Z',
      first_session_timezone: 'Europe/Prague',
    },
    feed_token: 'ft_abc123',
    default_locale: 'en',
  };

  it('mounts the arena-tickets widget with the right attributes', () => {
    const container = freshContainer();
    renderEvent(container, sampleData, 'en', { apiBase: 'https://api.example.com', resumingCheckout: false });

    const widget = container.querySelector('arena-tickets');
    expect(widget).not.toBeNull();
    expect(widget?.getAttribute('feed-token')).toBe('ft_abc123');
    expect(widget?.getAttribute('event-id')).toBe(sampleData.event.id);
    expect(widget?.getAttribute('locale')).toBe('en');
    expect(widget?.getAttribute('api-base')).toBe('https://api.example.com');
  });

  it('maps the page-only es locale onto en for the widget locale attribute', () => {
    const container = freshContainer();
    renderEvent(container, sampleData, 'es', { apiBase: '', resumingCheckout: false });
    const widget = container.querySelector('arena-tickets');
    expect(widget?.getAttribute('locale')).toBe('en');
  });

  it('renders the hero title, description and poster image', () => {
    const container = freshContainer();
    renderEvent(container, sampleData, 'en', { apiBase: '', resumingCheckout: false });

    expect(container.querySelector('.asa-hero-title')?.textContent).toBe('Summer Festival 2026');
    expect(container.querySelector('.asa-hero-description')?.textContent).toBe('One night, three stages.');
    const img = container.querySelector('.asa-hero-image') as HTMLImageElement | null;
    expect(img?.src).toBe('https://example.com/poster.jpg');
  });

  it('renders a back link to the org promoter page', () => {
    const container = freshContainer();
    renderEvent(container, sampleData, 'en', { apiBase: '', resumingCheckout: false });

    const back = container.querySelector('a.asa-back-link') as HTMLAnchorElement | null;
    expect(back).not.toBeNull();
    expect(back?.getAttribute('href')).toBe('/arenasoldout');
    expect(back?.textContent).toContain('All dates');
  });

  it('preserves the query string on the back link', () => {
    const originalSearch = window.location.search;
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?lang=ru');
    const container = freshContainer();
    renderEvent(container, sampleData, 'ru', { apiBase: '', resumingCheckout: false });
    const back = container.querySelector('a.asa-back-link') as HTMLAnchorElement | null;
    expect(back?.getAttribute('href')).toBe('/arenasoldout?lang=ru');
    window.history.replaceState(null, '', '/' + originalSearch);
  });
});

describe('renderPromoterPage', () => {
  const eventWithPoster: HostedPageEvent = {
    id: '01929d0e-0e47-7000-8000-000000000401',
    slug: 'masterclass-day-1',
    title: 'Master Class — Day 1',
    description: null,
    short_description: 'An intensive one-day acting workshop.',
    image_url: null,
    poster_url: 'https://example.com/day1.jpg',
    age_rating: null,
    venue_names: ['Studio A'],
    first_session_at: '2026-10-01T17:00:00Z',
    last_session_at: '2026-10-01T20:00:00Z',
    first_session_timezone: 'Europe/Prague',
  };

  const eventWithoutPoster: HostedPageEvent = {
    id: '01929d0e-0e47-7000-8000-000000000402',
    slug: 'masterclass-day-2',
    title: 'Master Class — Day 2',
    description: null,
    short_description: null,
    image_url: null,
    poster_url: null,
    age_rating: null,
    venue_names: [],
    first_session_at: null,
    last_session_at: null,
    first_session_timezone: null,
  };

  const sampleData: HostedPromoterPageResponse = {
    org: { slug: 'masterclassteatro', name: 'Master Class Teatro', logo_url: 'https://example.com/logo.png' },
    default_locale: 'en',
    events: [eventWithPoster, eventWithoutPoster],
  };

  it('renders the org name and logo', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en');
    expect(container.querySelector('.asa-promoter-title')?.textContent).toBe('Master Class Teatro');
    const logo = container.querySelector('.asa-promoter-logo') as HTMLImageElement | null;
    expect(logo?.src).toBe('https://example.com/logo.png');
  });

  it('renders one card per event, linking to the per-event page', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en');
    const cards = container.querySelectorAll('a.asa-card');
    expect(cards.length).toBe(2);
    expect(cards[0].getAttribute('href')).toBe('/masterclassteatro/masterclass-day-1');
    expect(cards[1].getAttribute('href')).toBe('/masterclassteatro/masterclass-day-2');
  });

  it('renders a poster thumbnail when present', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en');
    const img = container.querySelector('a.asa-card img.asa-card-image') as HTMLImageElement | null;
    expect(img?.src).toBe('https://example.com/day1.jpg');
  });

  it('renders without a poster thumbnail when none is set', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en');
    const cards = container.querySelectorAll('a.asa-card');
    const secondCard = cards[1];
    expect(secondCard.querySelector('img.asa-card-image')).toBeNull();
  });

  it('shows the date/time before the title in each card', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en');
    const firstBody = container.querySelectorAll('a.asa-card')[0].querySelector('.asa-card-body');
    const children = Array.from(firstBody?.children ?? []);
    const dateIndex = children.findIndex((el) => el.classList.contains('asa-card-datetime'));
    const titleIndex = children.findIndex((el) => el.classList.contains('asa-card-title'));
    expect(dateIndex).toBeGreaterThanOrEqual(0);
    expect(titleIndex).toBeGreaterThan(dateIndex);
  });

  it('renders the localized empty state when there are no events', () => {
    const container = freshContainer();
    renderPromoterPage(container, { ...sampleData, events: [] }, 'en');
    expect(container.querySelector('.asa-state--empty h2')?.textContent).toBe('No upcoming dates yet');
    expect(container.querySelectorAll('a.asa-card').length).toBe(0);
  });

  it('localizes the empty state for es', () => {
    const container = freshContainer();
    renderPromoterPage(container, { ...sampleData, events: [] }, 'es');
    expect(container.querySelector('.asa-state--empty h2')?.textContent).toBe('Aún no hay fechas próximas');
  });
});
