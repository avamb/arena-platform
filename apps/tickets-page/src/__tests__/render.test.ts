import { describe, expect, it, beforeEach } from 'vitest';
import { renderError, renderEvent, renderLoading, renderNotFound } from '../lib/render.ts';
import type { HostedPageResponse } from '../lib/api.ts';

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

  it('renders the hero title, description and poster image', () => {
    const container = freshContainer();
    renderEvent(container, sampleData, 'en', { apiBase: '', resumingCheckout: false });

    expect(container.querySelector('.asa-hero-title')?.textContent).toBe('Summer Festival 2026');
    expect(container.querySelector('.asa-hero-description')?.textContent).toBe('One night, three stages.');
    const img = container.querySelector('.asa-hero-image') as HTMLImageElement | null;
    expect(img?.src).toBe('https://example.com/poster.jpg');
  });
});
