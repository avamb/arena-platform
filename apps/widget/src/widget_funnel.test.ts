/**
 * Guards the wiring of the buyer funnel beacon (spec 50, stage A7) in the
 * widget: which moments send which step, that the host can name the surface,
 * and that the beacon stays anonymous (no cookie, no storage).
 *
 * Source-level assertions (`?raw`), the idiom this suite already uses for
 * component markup — see widget_hosted_card.test.ts. The behavior of the
 * sender itself is in lib/funnel.test.ts.
 */

import { describe, it, expect } from 'vitest';
import rawWidgetSource from './ArenaTickets.svelte?raw';
import funnelSource from './lib/funnel.ts?raw';

// The component file is CRLF on disk; the assertions below slice by line.
const widgetSource = rawWidgetSource.split('\r\n').join('\n');

describe('widget funnel wiring', () => {
  it('declares the funnel attribute so the host page can name the surface', () => {
    expect(widgetSource).toContain("funnel: { type: 'String', attribute: 'funnel' }");
    const [, defaults = ''] = widgetSource.match(/const \{([^}]*)\}: Props = \$props\(\)/) ?? [];
    expect(defaults).toContain("funnel = ''");
  });

  it('sends the view when a feed event is shown, and the session when it is on screen at once', () => {
    const apply = widgetSource.slice(widgetSource.indexOf('function applyFeedEvent'));
    const body = apply.slice(0, apply.indexOf('\n  }\n'));
    expect(body).toContain("sendFunnel('view')");
    // A single-session event or a deep-linked date shows its seat map at once.
    expect(body).toMatch(/sessions\.length === 1 \|\| selectedSession\.id === normSessionId/);
    expect(body).toContain("sendFunnel('session_open', selectedSession.id)");
  });

  it('does not send anything for the synthetic single-session event', () => {
    const synth = widgetSource.slice(widgetSource.indexOf('function applySyntheticSessionEvent'));
    const body = synth.slice(0, synth.indexOf('\n  }\n'));
    expect(body).not.toContain('sendFunnel');
  });

  it('sends session_open when the buyer picks another session', () => {
    expect(widgetSource).toMatch(/selectedSession = s;\s*\/\/[^\n]*\s*if \(s\) sendFunnel\('session_open', s\.id\);/);
  });

  it('sends seats_chosen when the cart first holds something in a session', () => {
    expect(widgetSource).toMatch(/if \(cartCount > 0 && sid\) untrack\(\(\) => sendFunnel\('seats_chosen', sid\)\)/);
  });

  it('is built lazily from the props and reads the utm from the stored touch', () => {
    expect(widgetSource).toContain('funnelBeacon ??= createFunnel({');
    expect(widgetSource).toContain('touch: () => attribution');
    expect(widgetSource).toContain('surface: parseFunnelSurface(funnel)');
  });
});

describe('the beacon stays anonymous', () => {
  it('never touches cookies or storage', () => {
    for (const forbidden of ['document.cookie', 'localStorage', 'sessionStorage', 'indexedDB']) {
      expect(funnelSource, forbidden).not.toContain(forbidden);
    }
  });

  it('is sent without credentials', () => {
    expect(funnelSource).toContain("credentials: 'omit'");
  });
});
