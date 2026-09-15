import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { packageDocument } from "./build_documents.mjs";

test("packaged links retain guides and resolve source references at the matching release", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "yatm-documents-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const source = path.join(root, "source"), target = path.join(root, "package");
  fs.mkdirSync(path.join(target, "docs"), { recursive: true });
  fs.mkdirSync(path.join(source, "library"), { recursive: true });
  fs.writeFileSync(path.join(target, "docs", "guide.md"), "guide");
  fs.writeFileSync(path.join(source, "library", "file.go"), "source");
  const input = "[guide](guide.md#use) [source](../library/file.go#L2) [folder](../library) [web](https://example.com) [anchor](#current)";
  assert.equal(packageDocument(input, "docs/page.md", source, target, "v1.0.0-alpha.1"),
    "[guide](guide.md#use) [source](https://github.com/samuelncui/yatm/blob/v1.0.0-alpha.1/library/file.go#L2) [folder](https://github.com/samuelncui/yatm/tree/v1.0.0-alpha.1/library) [web](https://example.com) [anchor](#current)");
  assert.throws(() => packageDocument("[missing](missing.md)", "docs/page.md", source, target, "v1.0.0-alpha.1"), /Broken documentation link/);
});
