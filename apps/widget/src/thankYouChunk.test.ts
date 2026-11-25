/** The thank-you blocks stay out of the main bundle: only a dynamic import reaches them. */

import { describe, expect, it } from 'vitest';
import orderStatusSource from './components/OrderStatus.svelte?raw';
import viteConfigSource from '../vite.config.ts?raw';

describe('thank-you chunk', () => {
  it('OrderStatus loads ThankYouBlocks by dynamic import only', () => {
    expect(orderStatusSource).toContain("import('./thankyou/ThankYouBlocks.svelte')");
    expect(orderStatusSource).not.toMatch(/^\s*import\s+ThankYouBlocks\s+from/m);
    expect(orderStatusSource).not.toMatch(/from\s+'\.\.\/lib\/thankYou(I18n)?\.js'/);
  });

  it('the build names chunks with a stable prefix and a hash', () => {
    expect(viteConfigSource).toContain("chunkFileNames: 'arena-tickets-[name]-[hash].js'");
  });
});
