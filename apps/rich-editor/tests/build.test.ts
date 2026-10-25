// Checks of the built files (run `npm run build` first; without dist the
// checks are skipped, CI builds before it tests): the stylesheet has no
// remote import or font, the scripts have no eval and no new Function, and the
// IIFE build exposes the global ArenaRichEditor.

import { existsSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { JSDOM } from 'jsdom'
import { describe, expect, it } from 'vitest'

const dist = resolve(dirname(fileURLToPath(import.meta.url)), '../dist')
const built = existsSync(resolve(dist, 'rich-editor.js')) && existsSync(resolve(dist, 'arena-rich-editor.iife.js'))
const read = (name: string) => readFileSync(resolve(dist, name), 'utf8')

describe.skipIf(!built)('the built library', () => {
  it('ships its own stylesheet with no @import and no remote resource', () => {
    const css = read('rich-editor.css')
    expect(css.length).toBeGreaterThan(1000)
    expect(css).not.toMatch(/@import/i)
    expect(css).not.toMatch(/url\(\s*['"]?(https?:)?\/\//i)
    expect(css).not.toMatch(/@font-face/i)
    expect(css).toContain('.arena-re')
    expect(css).toContain('.ProseMirror')
  })

  for (const file of ['rich-editor.js', 'arena-rich-editor.iife.js']) {
    it(`${file} has no eval and no new Function`, () => {
      const js = read(file)
      expect(js).not.toMatch(/(^|[^\w$.])eval\s*\(/)
      expect(js).not.toMatch(/\bnew\s+Function\b/)
      expect(js).not.toMatch(/\bFunction\s*\(\s*['"`]/)
      expect(js).not.toMatch(/setTimeout\s*\(\s*['"`]/)
    })
    it(`${file} sets no style attribute`, () => {
      const js = read(file)
      expect(js).not.toMatch(/setAttribute\(\s*["']style["']/)
    })
  }

  it('the IIFE build defines the global ArenaRichEditor and the editor works', () => {
    const dom = new JSDOM('<!doctype html><body><div id="host"></div></body>', { runScripts: 'outside-only' })
    dom.window.eval(read('arena-rich-editor.iife.js'))
    const lib = (dom.window as unknown as { ArenaRichEditor: Record<string, unknown> }).ArenaRichEditor
    expect(typeof lib.create).toBe('function')
    expect(Object.keys(lib.PROFILES as object).sort()).toEqual(['description', 'email_note', 'session_message'])
    const host = dom.window.document.getElementById('host') as HTMLElement
    const editor = (lib.create as (el: HTMLElement, o: object) => { getHTML: () => string; destroy: () => void })(host, {
      profile: 'email_note',
      locale: 'he',
      value: '<div>a <b>b</b></div>',
    })
    expect(editor.getHTML()).toBe('<p>a <strong>b</strong></p>')
    expect(host.querySelector('.arena-re')?.getAttribute('dir')).toBe('rtl')
    editor.destroy()
  })
})
