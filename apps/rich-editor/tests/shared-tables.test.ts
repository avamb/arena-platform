// Two tables of the Go package are read here and by Go tests:
//   testdata/links_table.json         cleanHref (href.go), Go test href_table_test.go
//   testdata/scheme_words_table.json  StrictSchemeWords (sanitize.go), Go test scheme_table_test.go
// A row changes only with both sides in mind: the ports must agree on every one.

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { PROFILES, cleanHref, measure, profileByName, sanitize } from '../src/index'

const here = dirname(fileURLToPath(import.meta.url))
const testdata = resolve(here, '../../backend/internal/platform/richtext/testdata')
const readTable = <T>(name: string): T[] => JSON.parse(readFileSync(resolve(testdata, name), 'utf8')) as T[]

interface LinkRow {
  name: string
  profile: string
  href: string
  want: string | null
}

describe('links_table.json (Go: TestCleanHrefSharedTable)', () => {
  const rows = readTable<LinkRow>('links_table.json')

  it('is a real table', () => {
    expect(rows.length).toBeGreaterThanOrEqual(50)
    expect(new Set(rows.map((r) => r.name + '|' + r.profile)).size).toBe(rows.length)
  })

  for (const row of rows) {
    it(`${row.profile}: ${row.name}`, () => {
      const profile = profileByName(row.profile)
      expect(profile, row.profile).toBeDefined()
      const got = cleanHref(row.href, profile as NonNullable<typeof profile>)
      expect(got).toBe(row.want)
      // What is accepted is canonical: it comes back unchanged.
      if (got !== null) expect(cleanHref(got, profile as NonNullable<typeof profile>)).toBe(got)
    })
  }

  // Not in the table: a lone surrogate reaches Go as U+FFFD after JSON, so Go would accept a link
  // the editor must not keep (it could not show what is saved).
  it('refuses a lone surrogate in a link', () => {
    expect(cleanHref('https://example.com/\ud800', PROFILES.description)).toBeNull()
    expect(cleanHref('https://example.com/\udc00x', PROFILES.description)).toBeNull()
    expect(cleanHref('https://example.com/\u{1f600}', PROFILES.description)).toBe('https://example.com/\u{1f600}')
  })
})

interface SchemeRow {
  name: string
  profile: string
  html: string
  reason: string
}

describe('scheme_words_table.json (Go: TestSchemeWordSharedTable)', () => {
  const rows = readTable<SchemeRow>('scheme_words_table.json')

  it('is a real table', () => {
    expect(rows.length).toBeGreaterThanOrEqual(30)
    expect(rows.some((r) => r.reason === 'scheme_not_allowed')).toBe(true)
    expect(rows.some((r) => r.reason === '')).toBe(true)
  })

  for (const row of rows) {
    it(`${row.profile}: ${row.name}`, () => {
      const profile = profileByName(row.profile)
      expect(profile, row.profile).toBeDefined()
      const stats = measure(row.html, profile as NonNullable<typeof profile>)
      expect(stats.schemeWord).toBe(row.reason === 'scheme_not_allowed')
      // The text itself is never changed by the check.
      expect(sanitize(row.html, profile as NonNullable<typeof profile>)).toBe(stats.html)
    })
  }
})
