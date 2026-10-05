import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync, writeSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import process from "node:process";
import test from "node:test";
import { collect, options } from "./compare-performance.mjs";
import { digest } from "../../dev/performance/evidence.mjs";

const benchmark = "src/components/files-browser.bench.ts";
const tracked = ["frontend/package.json", "frontend/pnpm-lock.yaml", "frontend/src/source.ts", `frontend/${benchmark}`];

function fixture(t) {
  const root = realpathSync(mkdtempSync(join(tmpdir(), "yatm-frontend-tools-")));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const config = options([
    "--baseline",
    join(root, "baseline"),
    "--candidate",
    join(root, "candidate"),
    "--harness",
    join(root, "harness"),
    "--out",
    join(root, "evidence"),
  ]);
  for (const side of ["baseline", "candidate", "harness"]) {
    mkdirSync(join(config[side], "frontend/src/components"), { recursive: true });
    for (const name of tracked) writeFileSync(join(config[side], name), `${side} ${name}\n`);
    // Git metadata, ignored dependencies and local credentials must never enter a snapshot.
    writeFileSync(join(config[side], ".git"), "synthetic Git metadata\n");
    writeFileSync(join(config[side], "frontend/.npmrc"), "private registry configuration\n");
    writeFileSync(join(config[side], "frontend/.env.local"), "private environment\n");
    writeFileSync(join(config[side], "frontend/untracked.ts"), "untracked source\n");
    mkdirSync(join(config[side], "frontend/node_modules"));
    writeFileSync(join(config[side], "frontend/node_modules/private"), "existing dependencies\n");
  }
  const calls = [];
  const spawn = (file, args, settings) => {
    calls.push({ file, args, settings });
    const ok = (stdout = "") => ({ status: 0, stdout, stderr: "" });
    if (file === "git") {
      if (args.includes("--show-toplevel")) return ok(args[1]);
      if (args.includes("rev-parse")) return ok("a".repeat(40));
      if (args.includes("--error-unmatch")) return ok(`frontend/${benchmark}`);
      assert.ok(args.includes("--cached"));
      assert.ok(!args.includes("--others"));
      return ok(tracked.join("\0") + "\0");
    }
    assert.equal(file, "pnpm");
    if (args[0] === "--version") return ok("10.10.0");
    const side = basename(join(settings.cwd, ".."));
    if (args[1] === "node") return ok(process.execPath);
    writeSync(settings.stdio[1], `${side} stdout\n`);
    writeSync(settings.stdio[2], `${side} stderr\n`);
    if (args[0] === "install") {
      assert.ok(args.includes("--frozen-lockfile"));
      assert.equal(readFileSync(join(settings.cwd, benchmark), "utf8"), `harness frontend/${benchmark}\n`);
      assert.equal(readFileSync(join(settings.cwd, "src/source.ts"), "utf8"), `${side} frontend/src/source.ts\n`);
      for (const path of ["../.git", ".npmrc", ".env.local", "untracked.ts", "node_modules"]) assert.equal(existsSync(join(settings.cwd, path)), false);
      assert.notEqual(settings.env.HOME, process.env.HOME);
      assert.equal(readFileSync(settings.env.npm_config_userconfig, "utf8"), "");
      mkdirSync(join(settings.cwd, "node_modules/vitest"), { recursive: true });
      writeFileSync(join(settings.cwd, "node_modules/vitest/package.json"), JSON.stringify({ version: "4.1.11" }));
      return ok();
    }
    assert.equal(args[1], "vitest");
    assert.equal(calls.filter((call) => call.args[0] === "install").length, 2, "both installs finish before measuring");
    const report = {
      files: [
        {
          filepath: join(settings.cwd, benchmark),
          groups: [{ fullName: benchmark, benchmarks: [{ id: "case-0", name: "directory listing", sampleCount: 1, mean: 1, hz: 1000 }] }],
        },
      ],
    };
    writeFileSync(args.find((arg) => arg.startsWith("--outputJson=")).slice("--outputJson=".length), JSON.stringify(report));
    return ok();
  };
  return { root, config, calls, spawn };
}

test("preserves supplied source and private files while comparing the common harness once per side", (t) => {
  const f = fixture(t);
  const record = collect(f.config, f.spawn);
  assert.equal(record.complete, true);
  const benches = f.calls.filter((call) => call.args[1] === "vitest");
  assert.equal(benches.length, 2);
  assert.deepEqual(
    benches.map((call) => basename(join(call.settings.cwd, ".."))),
    ["baseline", "candidate"],
  );
  assert.deepEqual(benches[0].args, ["exec", "vitest", "bench", "--run", "--maxWorkers=1", benchmark, `--outputJson=${join(f.config.out, "baseline.json")}`]);
  assert.deepEqual(benches[1].args, [
    ...benches[0].args.slice(0, -1),
    `--outputJson=${join(f.config.out, "candidate.json")}`,
    `--compare=${join(f.config.out, "baseline.json")}`,
  ]);
  for (const side of ["baseline", "candidate"]) {
    assert.equal(record.sources[side].lockfileSHA256, digest(readFileSync(join(f.config[side], "frontend/pnpm-lock.yaml"))));
    assert.equal(record.outputs[side].sha256, digest(readFileSync(join(f.config.out, `${side}.json`))));
    for (const name of tracked) assert.equal(readFileSync(join(f.config[side], name), "utf8"), `${side} ${name}\n`);
    assert.equal(readFileSync(join(f.config.out, `${side}.log`), "utf8"), `${side} stdout\n${side} stderr\n`);
    assert.equal(readFileSync(join(f.config[side], "frontend/.env.local"), "utf8"), "private environment\n");
  }
  assert.notEqual(record.sources.baseline.sha256, record.sources.candidate.sha256);
  assert.equal(record.harness.sha256, digest(readFileSync(join(f.config.harness, `frontend/${benchmark}`))));
  assert.equal(
    readdirSync(f.config.out).some((name) => name.startsWith(".work-")),
    false,
  );
});

