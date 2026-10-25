// The shared corpus of the Go package (richtext/testdata/richtext_legacy.json)
// is read by the Go, tickets-page and admin-web tests. Here every `art` value
// goes through setContent and getHTML of the real editor with the matching
// profile and must come back identical: the editor's output is the canonical
// ART the Go sanitizer accepts unchanged.

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterEach, describe, expect, it } from 'vitest'

import { create, profileByName, sanitize, type RichEditor } from '../src/index'

const here = dirname(fileURLToPath(import.meta.url))
const corpusPath = resolve(here, '../../backend/internal/platform/richtext/testdata/richtext_legacy.json')

interface Case {
  name: string
  profile: string
  legacy: string
  art: string
}

const cases = JSON.parse(readFileSync(corpusPath, 'utf8')) as Case[]

/**
 * Justified exceptions: corpus values the editor cannot return identically.
 * Empty on purpose; add a case here only with the reason. (The empty text is
 * not an exception: "" goes in and "" comes out.)
 */
const EXCEPTIONS: Record<string, string> = {}

const editors: RichEditor[] = []
afterEach(() => {
  while (editors.length) editors.pop()?.destroy()
  document.body.innerHTML = ''
})

describe('Go corpus richtext_legacy.json', () => {
  it('is found and not trivial', () => {
    expect(cases.length).toBeGreaterThan(60)
    expect(new Set(cases.map((c) => c.profile))).toEqual(new Set(['description', 'email_note', 'session_message']))
  })

  for (const c of cases) {
    const reason = EXCEPTIONS[c.name]
    const run = reason ? it.skip : it
    run(`${c.profile}: ${c.name}`, () => {
      const profile = profileByName(c.profile)
      expect(profile).toBeDefined()
      const host = document.createElement('div')
      document.body.appendChild(host)
      // setContent: the value goes in at creation, and again through setHTML.
      const editor = create(host, { profile: c.profile as 'description', value: c.art })
      editors.push(editor)
      expect(editor.getHTML()).toBe(c.art)
      editor.setHTML('<p>other</p>')
      editor.setHTML(c.art)
      expect(editor.getHTML()).toBe(c.art)
      // The pure sanitizer is idempotent on ART as well.
      expect(sanitize(c.art, profile!)).toBe(c.art)
    })
  }

  it('documents its exceptions', () => {
    for (const [name, why] of Object.entries(EXCEPTIONS)) {
      expect(cases.some((c) => c.name === name), name).toBe(true)
      expect(why.length).toBeGreaterThan(10)
    }
  })
})
