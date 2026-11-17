import console from "node:console";
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const catalogs = { en: "src/locales/en/messages.po", "zh-CN": "src/locales/zh-CN/messages.po" };

/** Entries (without the header) whose translation is empty. */
export function untranslated(po) {
  const missing = [];
  for (const block of po.split(/\n\n+/)) {
    const id = block.match(/^msgid ((?:".*"\n?)+)/m)?.[1];
    const str = block.match(/^msgstr ((?:".*"\n?)+)/m)?.[1];
    if (id === undefined || str === undefined) continue;
    const text = (quoted) =>
      quoted
        .split("\n")
        .filter(Boolean)
        .map((line) => JSON.parse(line))
        .join("");
    if (text(id) !== "" && text(str) === "") missing.push(text(id));
  }
  return missing;
}

/** The creation timestamp changes on every extraction and is not content. */
export const withoutTimestamp = (po) => po.replace(/^"POT-Creation-Date:.*\n/m, "");

export function checkMessages(root) {
  const read = (locale) => readFileSync(resolve(root, catalogs[locale]), "utf8");
  const before = Object.fromEntries(Object.keys(catalogs).map((locale) => [locale, read(locale)]));
  execFileSync("pnpm", ["exec", "lingui", "extract", "--clean"], { cwd: root, stdio: "pipe" });
  const errors = [];
  for (const locale of Object.keys(catalogs)) {
    const after = read(locale);
    if (withoutTimestamp(after) !== withoutTimestamp(before[locale])) {
      errors.push(`${catalogs[locale]} is out of date with the source text; run "pnpm i18n:extract" and commit the result.`);
    }
    // Leave the working tree as found: this check reports, it does not rewrite.
    writeFileSync(resolve(root, catalogs[locale]), before[locale]);
  }
  for (const message of untranslated(before["zh-CN"])) errors.push(`zh-CN has no translation for: ${message}`);
  return errors;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const errors = checkMessages(resolve(fileURLToPath(import.meta.url), "../.."));
  if (errors.length) {
    console.error(errors.join("\n"));
    process.exit(1);
  }
}
