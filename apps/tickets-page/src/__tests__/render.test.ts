import { describe, expect, it, beforeEach } from 'vitest';
import { eventIDWithOpenCheckout, renderError, renderEvent, renderLoading, renderNotFound, renderPromoterPage } from '../lib/render.ts';
import type { PromoterPageOptions } from '../lib/render.ts';
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
  /** Slugs the page asked to resolve, in order — proof that a row fetches
   * exactly when it is opened and never twice. */
  let resolvedSlugs: string[] = [];

  beforeEach(() => {
    resolvedSlugs = [];
  });

  /** Lets the awaited resolve and the render that follows it settle. */
  async function flush(): Promise<void> {
    await Promise.resolve();
    await Promise.resolve();
  }

  function pageOptions(): PromoterPageOptions {
    return {
      apiBase: 'https://api.example.com',
      resolveEvent: (slug) => {
        resolvedSlugs.push(slug);
        const event = sampleData.events.find((e) => e.slug === slug) ?? sampleData.events[0];
        return Promise.resolve({
          org: sampleData.org,
          event,
          feed_token: 'ft_abc123',
          default_locale: 'en',
        });
      },
    };
  }

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
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    expect(container.querySelector('.asa-promoter-title')?.textContent).toBe('Master Class Teatro');
    const logo = container.querySelector('.asa-promoter-logo') as HTMLImageElement | null;
    expect(logo?.src).toBe('https://example.com/logo.png');
  });

  it('renders one date row per event', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    const rows = container.querySelectorAll('.asa-date-row');
    expect(rows.length).toBe(2);
    expect(rows[0].getAttribute('aria-expanded')).toBe('false');
  });

  // The whole point of the list: a buyer picks a quantity here, and the
  // per-event page never enters the flow.
  it('opens the ticket picker inside the row it belongs to', async () => {
    const container = freshContainer();
    const options = pageOptions();
    renderPromoterPage(container, sampleData, 'en', options);

    const row = container.querySelectorAll('.asa-date-row')[0] as HTMLButtonElement;
    const panel = container.querySelector('.asa-date-panel') as HTMLElement;
    expect(panel.hidden).toBe(true);

    row.click();
    await flush();

    expect(row.getAttribute('aria-expanded')).toBe('true');
    expect(row.getAttribute('aria-controls')).toBe(panel.id);
    expect(panel.hidden).toBe(false);
    expect(resolvedSlugs).toEqual(['masterclass-day-1']);

    const widget = panel.querySelector('arena-tickets');
    expect(widget?.getAttribute('feed-token')).toBe('ft_abc123');
    expect(widget?.getAttribute('event-id')).toBe(eventWithPoster.id);
    expect(widget?.getAttribute('api-base')).toBe('https://api.example.com');
    // What the event is about is the one thing the row header does not
    // say, so it stays. The artwork and the date do not: both are already
    // on screen, above the row and in it.
    expect(panel.querySelector('.asa-date-panel__description')?.textContent).toBe(
      'An intensive one-day acting workshop.',
    );
    expect(widget?.getAttribute('cover')).toBe('hidden');
    expect(widget?.getAttribute('sessions')).toBe('hidden');
    expect(panel.querySelector('img')).toBeNull();
  });

  it('keeps the widget mounted when a row is closed and reopened', async () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    const row = container.querySelectorAll('.asa-date-row')[0] as HTMLButtonElement;

    row.click();
    await flush();
    row.click();
    expect(row.getAttribute('aria-expanded')).toBe('false');
    expect((container.querySelector('.asa-date-panel') as HTMLElement).hidden).toBe(true);

    row.click();
    await flush();
    // Reopening must not fetch again, or a buyer toggling the row would
    // throw away the cart they had already started.
    expect(resolvedSlugs).toEqual(['masterclass-day-1']);
    expect(container.querySelectorAll('arena-tickets').length).toBe(1);
  });

  // Two open carts on one page means two live seat holds.
  it('closes the other rows when one is opened', async () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    const rows = container.querySelectorAll('.asa-date-row');

    (rows[0] as HTMLButtonElement).click();
    await flush();
    (rows[1] as HTMLButtonElement).click();
    await flush();

    expect(rows[0].getAttribute('aria-expanded')).toBe('false');
    expect(rows[1].getAttribute('aria-expanded')).toBe('true');
  });

  it('offers a retry when the ticket picker cannot be loaded', async () => {
    const container = freshContainer();
    const options = pageOptions();
    options.resolveEvent = () => Promise.reject(new Error('offline'));
    renderPromoterPage(container, sampleData, 'en', options);

    (container.querySelector('.asa-date-row') as HTMLButtonElement).click();
    await flush();

    const panel = container.querySelector('.asa-date-panel') as HTMLElement;
    expect(panel.querySelector('.asa-date-panel__error')).not.toBeNull();
    expect(panel.querySelector('arena-tickets')).toBeNull();
    expect(panel.querySelector('button.asa-button')?.textContent).toBe('Retry');
  });

  // A promoter runs a season off ONE artwork, so it belongs at the top of
  // the page once, at full size — not repeated on every row, which made
  // six different master classes look like the same thing six times.
  it('promotes the one shared poster to the top and shows none on a row', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    const poster = container.querySelector('.asa-promoter-poster') as HTMLImageElement | null;
    expect(poster?.src).toBe('https://example.com/day1.jpg');
    expect(container.querySelector('a.asa-date-row img')).toBeNull();
  });

  // Two different pictures mean this is not one season, and showing one
  // event's poster over another event's row would be a lie.
  it('promotes no poster when the events carry different artwork', () => {
    const container = freshContainer();
    renderPromoterPage(
      container,
      {
        ...sampleData,
        events: [eventWithPoster, { ...eventWithoutPoster, poster_url: 'https://example.com/other.jpg' }],
      },
      'en',
      pageOptions(),
    );
    expect(container.querySelector('.asa-promoter-poster')).toBeNull();
  });

  it('shows the day, month and time of each date before the event title', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    const row = container.querySelectorAll('.asa-date-row')[0];
    const children = Array.from(row.children);
    const whenIndex = children.findIndex((el) => el.classList.contains('asa-date-row__when'));
    const whatIndex = children.findIndex((el) => el.classList.contains('asa-date-row__what'));
    expect(whenIndex).toBeGreaterThanOrEqual(0);
    expect(whatIndex).toBeGreaterThan(whenIndex);
    // 17:00 UTC on 1 October is 19:00 in Prague — the event's own zone.
    expect(row.querySelector('.asa-date-row__day')?.textContent).toBe('1');
    expect(row.querySelector('.asa-date-row__time')?.textContent).toContain('07:00 PM');
    expect(row.querySelector('.asa-date-row__title')?.textContent).toBe('Master Class — Day 1');
  });

  it('shows a multi-day event as a span of days with no single start time', () => {
    const container = freshContainer();
    renderPromoterPage(
      container,
      {
        ...sampleData,
        events: [
          {
            ...eventWithPoster,
            first_session_at: '2026-10-16T10:00:00Z',
            last_session_at: '2026-10-18T20:00:00Z',
          },
        ],
      },
      'en',
      pageOptions(),
    );
    expect(container.querySelector('.asa-date-row__day')?.textContent).toBe('16–18');
    expect(container.querySelector('.asa-date-row__time')).toBeNull();
  });

  it('offers no tickets on a date that has already happened', () => {
    const container = freshContainer();
    renderPromoterPage(
      container,
      {
        ...sampleData,
        events: [
          {
            ...eventWithPoster,
            first_session_at: '2020-03-01T17:00:00Z',
            last_session_at: '2020-03-01T20:00:00Z',
          },
        ],
      },
      'en',
      pageOptions(),
    );
    const row = container.querySelector('.asa-date-row');
    expect(row?.classList.contains('asa-date-row--past')).toBe(true);
    expect(row?.querySelector('.asa-date-row__cta')?.textContent).toBe('Took place');
  });

  it('heads the list only when there is a choice of dates to make', () => {
    const many = freshContainer();
    renderPromoterPage(many, sampleData, 'en', pageOptions());
    expect(many.querySelector('.asa-dates__head')?.textContent).toBe('Choose a date');

    const one = freshContainer();
    renderPromoterPage(one, { ...sampleData, events: [eventWithPoster] }, 'en', pageOptions());
    expect(one.querySelector('.asa-dates__head')).toBeNull();
    expect(one.querySelectorAll('.asa-date-row').length).toBe(1);
  });

  // Stripe sends a buyer back to the list, not to the row they bought
  // from, and the return URL carries no event id. Without this they land
  // on closed rows with nothing saying the payment went through — which
  // is what happened on the first real purchase.
  describe('returning from the payment page', () => {
    function storageWith(entries: Record<string, string>): Storage {
      return {
        getItem: (k: string) => entries[k] ?? null,
      } as unknown as Storage;
    }

    it('finds the event whose checkout the widget left open', () => {
      const storage = storageWith({
        [`arena_checkout_token:${eventWithoutPoster.id}`]: 'ct_abc',
      });
      expect(eventIDWithOpenCheckout(sampleData.events, storage)).toBe(eventWithoutPoster.id);
    });

    it('finds nothing when no checkout is open, and survives unusable storage', () => {
      expect(eventIDWithOpenCheckout(sampleData.events, storageWith({}))).toBeNull();
      expect(eventIDWithOpenCheckout(sampleData.events, null)).toBeNull();
      const throwing = {
        getItem: () => {
          throw new Error('blocked');
        },
      } as unknown as Storage;
      expect(eventIDWithOpenCheckout(sampleData.events, throwing)).toBeNull();
    });

    it('opens that row by itself so the buyer sees the outcome', async () => {
      const container = freshContainer();
      const options = pageOptions();
      options.openEventID = eventWithoutPoster.id;
      renderPromoterPage(container, sampleData, 'en', options);
      await flush();

      const rows = container.querySelectorAll('.asa-date-row');
      expect(rows[0].getAttribute('aria-expanded')).toBe('false');
      expect(rows[1].getAttribute('aria-expanded')).toBe('true');
      expect(resolvedSlugs).toEqual([eventWithoutPoster.slug]);
    });

    it('leaves every row closed when the id names nothing on the page', async () => {
      const container = freshContainer();
      const options = pageOptions();
      options.openEventID = 'not-on-this-page';
      renderPromoterPage(container, sampleData, 'en', options);
      await flush();

      for (const row of container.querySelectorAll('.asa-date-row')) {
        expect(row.getAttribute('aria-expanded')).toBe('false');
      }
      expect(resolvedSlugs).toEqual([]);
    });
  });

  it('renders the localized empty state when there are no events', () => {
    const container = freshContainer();
    renderPromoterPage(container, { ...sampleData, events: [] }, 'en', pageOptions());
    expect(container.querySelector('.asa-state--empty h2')?.textContent).toBe('No upcoming dates yet');
    expect(container.querySelectorAll('.asa-date-row').length).toBe(0);
  });

  it('localizes the empty state for es', () => {
    const container = freshContainer();
    renderPromoterPage(container, { ...sampleData, events: [] }, 'es', pageOptions());
    expect(container.querySelector('.asa-state--empty h2')?.textContent).toBe('Aún no hay fechas próximas');
  });
});
