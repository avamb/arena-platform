/**
 * The cart sheet must compile without the Svelte warning that a prop's initial
 * value is captured by `$state(...)` (state_referenced_locally): such a prop is
 * read once, so a later change by the host is silently ignored.
 */

import { describe, expect, it } from 'vitest';
import { compile } from 'svelte/compiler';
import { transformWithEsbuild } from 'vite';
import cartSheetSource from './components/CartSheet.svelte?raw';

async function warningsOf(source: string): Promise<string[]> {
  const src = source.replace(/\r\n/g, '\n');
  const match = /<script lang="ts">([\s\S]*?)<\/script>/.exec(src);
  expect(match).not.toBeNull();
  const js = await transformWithEsbuild(match![1]!, 'CartSheet.ts', { loader: 'ts', target: 'esnext' });
  const plain = src.replace(match![0], `<script>${js.code}</script>`);
  const result = compile(plain, { generate: 'client', filename: 'CartSheet.svelte' });
  return result.warnings.map((w) => w.code);
}

describe('CartSheet compiles cleanly', () => {
  it('captures no prop in its initial state', async () => {
    const codes = await warningsOf(cartSheetSource);
    expect(codes).not.toContain('state_referenced_locally');
  });

  it('is checked by the same compile: the warning is reported for a captured prop', async () => {
    const bad = cartSheetSource.replace('let promoOpened = $state(false);', 'let promoOpened = $state(promoExpanded);');
    expect(bad).not.toBe(cartSheetSource);
    expect(await warningsOf(bad)).toContain('state_referenced_locally');
  });
});
