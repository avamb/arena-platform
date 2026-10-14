import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The page's own start-up (main.ts) with the API answering 404 for the event:
// a letter's "Write to the organizer" link of an event that is gone must land
// on the organization's page, where the same form opens. This drives the real
// wiring in main.ts, not only the helper that builds the address.

vi.mock('../lib/api.ts', async (importOriginal) => {
  const real = await importOriginal<typeof import('../lib/api.ts')>();
  return {
    ...real,
    fetchHostedPage: vi.fn(async () => {
      throw new real.ApiError(404, 'not found');
    }),
  };
});

interface FakeLocation {
  pathname: string;
  hash: string;
  search: string;
  href: string;
  replace: ReturnType<typeof vi.fn>;
}

function fakeLocation(path: string, search: string, hash: string): FakeLocation {
  return {
    pathname: path,
    hash,
    search,
    href: 'https://tickets.example' + path + search + hash,
    replace: vi.fn(),
  };
}

async function startPage(loc: FakeLocation): Promise<void> {
  vi.stubGlobal('location', loc);
  await import('../main.ts');
}

describe('event page answering 404', () => {
  beforeEach(() => {
    vi.resetModules();
    document.head.innerHTML = '';
    document.body.innerHTML = '<main id="asa-main"></main>';
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sends a #contact link to the organization page with the same query', async () => {
    const loc = fakeLocation('/teatro-x/gone-event', '?lang=ru', '#contact');
    await startPage(loc);
    await vi.waitFor(() => expect(loc.replace).toHaveBeenCalledWith('/teatro-x?lang=ru#contact'));
    expect(document.querySelector('.asa-state--not-found')).toBeNull();
  });

  it('shows "not found" for an ordinary link and goes nowhere', async () => {
    const loc = fakeLocation('/teatro-x/gone-event', '', '');
    await startPage(loc);
    await vi.waitFor(() => expect(document.querySelector('.asa-state--not-found')).not.toBeNull());
    expect(loc.replace).not.toHaveBeenCalled();
  });
});
