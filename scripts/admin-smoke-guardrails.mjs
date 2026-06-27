#!/usr/bin/env node
/**
 * admin-smoke-guardrails.mjs -- SAUI-14 production-code smoke guardrails.
 *
 * Scans apps/admin-web/src for forbidden mock/fake-data patterns. Real
 * persistence MUST come from backend APIs; the admin shell must never
 * ship with module-level in-memory stores, globalThis-backed devStores,
 * or "TODO: real data" placeholders. This script is the cheap, fast
 * gate that runs before the vitest suite and exits non-zero on any
 * hit so CI fails loudly.
 *
 * What is forbidden in production code (anywhere under
 *   apps/admin-web/src/**, excluding test/spec/fixture files):
 *
 *     - globalThis        : devStore-style module-level state escape
 *     - devStore          : explicit dev-only persistence shim
 *     - dev-store         : kebab-cased variant of the same
 *     - mockDb            : in-memory mock database
 *     - mockData          : seeded fake rows
 *     - fakeData          : same family
 *     - sampleData        : same family
 *     - dummyData         : same family
 *     - TODO.*real        : "TODO: hook to real X" markers shipped
 *     - TODO.*database    : "TODO: wire to DB" markers shipped
 *     - STUB              : explicit STUB markers in production code
 *     - MOCK              : explicit MOCK markers in production code
 *
 * Comment-only lines (starting with `//`, `/*`, or `*`) are stripped
 * before matching so doc strings that NEGATE the patterns
 * ("no globalThis / devStore / mockDb") do not trip the gate. Inline
 * comments are also stripped (everything after `//` on a code line).
 *
 * Test files (*.test.ts / *.test.tsx / *.spec.* / test-setup.ts / under
 * __tests__/ directories) are exempt -- they legitimately reference
 * "MOCK" in test descriptions and pull from fixtures.
 *
 * Exit codes:
 *   0  no forbidden patterns found
 *   1  one or more hits found (each printed with file:line)
 *   2  internal error
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(scriptDir, "..");
const scanRoot = join(repoRoot, "apps", "admin-web", "src");

/**
 * Each pattern is an object { id, re, why } so the failure log explains
 * WHY the pattern is forbidden (helps future contributors). The regex
 * runs against each line individually; multi-line matches are not
 * needed for these classes of hits.
 */
const FORBIDDEN_PATTERNS = [
  {
    id: "globalThis",
    re: /\bglobalThis\b/,
    why: "globalThis-backed state survives HMR but loses on restart -- replace with real backend persistence.",
  },
  {
    id: "devStore",
    re: /\bdevStore\b/,
    why: "devStore is an in-memory shim; production code must talk to the backend.",
  },
  {
    id: "dev-store",
    re: /dev-store/,
    why: "kebab-cased devStore variant -- same rule.",
  },
  {
    id: "mockDb",
    re: /\bmockDb\b/i,
    why: "in-memory mock database is forbidden in production code.",
  },
  {
    id: "mockData",
    re: /\bmockData\b/,
    why: "mockData is fake rows; admin shell must render real backend data or an honest empty/gap state.",
  },
  {
    id: "fakeData",
    re: /\bfakeData\b/,
    why: "fakeData is forbidden in production code.",
  },
  {
    id: "sampleData",
    re: /\bsampleData\b/,
    why: "sampleData is forbidden in production code.",
  },
  {
    id: "dummyData",
    re: /\bdummyData\b/,
    why: "dummyData is forbidden in production code.",
  },
  {
    id: "TODO-real",
    re: /TODO[^\n]*\breal\b/i,
    why: "shipped 'TODO: hook to real X' marker -- finish the wiring or render a backend-gap tile.",
  },
  {
    id: "TODO-database",
    re: /TODO[^\n]*\bdatabase\b/i,
    why: "shipped 'TODO: wire to database' marker -- finish the wiring or render a backend-gap tile.",
  },
  {
    id: "STUB",
    re: /\bSTUB\b/,
    why: "explicit STUB marker in production code -- replace with real implementation or honest gap tile.",
  },
  {
    id: "MOCK",
    re: /\bMOCK\b/,
    why: "explicit MOCK marker in production code -- replace with real implementation or honest gap tile.",
  },
];