test("rejects unsafe output before commands or writes, including symlink aliases and existing evidence", (t) => {
  const f = fixture(t);
  mkdirSync(f.config.out);
  writeFileSync(join(f.config.out, "keep"), "prior evidence");
  symlinkSync(f.config.candidate, join(f.root, "alias"), "dir");
  symlinkSync(join(f.root, "absent"), join(f.root, "dangling"));
  for (const out of [
    f.config.out,
    ...["baseline", "candidate", "harness"].map((side) => join(f.config[side], "new-output")),
    join(f.root, "alias/new-output"),
    join(f.root, "dangling"),
  ]) {
    assert.throws(() => collect({ ...f.config, out }, f.spawn), /new directory|outside all/);
  }
  assert.equal(f.calls.length, 0);
  assert.equal(readFileSync(join(f.config.out, "keep"), "utf8"), "prior evidence");
});

test("rejects tracked symlinks and symlinked source parents without copying their targets", async (t) => {
  for (const parent of [false, true])
    await t.test(parent ? "parent" : "file", (t) => {
      const f = fixture(t);
      const target = join(f.config.baseline, parent ? "frontend/src" : "frontend/src/source.ts");
      rmSync(target, { recursive: true });
      symlinkSync(join(f.config.candidate, parent ? "frontend/src" : "frontend/src/source.ts"), target);
      assert.throws(() => collect(f.config, f.spawn), /without symlinks/);
      assert.equal(
        f.calls.some((call) => call.args[0] === "install"),
        false,
      );
      assert.equal(
        readdirSync(f.config.out).some((name) => name.startsWith(".work-")),
        false,
      );
    });
});

test("failed install or benchmark retains raw output and incomplete evidence, and removes only staging", async (t) => {
  for (const phase of ["install", "vitest"])
    await t.test(phase, (t) => {
      const f = fixture(t);
      assert.throws(
        () =>
          collect(f.config, (file, args, settings) => {
            if (file === "pnpm" && (args[0] === phase || args[1] === phase)) {
              writeSync(settings.stdio[1], "partial output\n");
              writeSync(settings.stdio[2], "failure detail\n");
              return { status: 9 };
            }
            return f.spawn(file, args, settings);
          }),
        /exit 9/,
      );
      const record = JSON.parse(readFileSync(join(f.config.out, "pair.json"), "utf8"));
      assert.equal(record.complete, false);
      assert.equal(record.commands.at(-1).status, 9);
      assert.equal(readFileSync(join(f.config.out, record.commands.at(-1).log), "utf8"), "partial output\nfailure detail\n");
      assert.equal(
        readdirSync(f.config.out).some((name) => name.startsWith(".work-")),
        false,
      );
      assert.equal(readFileSync(join(f.config.candidate, "frontend/src/source.ts"), "utf8"), "candidate frontend/src/source.ts\n");
      assert.equal(
        f.calls.some((call) => call.args.some((arg) => arg.startsWith("--compare="))),
        false,
      );
    });
});

test("missing, empty, skipped or mismatched results cannot mark a comparison complete", async (t) => {
  for (const outcome of ["missing", "empty", "skipped", "mismatched"])
    await t.test(outcome, (t) => {
      const f = fixture(t);
      assert.throws(
        () =>
          collect(f.config, (file, args, settings) => {
            const result = f.spawn(file, args, settings);
            if (args[1] !== "vitest") return result;
            const json = args.find((arg) => arg.startsWith("--outputJson=")).slice("--outputJson=".length);
            const report = JSON.parse(readFileSync(json, "utf8"));
            if (outcome === "missing") rmSync(json);
            if (outcome === "empty") writeFileSync(json, '{"files":[]}');
            if (outcome === "skipped") {
              report.files[0].groups[0].benchmarks[0].sampleCount = 0;
              writeFileSync(json, JSON.stringify(report));
            }
            if (outcome === "mismatched" && args.some((arg) => arg.startsWith("--compare="))) {
              report.files[0].groups[0].benchmarks[0].id = "unmatched-case";
              writeFileSync(json, JSON.stringify(report));
            }
            return result;
          }),
        /ENOENT|Missing or invalid|identities differ/,
      );
      assert.equal(JSON.parse(readFileSync(join(f.config.out, "pair.json"), "utf8")).complete, false);
      assert.equal(
        readdirSync(f.config.out).some((name) => name.startsWith(".work-")),
        false,
      );
      assert.equal(f.calls.filter((call) => call.args[1] === "vitest").length, outcome === "mismatched" ? 2 : 1);
    });
});
