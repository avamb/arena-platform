// Checks of the built files (run `npm run build` first; without dist the
// checks are skipped, CI builds before it tests): the stylesheet has no
// remote import or font, the scripts have no eval and no new Function, and the
// IIFE build exposes the global ArenaRichEditor.

import { existsSync, readFileSync } from 'node:fs'
import { gzipSync } from 'node:zlib'
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

  // Size budgets (gzip, bytes) with some room over the measured sizes: ESM 149.4 kB (the library build
  // is not minified, the host's build does that), IIFE 114.9 kB. A jump means an extension or a library
  // slipped back in (StarterKit, linkify, drop cursor...): look at what was imported before raising it.
  const BUDGET: Record<string, number> = { 'rich-editor.js': 160_000, 'arena-rich-editor.iife.js': 125_000 }
  for (const file of Object.keys(BUDGET)) {
    it(`${file} stays within its gzip budget`, () => {
      const size = gzipSync(readFileSync(resolve(dist, file)), { level: 9 }).length
      expect(size).toBeLessThan(BUDGET[file])
      expect(size).toBeGreaterThan(50_000) // and it is the whole library, not a stub
    })
    it(`${file} holds no extension or library that the white list does not use`, () => {
      const js = read(file)
      expect(js).not.toMatch(/linkify/i)
      expect(js).not.toMatch(/tlds/)
      for (const name of ['code', 'codeBlock', 'horizontalRule', 'strike', 'dropCursor', 'gapCursor', 'trailingNode', 'taskList', 'taskItem', 'listKeymap', 'characterCount', 'placeholder']) {
        expect(js, name).not.toMatch(new RegExp(`name:[ ]*["']${name}["']`))
      }
      // The extensions the editor does use are in.
      for (const name of ['doc', 'paragraph', 'text', 'bold', 'italic', 'underline', 'hardBreak', 'undoRedo', 'bulletList', 'orderedList', 'listItem', 'link']) {
        expect(js, name).toMatch(new RegExp(`name:[ ]*["']${name}["']`))
      }
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
