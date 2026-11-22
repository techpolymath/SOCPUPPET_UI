import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";
import ts from "typescript";

const sourceURL = new URL("../src/i18n/index.ts", import.meta.url);
const code = ts.transpileModule(readFileSync(sourceURL, "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, esModuleInterop: true },
}).outputText;

function load() {
  const context = { exports: {} };
  vm.runInNewContext(code, context, { filename: "i18n/index.js" });
  return context.exports;
}

test("the UI is English-only and interpolates own properties safely", () => {
  const api = load();
  assert.equal(api.intlLocale, "en-US");
  assert.equal(api.t("Language"), "Language");
  assert.equal(api.t("Unknown {value}", { value: 3 }), "Unknown 3");
  assert.equal(api.t("constructor"), "constructor");
  assert.equal(api.t("toString"), "toString");
  assert.equal(api.t("__proto__"), "__proto__");
  assert.equal(api.t("{count} tasks", Object.create({ count: 3 })), "{count} tasks");
});
