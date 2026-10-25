// The profiles table mirrors the Go package. This test READS richtext.go, so
// a limit, a tag or a scheme changed there fails the editor build until the
// table here follows.

import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { PROFILES, PROFILE_NAMES, profileByName } from '../src/index'
import { MAX_DEPTH, MAX_INPUT_BYTES, MAX_LINK_LENGTH } from '../src/profiles'

const here = dirname(fileURLToPath(import.meta.url))
const goSource = readFileSync(resolve(here, '../../backend/internal/platform/richtext/richtext.go'), 'utf8')

const goConst = (name: string): string => {
  const m = new RegExp(`${name}\\s*=\\s*([^\\n]+)`).exec(goSource)
  expect(m, `Go constant ${name}`).not.toBeNull()
  return (m as RegExpExecArray)[1].trim()
}

const goList = (body: string, field: string): string[] => {
  const m = new RegExp(`${field}:\\s*\\[\\]string\\{([^}]*)\\}`).exec(body)
  expect(m, `Go field ${field}`).not.toBeNull()
  return [...(m as RegExpExecArray)[1].matchAll(/"([^"]+)"/g)].map((x) => x[1])
}

const goInt = (body: string, field: string): number => {
  const m = new RegExp(`${field}:\\s*(\\d+)`).exec(body)
  expect(m, `Go field ${field}`).not.toBeNull()
  return Number((m as RegExpExecArray)[1])
}

describe('profiles: the Go table, field by field', () => {
  const goName: Record<string, string> = {
    description: 'ProfileDescription',
    email_note: 'ProfileEmailNote',
    session_message: 'ProfileSessionMessage',
  }
  for (const name of PROFILE_NAMES) {
    it(name, () => {
      const re = new RegExp(`${goName[name]}\\s*=\\s*Profile\\{([\\s\\S]*?)\\n\\t\\}`)
      const block = re.exec(goSource)
      expect(block, `Go profile ${goName[name]}`).not.toBeNull()
      const body = (block as RegExpExecArray)[1]
      const p = PROFILES[name]
      expect([...p.tags]).toEqual(goList(body, 'Tags'))
      expect([...p.schemes]).toEqual(goList(body, 'Schemes'))
      expect(p.maxVisible).toBe(goInt(body, 'MaxVisible'))
      expect(p.maxHtml).toBe(goInt(body, 'MaxHTML'))
      expect(p.maxLinks).toBe(goInt(body, 'MaxLinks'))
    })
  }

  it('has the three profiles of the spec and no others', () => {
    expect([...PROFILE_NAMES].sort()).toEqual(['description', 'email_note', 'session_message'])
    expect(profileByName('nope')).toBeUndefined()
    expect(profileByName('constructor')).toBeUndefined()
  })

  it('matches the table of the task', () => {
    const letter = ['p', 'br', 'strong', 'em', 'u', 'a', 'ul', 'ol', 'li']
    expect([...PROFILES.description.tags]).toEqual([...letter, 'h3', 'h4', 'blockquote'])
    expect([...PROFILES.email_note.tags]).toEqual(letter)
    expect([...PROFILES.session_message.tags]).toEqual(letter)
    expect(PROFILES.description).toMatchObject({ schemes: ['https', 'mailto'], maxVisible: 4000, maxLinks: 20 })
    expect(PROFILES.email_note).toMatchObject({ schemes: ['https'], maxVisible: 2000, maxLinks: 5 })
    expect(PROFILES.session_message).toMatchObject({ schemes: ['https'], maxVisible: 1000, maxLinks: 3 })
  })

  it('headings and the quote exist only in the description profile (owner decision 9.1)', () => {
    for (const tag of ['h3', 'h4', 'blockquote']) {
      expect(PROFILES.description.tags).toContain(tag)
      expect(PROFILES.email_note.tags).not.toContain(tag)
      expect(PROFILES.session_message.tags).not.toContain(tag)
    }
  })

  it('shares the global limits with Go', () => {
    expect(String(MAX_LINK_LENGTH)).toBe(goConst('MaxLinkLength'))
    expect(`1 << 20`).toBe(goConst('maxInputBytes'))
    expect(MAX_INPUT_BYTES).toBe(1 << 20)
    expect(String(MAX_DEPTH)).toBe(goConst('maxDepth'))
  })

  it('is frozen', () => {
    expect(Object.isFrozen(PROFILES)).toBe(true)
    expect(Object.isFrozen(PROFILES.description)).toBe(true)
    expect(Object.isFrozen(PROFILES.description.tags)).toBe(true)
  })
})
