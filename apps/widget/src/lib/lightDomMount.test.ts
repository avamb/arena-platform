// @vitest-environment jsdom
import { describe, it, expect } from 'vitest';
import { mountInLightDom } from './lightDomMount.js';

describe('mountInLightDom', () => {
  it('returns light-DOM nodes appended to the host when inside a shadow root', () => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const shadow = host.attachShadow({ mode: 'open' });
    const a = document.createElement('div');
    const b = document.createElement('div');
    shadow.append(a, b);

    const m = mountInLightDom(a, b, 'sx', 'sp');
    expect(m.express).not.toBe(a);
    expect(m.payment).not.toBe(b);
    expect(m.express?.parentNode).toBe(host);
    expect(m.payment?.parentNode).toBe(host);
    expect(m.express?.getAttribute('slot')).toBe('sx');
    expect(m.payment?.getAttribute('slot')).toBe('sp');
    expect(m.express?.style.width).toBe('100%');

    m.cleanup();
    expect(host.children.length).toBe(0);
    host.remove();
  });

  it('returns the placeholders themselves without a shadow root', () => {
    const a = document.createElement('div');
    const b = document.createElement('div');
    document.body.append(a, b);
    const m = mountInLightDom(a, b, 'sx', 'sp');
    expect(m.express).toBe(a);
    expect(m.payment).toBe(b);
    m.cleanup();
    expect(document.body.contains(a)).toBe(true);
    a.remove();
    b.remove();
  });
});
