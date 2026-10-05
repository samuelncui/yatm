import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repositoryFiles = new Set(execFileSync(
  "git",
  ["ls-files", "--cached", "--others", "--exclude-standard", "-z"],
  { cwd: repository, encoding: "utf8" },
).split("\0").filter(Boolean).filter((name) => fs.existsSync(path.join(repository, name))));

const markdown = [...repositoryFiles].filter((name) => name.endsWith(".md")).sort();
const documentation = markdown.filter((name) => name.startsWith("docs/"));
const allowedDocumentation = /^(?:docs\/README\.md|docs\/(?:architecture|operations)\/[a-z0-9-]+\.md)$/;
const retiredDocumentationReference = /docs\/(?:history|designs|decisions|releases)\/|docs\/(?:demo|e2e-test|physical-tape-e2e|platform-changes|platform-design)\.md|docs\/operations\/(?:demo|preview-build)\.md|docs\/architecture\/(?:api-ui|states)\.md/;

for (const document of documentation) {
  assert.match(document, allowedDocumentation, `Documentation has no current owner: ${document}`);
}

for (const filename of repositoryFiles) {
  const contents = fs.readFileSync(path.join(repository, filename));
  if (contents.includes(0)) continue;
  assert(!retiredDocumentationReference.test(contents.toString("utf8")),
    `Repository content refers to retired documentation: ${filename}`);
}

const index = fs.readFileSync(path.join(repository, "docs/README.md"), "utf8");
for (const document of documentation) {
  if (document === "docs/README.md") continue;
  const relative = path.posix.relative("docs", document);
  assert(index.includes(`](${relative})`), `Documentation is missing from docs/README.md: ${document}`);
}

const committableDirectory = (name) => [...repositoryFiles].some((file) => file.startsWith(`${name}/`));
const localLink = /!?\[[^\]]*\]\(([^\s)]+)(?:\s+[^)]*)?\)/g;
const anchorCache = new Map();
const markdownAnchors = (document) => {
  if (anchorCache.has(document)) return anchorCache.get(document);
  const anchors = new Set();
  const occurrences = new Map();
  const text = fs.readFileSync(path.join(repository, document), "utf8");
  for (const match of text.matchAll(/^#{1,6}\s+(.+?)\s*#*\s*$/gm)) {
    const base = match[1]
      .replace(/<[^>]*>/g, "")
      .replace(/!?\[([^\]]+)\]\([^)]+\)/g, "$1")
      .replace(/[`*_~]/g, "")
      .trim()
      .toLowerCase()
      .replace(/[^\p{L}\p{N}\-_ ]/gu, "")
      .replace(/ /g, "-");
    const seen = occurrences.get(base) ?? 0;
    occurrences.set(base, seen + 1);
    anchors.add(seen === 0 ? base : `${base}-${seen}`);
  }
  anchorCache.set(document, anchors);
  return anchors;
};
for (const document of markdown) {
  const text = fs.readFileSync(path.join(repository, document), "utf8");
  assert(!/(?:^|[\s`'"(])\.local(?:\/|\b)/m.test(text), `Repository document refers to private workspace material: ${document}`);

  for (const match of text.matchAll(localLink)) {
    const href = match[1].replace(/^<|>$/g, "");
    if (/^(?:[a-z][a-z0-9+.-]*:|\/\/)/i.test(href)) continue;
    assert(!path.isAbsolute(href), `Repository document uses a machine-local path: ${document} -> ${href}`);

    let filename;
    let fragment;
    try {
      const separator = href.indexOf("#");
      filename = decodeURIComponent(separator === -1 ? href : href.slice(0, separator));
      fragment = separator === -1 ? "" : decodeURIComponent(href.slice(separator + 1));
    } catch {
      assert.fail(`Repository document has an invalid encoded link: ${document} -> ${href}`);
    }
    const target = filename
      ? path.posix.normalize(path.posix.join(path.posix.dirname(document), filename))
      : document;
    assert(!target.startsWith("../"), `Repository document links outside the repository: ${document} -> ${href}`);
    assert(repositoryFiles.has(target) || committableDirectory(target),
      `Repository document links to a missing or ignored file: ${document} -> ${href}`);
    if (fragment && target.endsWith(".md")) {
      assert(markdownAnchors(target).has(fragment),
        `Repository document links to a missing heading: ${document} -> ${href}`);
    }
  }
}

console.log(`Validated ${markdown.length} repository Markdown files and ${documentation.length} current documents.`);
