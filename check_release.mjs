// Validate a built archive without executing cross-compiled programs.
import assert from "node:assert/strict";
import fs from "node:fs";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";
import path from "node:path";
import { gunzipSync } from "node:zlib";

const archive = process.argv[2];
assert(archive, "usage: node check_release.mjs <archive.tar.gz>");
const checksum = fs.readFileSync(`${archive}.sha256`, "utf8").trim();
assert.equal(checksum, `${crypto.createHash("sha256").update(fs.readFileSync(archive)).digest("hex")}  ${path.basename(archive)}`);
// PAX metadata is not listed as ordinary archive members; inspect its records separately.
const tar = gunzipSync(fs.readFileSync(archive));
for (let offset = 0; offset + 512 <= tar.length;) {
  const header = tar.subarray(offset, offset + 512);
  if (header.every((byte) => byte === 0)) break;
  const size = Number.parseInt(header.subarray(124, 136).toString().replace(/\0.*$/, "").trim(), 8);
  assert(Number.isSafeInteger(size) && size >= 0, "Invalid package tar size");
  const type = String.fromCharCode(header[156]);
  if (type === "x" || type === "g") {
    const metadata = tar.subarray(offset + 512, offset + 512 + size).toString();
    assert(!/(?:LIBARCHIVE|SCHILY)\.(?:xattr|acl)/.test(metadata), "Private filesystem metadata in archive");
  }
  offset += 512 + Math.ceil(size / 512) * 512;
}
const entries = execFileSync("tar", ["-tzf", archive], { encoding: "utf8" }).trim().split("\n").map((name) => name.replace(/^\.\//, ""));
const roots = new Set(["yatm-httpd", "yatm-cli", "yatm-export-library", "yatm-lto-info", "yatm-migrate", "VERSION", "COMMIT", "LICENSE", "README.md", "CONTEXT.md"]);
const directories = new Set(["frontend", "docs", "templates", "licenses", "skills"]);
for (const entry of entries) {
  if (!entry || entry === ".") continue;
  assert(!entry.startsWith("/") && !entry.split("/").includes(".."), `Unsafe archive path: ${entry}`);
  assert(roots.has(entry) || directories.has(entry.split("/")[0]), `Unexpected archive entry: ${entry}`);
  assert(!/(^|\/)(\.git|node_modules|config\.yaml|scripts\/demo)(\/|$)/.test(entry), `Development/private file in archive: ${entry}`);
}
for (const entry of [...roots, "frontend/index.html", "templates/config.example.yaml", "templates/yatm-httpd.service", "skills/yatm/SKILL.md", "licenses/dependencies.json", "licenses/THIRD_PARTY_NOTICES"]) {
  assert(entries.includes(entry), `Required package file missing: ${entry}`);
}
for (const script of ["encrypt", "get_device", "mkfs", "mount", "mount.openltfs", "readinfo", "umount"]) assert(entries.includes(`templates/scripts/${script}`));
const read = (entry) => execFileSync("tar", ["-xOzf", archive, `./${entry}`], { encoding: "utf8" }).trim();
const version = read("VERSION");
const commit = read("COMMIT");
assert.match(version, /^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/);
assert.match(commit, /^[a-f0-9]{40}$/);
const html = read("frontend/index.html");
assert(html.includes(`name="yatm-version" content="${version}"`), "Frontend version does not match package");
assert(html.includes(`name="yatm-commit" content="${commit}"`), "Frontend commit does not match package");
console.log(`Validated ${path.basename(archive)} (${entries.length} entries; ${commit}).`);
