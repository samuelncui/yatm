// Keep packaged guides navigable while source-only references point to the release tag.
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

export function packageDocument(text, document, sourceRoot, packageRoot, version) {
  return text.replace(/\]\(([^\s)]+)([^)]*)\)/g, (match, href, suffix) => {
    if (/^(?:[a-z][a-z0-9+.-]*:|\/\/|#)/i.test(href)) return match;
    const [filename, fragment] = href.split("#", 2);
    const relative = path.normalize(path.join(path.dirname(document), decodeURIComponent(filename)));
    if (fs.existsSync(path.join(packageRoot, relative))) return match;
    const source = path.join(sourceRoot, relative);
    if (!fs.existsSync(source)) throw new Error(`Broken documentation link: ${document} -> ${href}`);
    const kind = fs.statSync(source).isDirectory() ? "tree" : "blob";
    const url = `https://github.com/samuelncui/yatm/${kind}/${encodeURIComponent(version)}/${relative.split(path.sep).map(encodeURIComponent).join("/")}`;
    return `](${url}${fragment ? `#${fragment}` : ""}${suffix})`;
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const [packageRoot, version] = process.argv.slice(2);
  if (!packageRoot || !/^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/.test(version ?? "")) {
    throw new Error("usage: node build_documents.mjs <package-directory> <release-tag>");
  }
  const docs = fs.readdirSync(path.join(packageRoot, "docs"), { recursive: true })
    .filter((name) => name.endsWith(".md")).map((name) => path.join("docs", name));
  for (const document of ["README.md", "CONTEXT.md", ...docs]) {
    const filename = path.join(packageRoot, document);
    fs.writeFileSync(filename, packageDocument(fs.readFileSync(filename, "utf8"), document, process.cwd(), packageRoot, version));
  }
}
