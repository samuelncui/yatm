import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { checkContent, stageSource, stageBinaryStrings } from "./check-content.mjs";

function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "yatm-content-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

test("source staging includes current changes and untracked inputs but excludes ignored data", (t) => {
  const root = temporary(t);
  const repository = path.join(root, "repository");
  const destination = path.join(root, "source");
  fs.mkdirSync(repository);
  execFileSync("git", ["init", "-q", repository]);
  fs.writeFileSync(path.join(repository, ".gitignore"), "ignored\n");
  fs.writeFileSync(path.join(repository, "tracked"), "old");
  execFileSync("git", ["-C", repository, "add", "."]);
  fs.writeFileSync(path.join(repository, "tracked"), "current");
  fs.writeFileSync(path.join(repository, "untracked"), "new");
  fs.writeFileSync(path.join(repository, "ignored"), "private");
  stageSource(repository, destination);
  assert.equal(fs.readFileSync(path.join(destination, "tracked"), "utf8"), "current");
  assert.equal(fs.readFileSync(path.join(destination, "untracked"), "utf8"), "new");
  assert(!fs.existsSync(path.join(destination, "ignored")));
  assert(!fs.existsSync(path.join(destination, ".git")));
});

test("source staging refuses both resolvable and dangling symlinks to workstation files", (t) => {
  const root = temporary(t);
  const repository = path.join(root, "repository");
  fs.mkdirSync(repository);
  execFileSync("git", ["init", "-q", repository]);
  fs.writeFileSync(path.join(root, "private"), "private");
  fs.symlinkSync(path.join(root, "private"), path.join(repository, "link"));
  assert.throws(() => stageSource(repository, path.join(root, "source")), /ordinary file/);
  fs.unlinkSync(path.join(root, "private"));
  assert.throws(() => stageSource(repository, path.join(root, "source")), /ordinary file/);
});

test("embedded binary strings are scanned without extracting archive paths", (t) => {
  const root = temporary(t);
  const content = path.join(root, "content");
  const artifacts = path.join(root, "artifacts");
  fs.mkdirSync(content);
  fs.mkdirSync(artifacts);
  fs.writeFileSync(path.join(content, "yatm-cli"), Buffer.concat([Buffer.from("7f454c46", "hex"), Buffer.from("\0printable-fixture\0")]));
  execFileSync("tar", ["-czf", path.join(artifacts, "fixture.tgz"), "-C", content, "."]);
  const destination = path.join(root, "strings");
  assert.equal(stageBinaryStrings(artifacts, destination), 1);
  assert.equal(fs.readFileSync(path.join(destination, "0.txt"), "utf8"), "printable-fixture");
});

// Release CI supplies the checksum-verified scanner; local tool-only tests remain offline.
const scanner = process.env.GITLEAKS_BIN;
test("content scanning detects a dummy credential inside a compressed archive without emitting it", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const content = path.join(root, "content");
  const artifacts = path.join(root, "artifacts");
  fs.mkdirSync(content);
  fs.mkdirSync(artifacts);
  const dummy = ["ghp", crypto.randomBytes(18).toString("hex")].join("_");
  fs.writeFileSync(path.join(content, "fixture.txt"), `credential=${dummy}\n`);
  execFileSync("tar", ["-czf", path.join(artifacts, "fixture.tgz"), "-C", content, "."]);
  assert.throws(() => checkContent("artifacts", artifacts, undefined, scanner), (error) => {
    assert.match(error.message, /Content check failed/);
    assert(!error.message.includes(dummy));
    return true;
  });
});

test("content scanning detects personal paths and passes public text", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const file = path.join(root, "fixture.txt");
  fs.writeFileSync(file, ["", "Users", "fixture", "private.txt"].join("/"));
  assert.throws(() => checkContent("artifacts", root, undefined, scanner), /personal-build-path/);
  fs.writeFileSync(file, "https://github.com/samuelncui/yatm\n");
  checkContent("artifacts", root, undefined, scanner);
});

