import assert from "node:assert/strict";
import crypto from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const rules = fileURLToPath(new URL("./content-rules.toml", import.meta.url));
const scannerVersion = "8.30.1";
const defaultRulesChecksum = "e163e53b9e7e8a8511e77271e2b323ed057759542a6d988258afe3a1fa329caf";

function scannerRules(scanner, temporary) {
  const executable = scanner.includes("/") ? scanner : execFileSync("which", [scanner], { encoding: "utf8" }).trim();
  const original = fs.readFileSync(path.join(path.dirname(fs.realpathSync(executable)), "gitleaks-default.toml"));
  assert.equal(crypto.createHash("sha256").update(original).digest("hex"), defaultRulesChecksum, "Unexpected default credential rules; run install-gitleaks.sh");
  // Keep the pinned credential rules, removing their global vendor/generated-file exclusions.
  const text = original.toString("utf8");
  const start = text.indexOf("\n[allowlist]\n");
  const end = text.indexOf("\n[[rules]]\n", start);
  assert(start >= 0 && end > start, "Unexpected default credential configuration");
  const base = path.join(temporary, "default-rules.toml");
  fs.writeFileSync(base, text.slice(0, start) + text.slice(end));
  const config = path.join(temporary, "content-rules.toml");
  fs.writeFileSync(config, fs.readFileSync(rules, "utf8").replace("useDefault = true", `path = ${JSON.stringify(base)}`));
  return config;
}

// Secret scanners skip executable bytes. Check embedded printable strings separately,
// including native libraries, without extracting archive paths onto the workstation.
export function stageBinaryStrings(directory, destination) {
  fs.mkdirSync(destination, { recursive: true });
  let count = 0;
  for (const name of fs.readdirSync(directory)) {
    if (!/\.(?:tar\.gz|tgz)$/.test(name)) continue;
    const archive = path.join(directory, name);
    const members = execFileSync("tar", ["-tzf", archive], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 }).split("\n");
    for (const member of members) {
      if (!/(?:^|\/)(?:yatm-(?:httpd|cli|export-library|lto-info|migrate|preview)|[^/]+\.(?:so(?:\.[0-9]+)*|dylib))$/.test(member)) continue;
      const bytes = execFileSync("tar", ["-xOzf", archive, member], { maxBuffer: 256 * 1024 * 1024 });
      const magic = bytes.subarray(0, 4).toString("hex");
      if (!["7f454c46", "feedface", "cefaedfe", "feedfacf", "cffaedfe", "cafebabe", "bebafeca", "cafebabf", "bfbafeca"].includes(magic)) continue;
      const strings = bytes.toString("latin1").match(/[\x20-\x7e]{8,}/g) || [];
      fs.writeFileSync(path.join(destination, `${count++}.txt`), strings.join("\n"));
    }
  }
  return count;
}

