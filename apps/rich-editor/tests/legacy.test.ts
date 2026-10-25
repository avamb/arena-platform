// FromLegacy port: the `legacy` and `art` fields of the shared Go corpus (richtext_legacy.json) must agree
// with fromLegacy(legacy, profile), case by case. The same file drives the Go test of legacy.go.

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { artToPlainText, fromLegacy, profileByName, sanitize } from '../src/text'

const here = dirname(fileURLToPath(import.meta.url))
const corpus = JSON.parse(
  readFileSync(resolve(here, '../../backend/internal/platform/richtext/testdata/richtext_legacy.json'), 'utf8'),
) as Array<{ name: string; profile: string; legacy: string; art: string }>

describe('fromLegacy against the Go corpus', () => {
  for (const c of corpus) {
    it(`${c.profile}: ${c.name}`, () => {
      const p = profileByName(c.profile)!
      expect(fromLegacy(c.legacy, p)).toBe(c.art)
      // Canonical: the sanitizer leaves the result alone.
      expect(sanitize(fromLegacy(c.legacy, p), p)).toBe(c.art)
    })
  }
})

describe('fromLegacy by hand', () => {
  const desc = profileByName('description')!
  const note = profileByName('email_note')!
  it('keeps a typed tag as text', () => {
    expect(fromLegacy('a <b> & c', desc)).toBe('<p>a &lt;b&gt; &amp; c</p>')
  })
  it('links bare https addresses only in the letter note', () => {
    expect(fromLegacy('See https://example.com/x.', note)).toBe('<p>See <a href="https://example.com/x">https://example.com/x</a>.</p>')
    expect(fromLegacy('See https://example.com/x.', desc)).toBe('<p>See https://example.com/x.</p>')
  })
  it('round-trips through the plain text of ART', () => {
    const art = fromLegacy('One\n\n- a\n- b', desc)
    expect(artToPlainText(art, desc)).toBe('One\n\n- a\n- b')
  })
  it('turns a blank or empty input into an empty text', () => {
    expect(fromLegacy('', desc)).toBe('')
    expect(fromLegacy(' \n\t\n ', desc)).toBe('')
  })
})
