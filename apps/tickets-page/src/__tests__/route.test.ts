import { describe, expect, it } from 'vitest';
import { parsePath } from '../lib/route.ts';

describe('parsePath', () => {
  it('parses a plain /{org}/{event} path', () => {
    expect(parsePath('/arenasoldout/summer-festival')).toEqual({
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('tolerates a trailing slash', () => {
    expect(parsePath('/arenasoldout/summer-festival/')).toEqual({
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('tolerates a leading double slash', () => {
    expect(parsePath('//arenasoldout/summer-festival')).toEqual({
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('decodes percent-encoded segments', () => {
    expect(parsePath('/arena%20org/summer%20fest')).toEqual({
      orgSlug: 'arena org',
      eventSlug: 'summer fest',
    });
  });

  it('returns null for the root path', () => {
    expect(parsePath('/')).toBeNull();
  });

  it('returns null for an empty path', () => {
    expect(parsePath('')).toBeNull();
  });

  it('returns null for a single-segment path', () => {
    expect(parsePath('/arenasoldout')).toBeNull();
  });

  it('returns null for a path with more than two segments', () => {
    expect(parsePath('/arenasoldout/summer-festival/extra')).toBeNull();
  });

  it('returns null when a segment decodes to empty', () => {
    expect(parsePath('/arenasoldout//')).toBeNull();
  });
});