// Scan only releasable source inputs; ignored workstation data never enters this staging tree.
export function stageSource(repository, destination) {
  const names = execFileSync("git", ["-C", repository, "ls-files", "--cached", "--others", "--exclude-standard", "-z"], { encoding: "utf8" }).split("\0").filter(Boolean);
  for (const name of new Set(names)) {
    assert(!path.isAbsolute(name) && !name.split("/").includes(".."), "Unsafe source path");
    const source = path.join(repository, name);
    const entry = fs.lstatSync(source, { throwIfNoEntry: false });
    if (!entry) continue;
    assert(entry.isFile(), `Source input must be an ordinary file: ${name}`);
    const target = path.join(destination, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.copyFileSync(source, target);
  }
}

function stageHistoryIgnores(repository, destination) {
  const source = path.join(repository, ".gitleaksignore");
  const entry = fs.lstatSync(source, { throwIfNoEntry: false });
  if (!entry) {
    fs.writeFileSync(destination, "");
    return destination;
  }
  assert(entry.isFile(), "History exceptions must be an ordinary file");
  const fingerprints = fs.readFileSync(source, "utf8").split(/\r?\n/)
    .map((line) => line.trim()).filter((line) => line && !line.startsWith("#"));
  for (const fingerprint of fingerprints) {
    assert.match(fingerprint, /^[0-9a-f]{40}:[^:\r\n]+:[a-z0-9][a-z0-9-]*:[1-9][0-9]*$/, "History exceptions require exact commit:file:rule:line fingerprints");
    const filename = fingerprint.split(":")[1];
    assert(!path.isAbsolute(filename) && !filename.split("/").includes("..") && !/[\\*?\[\]]/.test(filename), "History exceptions require exact relative paths");
  }
  fs.writeFileSync(destination, fingerprints.join("\n") + "\n");
  return destination;
}

export function checkContent(mode, target, historyRange, scanner = process.env.GITLEAKS_BIN || "gitleaks") {
  assert(["source", "artifacts"].includes(mode), "Usage: check-content.mjs <source|artifacts> <directory> [history-range]");
  assert(target && fs.statSync(target).isDirectory(), "Content directory is required");
  assert(!historyRange || mode === "source", "History range is only supported for source checks");
  assert(!historyRange?.startsWith("-"), "History range must not be an option");
  assert.equal(execFileSync(scanner, ["version"], { encoding: "utf8" }).trim(), scannerVersion, "Unexpected Gitleaks version");
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "yatm-content-check-"));
  try {
    const config = scannerRules(scanner, temporary);
    // Commit fingerprints are accepted only for history, never current source or packages.
    const noIgnores = path.join(temporary, "no-ignores");
    fs.writeFileSync(noIgnores, "");
    const historyIgnores = historyRange ? stageHistoryIgnores(path.resolve(target), path.join(temporary, "history-ignores")) : noIgnores;
    const source = path.join(temporary, "source");
    fs.mkdirSync(source);
    if (mode === "source") stageSource(path.resolve(target), source);
    const commands = [["dir", mode === "source" ? source : path.resolve(target)]];
    if (mode === "artifacts") {
      // Gitleaks 8.30.1 does not open .tgz archives; use a scan-only .tar.gz copy for npm packages.
      const npm = path.join(temporary, "npm");
      fs.mkdirSync(npm);
      for (const name of fs.readdirSync(target))
        if (name.endsWith(".tgz")) fs.copyFileSync(path.join(target, name), path.join(npm, `${name.slice(0, -4)}.tar.gz`));
      if (fs.readdirSync(npm).length) commands.push(["dir", npm]);
      const binaries = path.join(temporary, "binaries");
      if (stageBinaryStrings(path.resolve(target), binaries)) commands.push(["dir", binaries]);
    }
    if (historyRange) commands.push(["git", path.resolve(target), `--log-opts=${historyRange}`]);
    for (let index = 0; index < commands.length; index++) {
      const report = path.join(temporary, `report-${index}.json`);
      const ignores = commands[index][0] === "git" ? historyIgnores : noIgnores;
      const result = spawnSync(scanner, [...commands[index], "--config", config, "--redact=100", "--no-banner", "--no-color", "--ignore-gitleaks-allow", "--gitleaks-ignore-path", ignores, "--max-archive-depth=4", "--max-decode-depth=5", "--report-format=json", "--report-path", report], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
      // Scanner output and match snippets may contain private data even when redacted.
      // Emit only rule counts; reports are temporary and never become public CI artifacts.
      if (result.status !== 0) {
        const findings = fs.existsSync(report) ? JSON.parse(fs.readFileSync(report, "utf8")) : [];
        const counts = {};
        for (const finding of findings) counts[finding.RuleID] = (counts[finding.RuleID] || 0) + 1;
        throw new Error(findings.length ? `Content check failed (${findings.length} findings): ${JSON.stringify(counts)}` : "Content scanner failed; no private diagnostics were emitted");
      }
    }
    console.log(`Public ${mode} content passed Gitleaks ${scannerVersion}${historyRange ? " and history checks" : ""}.`);
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    checkContent(...process.argv.slice(2));
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
