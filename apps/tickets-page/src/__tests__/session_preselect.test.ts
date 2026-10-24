import { afterEach, describe, expect, it } from 'vitest';
import { preselectedSession, renderEvent } from '../lib/render.ts';
import type { HostedPageResponse } from '../lib/api.ts';

const SESSION = '01929d0e-0e47-7000-8000-0000000003a1';

const page: HostedPageResponse = {
  org: { slug: 'arenasoldout', name: 'Arena Sold Out', logo_url: null },
  event: {
    id: '01929d0e-0e47-7000-8000-000000000301',
    slug: 'summer-festival',
    title: 'Summer Festival 2026',
    description: 'Open-air festival.',
    short_description: 'One night, three stages.',
    image_url: null,
    poster_url: null,
    age_rating: '16+',
    venue_names: ['Forum Karlin'],
    first_session_at: '2026-08-15T18:00:00Z',
    last_session_at: '2026-08-15T22:00:00Z',
    first_session_timezone: 'Europe/Prague',
  },
  feed_token: 'ft_abc123',
  default_locale: 'en',
};

describe('preselectedSession', () => {
  it('reads a well-formed uuid', () => {
    expect(preselectedSession(`?session=${SESSION}`)).toBe(SESSION);
    expect(preselectedSession(`?lang=ru&session=${SESSION}&ref=x7k2qpm`)).toBe(SESSION);
  });

  it('folds the case of the uuid', () => {
    expect(preselectedSession(`?session=${SESSION.toUpperCase()}`)).toBe(SESSION);
  });

  it('is null for anything that is not a uuid', () => {
    expect(preselectedSession('')).toBeNull();
    expect(preselectedSession('?lang=ru')).toBeNull();
    expect(preselectedSession('?session=')).toBeNull();
    expect(preselectedSession('?session=123')).toBeNull();
    expect(preselectedSession(`?session=${SESSION}x`)).toBeNull();
    expect(preselectedSession('?session=<script>')).toBeNull();
  });
});

describe('renderEvent session preselect', () => {
  const original = window.location.href;

  afterEach(() => {
    window.history.replaceState(null, '', original);
    document.body.innerHTML = '';
  });

  function mount(): HTMLElement {
    const container = document.createElement('main');
    document.body.appendChild(container);
    renderEvent(container, page, 'en', { apiBase: '', resumingCheckout: false });
    return container;
  }

  it('hands ?session=<uuid> to the widget and keeps the address as it was', () => {
    window.history.replaceState(null, '', `/arenasoldout/summer-festival?ref=x7k2qpm&session=${SESSION}`);
    const widget = mount().querySelector('arena-tickets');
    expect(widget?.getAttribute('session-id')).toBe(SESSION);
    expect(widget?.getAttribute('event-id')).toBe(page.event.id);
    expect(widget?.getAttribute('sessions')).toBeNull(); // the date chips stay
    expect(window.location.search).toBe(`?ref=x7k2qpm&session=${SESSION}`);
  });

  it('sets no session-id without the parameter or with a bad value', () => {
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?ref=x7k2qpm');
    expect(mount().querySelector('arena-tickets')?.hasAttribute('session-id')).toBe(false);
    window.history.replaceState(null, '', '/arenasoldout/summer-festival?session=not-a-uuid');
    expect(mount().querySelector('arena-tickets')?.hasAttribute('session-id')).toBe(false);
  });
});
