import { describe, expect, it, beforeEach } from 'vitest';
import { consumeCheckoutTokenFromURL, eventIDWithOpenCheckout, isPosterCatalog, renderError, renderEvent, renderLoading, renderNotFound, renderPromoterPage } from '../lib/render.ts';
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
  function pageOptions(): PromoterPageOptions {
    return { apiBase: 'https://api.example.com' };
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
    feed_token: 'ft_abc123',
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
    feed_token: 'ft_abc123',
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

  it('renders one date card per event', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());
    expect(container.querySelectorAll('.asa-date-item').length).toBe(2);
    expect(container.querySelectorAll('.asa-date-row').length).toBe(2);
  });

  // The whole point of the list: a buyer picks a quantity here, with
  // nothing to press first and the per-event page never in the flow.
  it('renders every ticket picker open, with no control to unfold it', () => {
    const container = freshContainer();
    renderPromoterPage(container, sampleData, 'en', pageOptions());

    const panels = container.querySelectorAll('.asa-date-panel');
    expect(panels.length).toBe(2);
    for (const panel of panels) {
      expect((panel as HTMLElement).hidden).toBe(false);
      expect(panel.querySelector('arena-tickets')).not.toBeNull();
    }
    // Nothing toggles any more — no button, no expanded state, no CTA on
    // a live date. The picker under it is the action.
    expect(container.querySelector('.asa-date-row button')).toBeNull();
    expect(container.querySelector('[aria-expanded]')).toBeNull();
    expect(container.querySelector('.asa-date-row:not(.asa-date-row--past) .asa-date-row__cta')).toBeNull();

    const panel = panels[0] as HTMLElement;
    expect(panel.id).toBe(`asa-panel-${eventWithPoster.id}`);

    const widget = panel.querySelector('arena-tickets');
    // The token now travels with the list, so the card mounts its picker
    // without resolving the event's own page first.
    expect(widget?.getAttribute('feed-token')).toBe('ft_abc123');
    expect(widget?.getAttribute('event-id')).toBe(eventWithPoster.id);
    expect(widget?.getAttribute('api-base')).toBe('https://api.example.com');
    // What the event is about is the one thing the card header does not
    // say, so it stays. The artwork and the date do not: both are already
    // on screen, above the card and in it.
    expect(panel.querySelector('.asa-date-panel__description')?.textContent).toBe(
      'An intensive one-day acting workshop.',
    );
    expect(widget?.getAttribute('cover')).toBe('hidden');
    expect(widget?.getAttribute('sessions')).toBe('hidden');
    // The card is already a box; without this the picker draws a second
    // one inside it and one date reads as two separate things.
    expect(widget?.getAttribute('frame')).toBe('hidden');
    expect(panel.querySelector('img')).toBeNull();
  });

  // Should not happen — the list only returns events resolved THROUGH a
  // token — but the field is optional in the schema, and a card that
  // silently offers nothing is worse than one that sends the buyer on.
  it('falls back to the event page when a date carries no feed token', () => {
    const container = freshContainer();
    renderPromoterPage(
      container,
      { ...sampleData, events: [{ ...eventWithPoster, feed_token: undefined }] },
      'en',
      pageOptions(),
    );
    const panel = container.querySelector('.asa-date-panel') as HTMLElement;
    expect(panel.querySelector('arena-tickets')).toBeNull();
    const link = panel.querySelector('a.asa-button') as HTMLAnchorElement | null;
    expect(link?.textContent).toBe('Tickets');
    expect(link?.getAttribute('href')).toContain('/masterclassteatro/masterclass-day-1');
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

  // Separate shows (each with its own artwork) are picked from a poster
  // catalog, and every show has its own page — nothing is bought here.
  describe('poster catalog', () => {
    const otherShow: HostedPageEvent = {
      ...eventWithPoster,
      id: '01929d0e-0e47-7000-8000-000000000403',
      slug: 'one-night-reading',
      title: 'One-night reading',
      poster_url: 'https://example.com/reading.jpg',
      first_session_at: '2026-11-17T18:30:00Z',
      last_session_at: '2026-11-17T20:30:00Z',
      first_session_timezone: 'Europe/Madrid',
    };
    const catalog: HostedPromoterPageResponse = {
      ...sampleData,
      org: { slug: 'actorre', name: 'Actorre', logo_url: null },
      events: [eventWithPoster, otherShow],
    };

    it('is chosen only when the events carry two or more different posters', () => {
      expect(isPosterCatalog(catalog.events)).toBe(true);
      // One shared artwork is a tour, and an event without artwork does not
      // turn it into a catalog.
      expect(isPosterCatalog(sampleData.events)).toBe(false);
      expect(isPosterCatalog([eventWithPoster, { ...otherShow, poster_url: eventWithPoster.poster_url }])).toBe(false);
      expect(isPosterCatalog([eventWithPoster])).toBe(false);
      expect(isPosterCatalog([])).toBe(false);
    });

    it('renders one poster card per event, each linking to its own page', () => {
      const container = freshContainer();
      renderPromoterPage(container, catalog, 'en', pageOptions());
      const cards = container.querySelectorAll('a.asa-card');
      expect(cards.length).toBe(2);
      expect(cards[0].getAttribute('href')).toBe('/actorre/masterclass-day-1');
      expect(cards[1].getAttribute('href')).toBe('/actorre/one-night-reading');
      expect((cards[1].querySelector('img') as HTMLImageElement).src).toBe('https://example.com/reading.jpg');
      expect(cards[1].querySelector('.asa-card__title')?.textContent).toBe('One-night reading');
      expect(cards[1].querySelector('.asa-card__venue')?.textContent).toBe('Studio A');
      expect(cards[1].querySelector('.asa-card__cta')?.textContent).toBe('Tickets');
    });

    it('mounts no ticket picker and promotes no single poster', () => {
      const container = freshContainer();
      renderPromoterPage(container, catalog, 'en', pageOptions());
      expect(container.querySelector('arena-tickets')).toBeNull();
      expect(container.querySelector('.asa-date-panel')).toBeNull();
      expect(container.querySelector('.asa-promoter-poster')).toBeNull();
    });

    it('shows the date and time in the venue time zone, and keeps the language query', () => {
      const container = freshContainer();
      window.history.replaceState(null, '', '/actorre?lang=ru');
      renderPromoterPage(container, catalog, 'en', pageOptions());
      const card = container.querySelectorAll('a.asa-card')[1];
      expect(card.querySelector('.asa-card__when')?.textContent).toBe('17 Nov · Tue 07:30 PM');
      expect(card.getAttribute('href')).toBe('/actorre/one-night-reading?lang=ru');
      window.history.replaceState(null, '', '/');
    });

    it('offers no tickets on a show that has already happened', () => {
      const container = freshContainer();
      renderPromoterPage(
        container,
        { ...catalog, events: [{ ...otherShow, first_session_at: '2020-01-01T10:00:00Z', last_session_at: '2020-01-01T12:00:00Z' }, eventWithPoster] },
        'en',
        pageOptions(),
      );
      const past = container.querySelector('.asa-card-item--past');
      expect(past?.querySelector('.asa-card__cta')?.textContent).toBe('Took place');
    });

    it('headings the list with the localized choose-an-event prompt', () => {
      const container = freshContainer();
      renderPromoterPage(container, catalog, 'ru', pageOptions());
      expect(container.querySelector('.asa-dates__head')?.textContent).toBe('Выберите событие');
    });
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
    // No picker under a date that is over — there is nothing to sell.
    expect(container.querySelector('.asa-date-panel')).toBeNull();
    expect(container.querySelector('arena-tickets')).toBeNull();
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

    // The token names a checkout, not an event. Left in the address it
    // made every row that opened afterwards show that one paid order, and
    // reloading could not clear it.
    it('takes the checkout token out of the address, once', () => {
      const originalHref = window.location.href;
      window.history.replaceState(null, '', '/masterclassteatro?checkout_token=ct_abc&lang=ru');

      expect(consumeCheckoutTokenFromURL(window.location, window.history)).toBe('ct_abc');
      expect(window.location.search).toBe('?lang=ru');
      // A second pass has nothing left to take.
      expect(consumeCheckoutTokenFromURL(window.location, window.history)).toBeNull();
      expect(window.location.search).toBe('?lang=ru');

      window.history.replaceState(null, '', originalHref);
    });

    it('leaves an address without a token exactly as it was', () => {
      const originalHref = window.location.href;
      window.history.replaceState(null, '', '/masterclassteatro?lang=cs');
      expect(consumeCheckoutTokenFromURL(window.location, window.history)).toBeNull();
      expect(window.location.search).toBe('?lang=cs');
      window.history.replaceState(null, '', originalHref);
    });

    // Every card is open already, so the only thing left to do is put
    // theirs on screen.
    it('scrolls their card into view so the buyer sees the outcome', () => {
      const container = freshContainer();
      const scrolled: Element[] = [];
      const original = Element.prototype.scrollIntoView;
      Element.prototype.scrollIntoView = function scrollIntoViewStub(this: Element) {
        scrolled.push(this);
      };
      const frames: FrameRequestCallback[] = [];
      const originalRaf = window.requestAnimationFrame;
      window.requestAnimationFrame = ((cb: FrameRequestCallback) => {
        frames.push(cb);
        return 0;
      }) as typeof window.requestAnimationFrame;

      try {
        const options = pageOptions();
        options.openEventID = eventWithoutPoster.id;
        renderPromoterPage(container, sampleData, 'en', options);
        for (const cb of frames) cb(0);
      } finally {
        Element.prototype.scrollIntoView = original;
        window.requestAnimationFrame = originalRaf;
      }

      expect(scrolled.length).toBe(1);
      expect(scrolled[0].querySelector('.asa-date-panel')?.id).toBe(
        `asa-panel-${eventWithoutPoster.id}`,
      );
    });

    it('scrolls nothing when the id names no date on the page', () => {
      const container = freshContainer();
      const scrolled: Element[] = [];
      const original = Element.prototype.scrollIntoView;
      Element.prototype.scrollIntoView = function scrollIntoViewStub(this: Element) {
        scrolled.push(this);
      };
      try {
        const options = pageOptions();
        options.openEventID = 'not-on-this-page';
        renderPromoterPage(container, sampleData, 'en', options);
      } finally {
        Element.prototype.scrollIntoView = original;
      }
      expect(scrolled).toEqual([]);
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
