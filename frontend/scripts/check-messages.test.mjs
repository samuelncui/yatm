import assert from "node:assert/strict";
import test from "node:test";
import { untranslated, withoutTimestamp } from "./check-messages.mjs";

const header = 'msgid ""\nmsgstr ""\n"POT-Creation-Date: 2026-01-01 00:00+0000\\n"\n"Language: zh-CN\\n"';

test("reports empty translations and ignores the header", () => {
  const po = `${header}\n\nmsgid "Save"\nmsgstr "保存"\n\nmsgctxt "x"\nmsgid "Scan"\nmsgstr ""\n\nmsgid ""\n"Long "\n"text"\nmsgstr ""`;
  assert.deepEqual(untranslated(po), ["Scan", "Long text"]);
});

test("the creation timestamp is not content", () => {
  assert.equal(withoutTimestamp(header), 'msgid ""\nmsgstr ""\n"Language: zh-CN\\n"');
});