/**
 * Strip the comment portion of a line so doc strings can mention the
 * forbidden tokens (e.g. negating prose like "no globalThis / devStore /
 * mockDb"). Returns the empty string for lines that are entirely
 * comments. Implementation notes:
 *
 *   - JSX/TS does not have a robust regex for "is in string vs in
 *     comment", but the forbidden tokens are unusual enough that
 *     stripping leading `//` / `/*` / leading-`*` (jsdoc continuation)
 *     lines plus the post-`//` tail of code lines is a sound,
 *     low-false-positive heuristic.
 *   - We do NOT attempt to honour `//` appearing inside a string literal;
 *     that would only cause us to UNDER-report, and the test suite
 *     itself asserts the production hits remain at zero, so the
 *     dual-layer guard catches regressions either way.
 */
function stripComments(rawLine) {
  const trimmed = rawLine.trim();
  if (trimmed.startsWith("//")) return "";
  if (trimmed.startsWith("/*")) return "";
  if (trimmed.startsWith("*")) return "";
  const idx = rawLine.indexOf("//");
  return idx === -1 ? rawLine : rawLine.slice(0, idx);
}

/**
 * Files that are EXEMPT from the scan. These are test sources / fixtures
 * where MOCK / mockData appear legitimately (vitest mocks, fixture
 * builders, test descriptions). They are intentionally excluded from
 * the production code guardrail.
 */
function isExempt(absPath) {
  const rel = relative(scanRoot, absPath).replace(/\\/g, "/");
  if (rel.startsWith("..")) return true;
  if (/\.test\.(ts|tsx|js|jsx)$/.test(rel)) return true;
  if (/\.spec\.(ts|tsx|js|jsx)$/.test(rel)) return true;
  if (rel === "test-setup.ts") return true;
  if (rel.includes("/__tests__/")) return true;
  if (rel.includes("/__fixtures__/")) return true;
  if (rel.includes("/__mocks__/")) return true;
  // Smoke guardrail suite itself references the forbidden tokens by
  // name when asserting they're absent -- exempt it.
  if (rel.startsWith("smoke/")) return true;
  return false;
}

function walk(dir, out = []) {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "dist") continue;
    const abs = join(dir, entry);
    const st = statSync(abs);
    if (st.isDirectory()) {
      walk(abs, out);
    } else if (/\.(ts|tsx|js|jsx|mjs|cjs)$/.test(entry)) {
      out.push(abs);
    }
  }
  return out;
}

function main() {
  let files;
  try {
    files = walk(scanRoot);
  } catch (err) {
    console.error(`[admin-smoke-guardrails] cannot scan ${scanRoot}: ${err.message}`);
    process.exit(2);
  }

  const hits = [];
  for (const file of files) {
    if (isExempt(file)) continue;
    let content;
    try {
      content = readFileSync(file, "utf8");
    } catch (err) {
      console.error(`[admin-smoke-guardrails] cannot read ${file}: ${err.message}`);
      process.exit(2);
    }
    const lines = content.split(/\r?\n/);
    for (let i = 0; i < lines.length; i++) {
      const rawLine = lines[i];
      const line = stripComments(rawLine);
      if (line === "") continue;
      for (const pat of FORBIDDEN_PATTERNS) {
        if (pat.re.test(line)) {
          hits.push({
            file: relative(repoRoot, file).replace(/\\/g, "/"),
            line: i + 1,
            pattern: pat.id,
            why: pat.why,
            snippet: rawLine.trim().slice(0, 200),
          });
        }
      }
    }
  }

  if (hits.length === 0) {
    console.log(
      `[admin-smoke-guardrails] OK -- scanned ${files.length} files; no forbidden patterns found.`,
    );
    process.exit(0);
  }

  console.error(
    `[admin-smoke-guardrails] FAIL -- ${hits.length} forbidden pattern hit(s) in production code:\n`,
  );
  for (const hit of hits) {
    console.error(`  ${hit.file}:${hit.line}  [${hit.pattern}]`);
    console.error(`    > ${hit.snippet}`);
    console.error(`    ${hit.why}`);
  }
  console.error(
    `\nAdmin production code must render real backend data or an honest backend-gap tile. ` +
      `If a pattern is needed for a legitimate test, move it under __tests__/ or rename the file to *.test.ts.`,
  );
  process.exit(1);
}

main();
