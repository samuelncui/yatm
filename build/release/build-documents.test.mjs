import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { packageDocument } from "./build-documents.mjs";

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

test("packaged user entry and Tape procedures stay local while developer references use the release", (t) => {
  const target = fs.mkdtempSync(path.join(os.tmpdir(), "yatm-user-guides-"));
  t.after(() => fs.rmSync(target, { recursive: true, force: true }));
  const source = path.resolve(import.meta.dirname, "../..");
  const guides = ["index", "install", "migration", "locations", "library", "jobs", "devices", "tape-scripts", "read-order", "cli", "tar-tape-recovery", "offline-migration"];
  fs.mkdirSync(path.join(target, "docs/operations"), { recursive: true });
  for (const guide of guides) {
    fs.copyFileSync(path.join(source, `docs/operations/${guide}.md`), path.join(target, `docs/operations/${guide}.md`));
  }
  for (const [original, installed] of [
    ["config.example.yaml", "templates/config.example.yaml"],
    ["scripts/mount", "templates/scripts/mount"],
    ["scripts/readinfo", "templates/scripts/readinfo"],
    ["scripts/umount", "templates/scripts/umount"],
  ]) {
    fs.mkdirSync(path.dirname(path.join(target, installed)), { recursive: true });
    fs.copyFileSync(path.join(source, original), path.join(target, installed));
  }
  const documents = new Map(guides.map(guide => {
    const name = `docs/operations/${guide}.md`;
    return [guide, packageDocument(fs.readFileSync(path.join(target, name), "utf8"), name, source, target, "v1.0.0-alpha.1")];
  }));
  assert.match(documents.get("index"), /\]\(devices\.md\)/);
  assert.match(documents.get("index"), /\]\(tar-tape-recovery\.md\)/);
  assert.match(documents.get("library"), /\]\(\.\.\/\.\.\/templates\/config\.example\.yaml\)/);
  assert.match(documents.get("migration"), /\]\(\.\.\/\.\.\/templates\/scripts\/mount\)/);
  assert.match(documents.get("tape-scripts"), /\]\(\.\.\/\.\.\/templates\/scripts\/readinfo\)/);
  assert.match(documents.get("tape-scripts"), /\]\(\.\.\/\.\.\/templates\/scripts\/umount\)/);
  assert.match(documents.get("devices"), /\]\(tar-tape-recovery\.md\)/);
  assert.match(documents.get("tar-tape-recovery"), /\]\(devices\.md\)/);
  assert.match(documents.get("index"), /\]\(https:\/\/github\.com\/samuelncui\/yatm\/blob\/v1\.0\.0-alpha\.1\/docs\/README\.md\)/);
  assert(!fs.existsSync(path.join(target, "docs/README.md")));
  for (const [guide, text] of documents) {
    for (const match of text.matchAll(/\]\(([^\s)]+)\)/g)) {
      const href = match[1];
      if (/^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(href)) continue;
      assert(fs.existsSync(path.resolve(target, "docs/operations", decodeURIComponent(href.split("#")[0]))), `${guide}: ${href}`);
    }
  }
});
