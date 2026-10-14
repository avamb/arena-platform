import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';

function normalizeRelative(value) {
  return String(value).trim().replaceAll('\\', '/').replace(/^\.\//, '');
}

function lineCount(file) {
  const text = readFileSync(file, 'utf8');
  if (text.length === 0) return 0;
  return text.replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n').at(-1) === ''
    ? text.replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n').length - 1
    : text.replace(/\r\n/g, '\n').replace(/\r/g, '\n').split('\n').length;
}

/**
 * Validate an evidence citation against exact approved relative paths.
 * An optional em-dash annotation is allowed after the line reference so that
 * a citation can retain the model's short explanation without changing the
 * approved path component.
 */
export function validateEvidenceReference(reference, approvedPaths, { root = process.cwd(), checkLines = true } = {}) {
  if (typeof reference !== 'string' || !Array.isArray(approvedPaths)) {
    return { valid: false, reason: 'invalid_input' };
  }
  const value = normalizeRelative(reference);
  const candidates = approvedPaths.map(normalizeRelative).sort((a, b) => b.length - a.length);
  const approved = candidates.find(candidate => value === candidate || value.startsWith(`${candidate}:`));
  if (!approved) return { valid: false, reason: 'path_not_approved' };

  const suffix = value.slice(approved.length);
  let startLine = null;
  let endLine = null;
  let annotation = '';
  if (suffix) {
    const match = /^:([1-9]\d*)(?:-([1-9]\d*))?(?:\s+—\s+(.+))?$/.exec(suffix);
    if (!match) return { valid: false, reason: 'invalid_line_reference', path: approved };
    startLine = Number(match[1]);
    endLine = match[2] ? Number(match[2]) : startLine;
    annotation = match[3] ?? '';
    if (endLine < startLine) return { valid: false, reason: 'reverse_line_range', path: approved };
  }

  const absolute = path.resolve(root, approved);
  const relative = path.relative(root, absolute);
  if (relative.startsWith('..') || path.isAbsolute(relative) || !existsSync(absolute)) {
    return { valid: false, reason: 'path_unavailable', path: approved };
  }
  if (checkLines && startLine !== null && endLine > lineCount(absolute)) {
    return { valid: false, reason: 'line_out_of_range', path: approved, startLine, endLine };
  }
  return { valid: true, path: approved, startLine, endLine, annotation };
}

export function validateEvidenceList(evidence, approvedPaths, options = {}) {
  if (!Array.isArray(evidence)) return { valid: false, references: [] };
  const references = evidence.map(reference => ({ reference, ...validateEvidenceReference(reference, approvedPaths, options) }));
  return { valid: references.every(item => item.valid), references };
}
