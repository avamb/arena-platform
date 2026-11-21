// jsdom has no layout. ProseMirror asks for client rects when it scrolls the
// selection into view; answer with empty ones.
const emptyRect = { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) }

if (typeof Range !== 'undefined') {
  Range.prototype.getBoundingClientRect = () => emptyRect as DOMRect
  Range.prototype.getClientRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: [][Symbol.iterator] }) as unknown as DOMRectList
}
Element.prototype.getClientRects = () => ({ length: 0, item: () => null, [Symbol.iterator]: [][Symbol.iterator] }) as unknown as DOMRectList
document.elementFromPoint = () => null

// Vitest 4 keeps Node's own URL in the jsdom environment. The shared link tables (Go parity,
// strict UTS 46 bidi rule) were written against jsdom's whatwg-url, so keep using that one.
import { JSDOM } from 'jsdom'
globalThis.URL = new JSDOM('').window.URL as unknown as typeof URL