test("public upstream fixture exceptions still detect other credentials and paths in the same member", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const member = path.join(root, "candidate.tar.gz!ffmpeg-8.0.tar.xz!ffmpeg-8.0/tests/ref/fate/mxf-probe-dnxhd");
  fs.mkdirSync(path.dirname(member), { recursive: true });
  const publicPath = ["", "Users", "mark", "Dev", "fixture.aaf"].join("/");
  fs.writeFileSync(member, `TAG:comment_UNC Path=${publicPath}\n`);
  checkContent("artifacts", root, undefined, scanner);

  const dummy = ["ghp", crypto.randomBytes(18).toString("hex")].join("_");
  fs.appendFileSync(member, `credential=${dummy}\n`);
  assert.throws(() => checkContent("artifacts", root, undefined, scanner), /github-pat/);

  fs.writeFileSync(member, ["", "Users", "private-fixture", "private.txt"].join("/"));
  assert.throws(() => checkContent("artifacts", root, undefined, scanner), /personal-build-path/);
});

test("credential checks include dependency and generated source", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const dummy = ["ghp", crypto.randomBytes(18).toString("hex")].join("_");
  for (const name of ["vendor/github.com/example/generated.go", "node_modules/example/source.js", "frontend/src/entity/generated.ts"]) {
    const file = path.join(root, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, `credential=${dummy}\n`);
    assert.throws(() => checkContent("artifacts", root, undefined, scanner), /github-pat/);
    fs.rmSync(file);
  }
});

test("the public module checksum exception excludes no other go.sum content", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const file = path.join(root, "go.sum");
  const checksum = fs.readFileSync(new URL("../../go.sum", import.meta.url), "utf8").split("\n")
    .find((line) => line.startsWith("modernc.org/token v1.1.0 h1:"));
  assert(checksum);
  fs.writeFileSync(file, "public context\n" + checksum + "\nmore public context\n");
  checkContent("artifacts", root, undefined, scanner);

  const dummy = ["ghp", crypto.randomBytes(18).toString("hex")].join("_");
  fs.appendFileSync(file, `credential=${dummy}\n`);
  assert.throws(() => checkContent("artifacts", root, undefined, scanner), /github-pat/);

  fs.writeFileSync(file, checksum.replace("h1:X", "h1:Z") + "\n");
  assert.throws(() => checkContent("artifacts", root, undefined, scanner), /generic-api-key/);
});

test("history exceptions match one commit without exempting current source, artifacts or later history", { skip: !scanner }, (t) => {
  const root = temporary(t);
  const repository = path.join(root, "repository");
  fs.mkdirSync(repository);
  const git = (...args) => execFileSync("git", ["-C", repository, ...args], { encoding: "utf8" }).trim();
  git("init", "-q");
  git("config", "user.name", "Content test");
  git("config", "user.email", "content-test@example.invalid");
  const commit = () => {
    git("add", ".");
    git("-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Content fixture");
    return git("rev-parse", "HEAD");
  };
  const file = path.join(repository, "fixture.txt");
  const fixture = ["", "Users", "history-fixture", "file.txt"].join("/") + "\n";
  fs.writeFileSync(file, fixture);
  const original = commit();
  fs.writeFileSync(file, "Public fixture\n");
  commit();
  const ignore = path.join(repository, ".gitleaksignore");
  fs.writeFileSync(ignore, `# Approved history only\n${original}:fixture.txt:personal-build-path:1\n`);
  checkContent("source", repository, "HEAD", scanner);

  fs.writeFileSync(file, fixture);
  assert.throws(() => checkContent("source", repository, "HEAD", scanner), /personal-build-path/);
  const artifacts = path.join(root, "artifacts");
  fs.mkdirSync(artifacts);
  fs.copyFileSync(file, path.join(artifacts, "fixture.txt"));
  fs.writeFileSync(path.join(artifacts, ".gitleaksignore"), "fixture.txt:personal-build-path:1\n");
  assert.throws(() => checkContent("artifacts", artifacts, undefined, scanner), /personal-build-path/);

  commit();
  fs.writeFileSync(file, "Public fixture again\n");
  commit();
  assert.throws(() => checkContent("source", repository, "HEAD", scanner), /personal-build-path/);
});

test("history scanning rejects exceptions without a full commit or with wildcard paths", { skip: !scanner }, (t) => {
  const root = temporary(t);
  execFileSync("git", ["init", "-q", root]);
  for (const fingerprint of ["fixture.txt:personal-build-path:1", `${"a".repeat(40)}:*.txt:personal-build-path:1`]) {
    fs.writeFileSync(path.join(root, ".gitleaksignore"), fingerprint + "\n");
    assert.throws(() => checkContent("source", root, "HEAD", scanner), /History exceptions require exact/);
  }
});
