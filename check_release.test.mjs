import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { gunzipSync, gzipSync } from "node:zlib";

const version = "v1.0.0-alpha.1";
const commit = "a".repeat(40);

function fixture(t, change = () => {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "yatm-package-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const content = path.join(root, "content");
  const files = new Map([
    ...["yatm-httpd", "yatm-cli", "yatm-export-library", "yatm-lto-info", "yatm-migrate", "LICENSE", "README.md", "CONTEXT.md", "templates/config.example.yaml", "templates/yatm-httpd.service", "skills/yatm/SKILL.md", "licenses/dependencies.json", "licenses/THIRD_PARTY_NOTICES"].map((name) => [name, "fixture"]),
    ...["encrypt", "get_device", "mkfs", "mount", "mount.openltfs", "readinfo", "umount"].map((name) => [`templates/scripts/${name}`, "fixture"]),
    ["VERSION", version], ["COMMIT", commit],
    ["frontend/index.html", `<meta name="yatm-version" content="${version}"><meta name="yatm-commit" content="${commit}">`],
  ]);
  change(files);
  for (const [name, value] of files) {
    const target = path.join(content, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, value);
  }
  const archive = path.join(root, "candidate.tar.gz");
  execFileSync("tar", ["--no-xattrs", "--no-acls", "-czf", archive, "-C", content, "."], { env: { ...process.env, COPYFILE_DISABLE: "1" } });
  fs.writeFileSync(`${archive}.sha256`, `${crypto.createHash("sha256").update(fs.readFileSync(archive)).digest("hex")}  ${path.basename(archive)}\n`);
  return archive;
}

function check(archive) {
  return spawnSync(process.execPath, ["check_release.mjs", archive], { encoding: "utf8" });
}

test("accepts an allowlisted candidate with matching identities", (t) => {
  const result = check(fixture(t));
  assert.equal(result.status, 0, result.stderr);
});

test("rejects altered bytes before reading package contents", (t) => {
  const archive = fixture(t);
  fs.appendFileSync(archive, "changed");
  assert.notEqual(check(archive).status, 0);
});

test("rejects filesystem metadata hidden in PAX records", (t) => {
  const archive = fixture(t);
  const value = "LIBARCHIVE.xattr.user.fixture=fixture\n";
  const record = Buffer.from(`${value.length + 3} ${value}`);
  const header = Buffer.alloc(512);
  header.write("PaxHeader");
  header.write(record.length.toString(8).padStart(11, "0"), 124);
  header.fill(32, 148, 156);
  header[156] = "x".charCodeAt(0);
  header.write("ustar", 257);
  const sum = header.reduce((total, byte) => total + byte, 0);
  header.write(sum.toString(8).padStart(6, "0") + "\0 ", 148);
  const data = gzipSync(Buffer.concat([header, record, Buffer.alloc(512 - record.length), gunzipSync(fs.readFileSync(archive))]));
  fs.writeFileSync(archive, data);
  fs.writeFileSync(`${archive}.sha256`, `${crypto.createHash("sha256").update(data).digest("hex")}  ${path.basename(archive)}\n`);
  const result = check(archive);
  assert.notEqual(result.status, 0);
  assert(result.stderr.includes("Private filesystem metadata"), result.stderr);
});

for (const [name, change, message] of [
  ["missing executable", (files) => files.delete("yatm-cli"), "Required package file missing"],
  ["active config", (files) => files.set("config.yaml", "private"), "Unexpected archive entry"],
  ["development dependency", (files) => files.set("frontend/node_modules/test.js", "private"), "Development/private file"],
  ["mixed frontend version", (files) => files.set("frontend/index.html", "development"), "Frontend version does not match"],
]) {
  test(`rejects ${name}`, (t) => {
    const result = check(fixture(t, change));
    assert.notEqual(result.status, 0);
    assert(result.stderr.includes(message), result.stderr);
  });
}
