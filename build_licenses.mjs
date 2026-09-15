// Collect distributed dependency notices from the locked, installed source graph.
import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";

const output = path.resolve(process.argv[2] || "output/licenses");
fs.mkdirSync(output, { recursive: true });
const records = [];

function copyNotices(kind, name, version, directory, license = "") {
  const target = path.join(output, kind, `${name}@${version}`);
  const names = fs.readdirSync(directory).filter((entry) => /^(licen[cs]e|notice|copying|copyright|patents)([.-].*)?$/i.test(entry));
  fs.mkdirSync(target, { recursive: true });
  const copied = [];
  for (const entry of names) {
    const source = path.join(directory, entry);
    if (!fs.statSync(source).isFile()) continue;
    fs.copyFileSync(source, path.join(target, entry));
    copied.push(entry);
  }
  // These published subpackages omit their monorepo's root license.
  if (!copied.length && name.startsWith("@react-dnd/")) {
    fs.copyFileSync("licenses/react-dnd.LICENSE", path.join(target, "LICENSE"));
    copied.push("LICENSE");
  }
  // Package metadata may contain an inline license or point to a bundled notice.
  if (kind === "node") fs.copyFileSync(path.join(directory, "package.json"), path.join(target, "package.json"));
  records.push({ kind, name, version, license, notices: copied });
  if (!copied.length) throw new Error(`No bundled license found for ${name}@${version}; review attribution before release`);
}

// Go reports precisely the modules used by the five target programs, not test-only modules.
const modules = new Map();
const lines = execFileSync("go", ["list", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}",
  "./cmd/httpd", "./cmd/yatm-cli", "./cmd/export-library", "./cmd/lto-info", "./cmd/migrate"], { encoding: "utf8" });
for (const line of lines.split("\n")) {
  const [name, version, directory] = line.split("\t");
  if (name && version && directory) modules.set(`${name}@${version}`, { name, version, directory });
}
for (const item of [...modules.values()].sort((a, b) => a.name.localeCompare(b.name))) {
  copyNotices("go", item.name, item.version, item.directory);
}
const root = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
copyNotices("go", "stdlib", execFileSync("go", ["env", "GOVERSION"], { encoding: "utf8" }).trim(), root);

// Follow the installed production graph, resolving pnpm's symlinks without its mutable store index.
const visited = new Set();
function dependency(name, from, optional = false) {
  let current = from;
  while (true) {
    const candidate = path.join(current, "node_modules", name, "package.json");
    if (fs.existsSync(candidate)) {
      const directory = path.dirname(fs.realpathSync(candidate));
      if (visited.has(directory)) return;
      visited.add(directory);
      const pkg = JSON.parse(fs.readFileSync(path.join(directory, "package.json"), "utf8"));
      copyNotices("node", pkg.name, pkg.version, directory, pkg.license || "");
      for (const child of Object.keys(pkg.dependencies || {}).sort()) dependency(child, directory, child in (pkg.optionalDependencies || {}));
      for (const child of Object.keys(pkg.optionalDependencies || {}).sort()) dependency(child, directory, true);
      return;
    }
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }
  if (!optional) throw new Error(`Installed production dependency is missing: ${name}`);
}
const frontend = path.resolve("frontend");
const pkg = JSON.parse(fs.readFileSync(path.join(frontend, "package.json"), "utf8"));
for (const name of Object.keys(pkg.dependencies).sort()) dependency(name, frontend);
fs.writeFileSync(path.join(output, "dependencies.json"), JSON.stringify(records, null, 2) + "\n");

// Keep copied-source attribution outside the module graph as an explicit maintained input.
fs.copyFileSync("THIRD_PARTY_NOTICES", path.join(output, "THIRD_PARTY_NOTICES"));
fs.copyFileSync("licenses/lto-info.LICENSE", path.join(output, "lto-info.LICENSE"));
fs.copyFileSync("licenses/react-dnd.LICENSE", path.join(output, "react-dnd.LICENSE"));
console.log(`Collected notices for ${records.length} distributed dependencies.`);
