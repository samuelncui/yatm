import assert from "node:assert/strict";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, symlinkSync, writeFileSync, writeSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import process from "node:process";
import test from "node:test";
import { collect, options } from "./compare-performance.mjs";
import { digest } from "../../dev/performance/evidence.mjs";

const benchmarks = ["src/components/files-browser.bench.ts", "src/components/job-result-window.bench.ts"];
const caseNames = [
  ["stream and accumulate 10k directory rows in 500-row batches"],
  [1_000, 10_000, 100_000, 1_000_000].map((size) => `append ${size} Job result rows in 200-row windows`),
];
const tracked = ["frontend/package.json", "frontend/pnpm-lock.yaml", "frontend/src/source.ts", ...benchmarks.map((name) => `frontend/${name}`)];

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
      if (args.includes("--error-unmatch")) return ok(args.at(-1));
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
      for (const benchmark of benchmarks) assert.equal(readFileSync(join(settings.cwd, benchmark), "utf8"), `harness frontend/${benchmark}\n`);
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
      files: benchmarks.map((benchmark, fileIndex) => ({
        filepath: join(settings.cwd, benchmark),
        groups: [
          {
            fullName: benchmark,
            benchmarks: caseNames[fileIndex].map((name, caseIndex) => ({
              id: `file-${fileIndex}-case-${caseIndex}`,
              name,
              sampleCount: 1,
              mean: 1,
              hz: 1000,
            })),
          },
        ],
      })),
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
  assert.deepEqual(benches[0].args, [
    "exec",
    "vitest",
    "bench",
    "--run",
    "--maxWorkers=1",
    ...benchmarks,
    `--outputJson=${join(f.config.out, "baseline.json")}`,
  ]);
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
  assert.deepEqual(
    record.harness.files.map(([name, , hash]) => [name, hash]),
    benchmarks.map((name) => [`frontend/${name}`, digest(readFileSync(join(f.config.harness, `frontend/${name}`)))]),
  );
  assert.equal(record.harness.sha256, digest(JSON.stringify(record.harness.files)));
  assert.equal(record.outputs.baseline.cases.length, 5);
  assert.deepEqual(record.outputs.baseline.cases, record.outputs.candidate.cases);
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

test("missing, invalid, failed, duplicate or mismatched cases cannot complete a comparison", async (t) => {
  const mutations = {
    missing: (_report, json) => rmSync(json),
    empty: (report) => {
      report.files = [];
    },
    "missing file": (report) => {
      report.files.pop();
    },
    "unexpected file": (report) => {
      report.files[1].filepath = "src/other.bench.ts";
    },
    "duplicate file": (report) => {
      report.files[1] = report.files[0];
    },
    "missing case": (report) => {
      report.files[1].groups[0].benchmarks.pop();
    },
    "extra case": (report) => {
      report.files[1].groups[0].benchmarks.push({ id: "extra", name: "extra" });
    },
    "duplicate name": (report) => {
      report.files[1].groups[0].benchmarks[1].name = report.files[1].groups[0].benchmarks[0].name;
    },
    "duplicate id": (report) => {
      report.files[1].groups[0].benchmarks[0].id = report.files[0].groups[0].benchmarks[0].id;
    },
    "unexpected case": (report) => {
      report.files[1].groups[0].benchmarks[0].name = "unmaintained workload";
    },
    "failed file": (report) => {
      report.files[1].error = "collection failure";
    },
    "failed group": (report) => {
      report.files[1].groups[0].error = "hook failure";
    },
    "failed case": (report) => {
      report.files[1].groups[0].benchmarks[0].error = "assertion failure";
    },
    skipped: (report) => {
      report.files[1].groups[0].benchmarks[0].sampleCount = 0;
    },
    "invalid samples": (report) => {
      report.files[1].groups[0].benchmarks[0].sampleCount = 1.5;
    },
    "invalid mean": (report) => {
      report.files[1].groups[0].benchmarks[0].mean = null;
    },
    "invalid rate": (report) => {
      report.files[1].groups[0].benchmarks[0].hz = 0;
    },
    "missing id": (report) => {
      delete report.files[1].groups[0].benchmarks[0].id;
    },
    mismatched: (report) => {
      report.files[1].groups[0].benchmarks[0].id = "unmatched-case";
    },
  };
  for (const [outcome, mutate] of Object.entries(mutations))
    await t.test(outcome, (t) => {
      const f = fixture(t);
      assert.throws(
        () =>
          collect(f.config, (file, args, settings) => {
            const result = f.spawn(file, args, settings);
            if (args[1] !== "vitest" || (outcome === "mismatched" && !args.some((arg) => arg.startsWith("--compare=")))) return result;
            const json = args.find((arg) => arg.startsWith("--outputJson=")).slice("--outputJson=".length);
            const report = JSON.parse(readFileSync(json, "utf8"));
            mutate(report, json);
            if (outcome !== "missing") writeFileSync(json, JSON.stringify(report));
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

test("compares identities independent of native file and case report order", (t) => {
  const f = fixture(t);
  const record = collect(f.config, (file, args, settings) => {
    const result = f.spawn(file, args, settings);
    if (args.some((arg) => arg.startsWith("--compare="))) {
      const json = args.find((arg) => arg.startsWith("--outputJson=")).slice("--outputJson=".length);
      const report = JSON.parse(readFileSync(json, "utf8"));
      report.files.reverse();
      for (const file of report.files) file.groups[0].benchmarks.reverse();
      writeFileSync(json, JSON.stringify(report));
    }
    return result;
  });
  assert.equal(record.complete, true);
  assert.deepEqual(record.outputs.baseline.cases, record.outputs.candidate.cases);
});

test("requires both tracked harness files before installing dependencies", (t) => {
  const f = fixture(t);
  rmSync(join(f.config.harness, `frontend/${benchmarks[1]}`));
  assert.throws(() => collect(f.config, f.spawn), /Missing tracked harness/);
  assert.equal(
    f.calls.some((call) => call.args[0] === "install"),
    false,
  );
  assert.equal(JSON.parse(readFileSync(join(f.config.out, "pair.json"), "utf8")).complete, false);
});
