import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// SOCPuppet is English-only. Keep checking literal t() call coverage and fail if
// a locale dictionary is accidentally reintroduced into the shipped bundle.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const localeDir = join(root, "src/i18n/locales");
const dictionaries = existsSync(localeDir) ? readdirSync(localeDir).filter((name) => name.endsWith(".json")) : [];

const files = [];
function walk(dir) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    const st = statSync(p);
    if (st.isDirectory()) walk(p);
    else if (/\.(ts|tsx)$/.test(name)) files.push(p);
  }
}
walk(join(root, "src"));

const calls = new Map();
const literalCall = /(?:^|[^\w$.])t\(\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')/gm;
for (const file of files) {
  const src = readFileSync(file, "utf8");
  for (const m of src.matchAll(literalCall)) {
    const key =
      m[1] !== undefined ? JSON.parse(`"${m[1]}"`) : m[2].replace(/\\(['\\])/g, "$1");
    if (!calls.has(key)) calls.set(key, file.slice(root.length + 1));
  }
}

const errors = [];
if (dictionaries.length) errors.push(`Unexpected locale dictionaries: ${dictionaries.join(", ")}`);
if (!calls.size) errors.push("No literal t() calls found");
console.log(`i18n: English-only UI with ${calls.size} literal keys and no locale dictionaries`);
if (errors.length) {
  console.error(errors.join("\n"));
  process.exit(1);
}
