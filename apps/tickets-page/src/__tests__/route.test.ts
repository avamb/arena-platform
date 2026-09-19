import { describe, expect, it } from 'vitest';
import { parsePath } from '../lib/route.ts';

describe('parsePath — event route (two segments)', () => {
  it('parses a plain /{org}/{event} path', () => {
    expect(parsePath('/arenasoldout/summer-festival')).toEqual({
      kind: 'event',
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('tolerates a trailing slash', () => {
    expect(parsePath('/arenasoldout/summer-festival/')).toEqual({
      kind: 'event',
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('tolerates a leading double slash', () => {
    expect(parsePath('//arenasoldout/summer-festival')).toEqual({
      kind: 'event',
      orgSlug: 'arenasoldout',
      eventSlug: 'summer-festival',
    });
  });

  it('decodes percent-encoded segments', () => {
    expect(parsePath('/arena%20org/summer%20fest')).toEqual({
      kind: 'event',
      orgSlug: 'arena org',
      eventSlug: 'summer fest',
    });
  });

  it('preserves mixed case verbatim (the API resolves case-insensitively, not the page)', () => {
    expect(parsePath('/MasterClassTeatro/Summer-Fest')).toEqual({
      kind: 'event',
      orgSlug: 'MasterClassTeatro',
      eventSlug: 'Summer-Fest',
    });
  });

  it('returns null for a path with more than two segments', () => {
    expect(parsePath('/arenasoldout/summer-festival/extra')).toBeNull();
  });
});

describe('parsePath — promoter route (single segment)', () => {
  it('parses a plain /{org} path as a promoter route', () => {
    expect(parsePath('/arenasoldout')).toEqual({ kind: 'promoter', orgSlug: 'arenasoldout' });
  });

  it('tolerates a trailing slash', () => {
    expect(parsePath('/arenasoldout/')).toEqual({ kind: 'promoter', orgSlug: 'arenasoldout' });
  });

  it('tolerates a leading double slash', () => {
    expect(parsePath('//arenasoldout')).toEqual({ kind: 'promoter', orgSlug: 'arenasoldout' });
  });

  it('decodes a percent-encoded segment', () => {
    expect(parsePath('/master%20class')).toEqual({ kind: 'promoter', orgSlug: 'master class' });
  });

  it('preserves mixed case verbatim', () => {
    expect(parsePath('/MasterClassTeatro')).toEqual({ kind: 'promoter', orgSlug: 'MasterClassTeatro' });
  });

  it('collapses a doubled trailing slash to the same route as a single one', () => {
    // "/arenasoldout//" trims all trailing slashes down to one segment,
    // same as "/arenasoldout/" — a promoter route, not an event route with
    // an empty event_slug.
    expect(parsePath('/arenasoldout//')).toEqual({ kind: 'promoter', orgSlug: 'arenasoldout' });
  });
});

describe('parsePath — no route', () => {
  it('returns null for the root path', () => {
    expect(parsePath('/')).toBeNull();
  });

  it('returns null for an empty path', () => {
    expect(parsePath('')).toBeNull();
  });

  it('returns null when a segment fails to percent-decode', () => {
    expect(parsePath('/%E0%A4%A')).toBeNull();
  });
});
