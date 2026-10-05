import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { collect, options, validateTier } from "./collect.mjs";
import { digest, readSamples, validateInventory, validatePair, validateSamples } from "./evidence.mjs";
import { comparePair } from "../check-performance.mjs";

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), "yatm-performance-tools-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const side of ["baseline", "candidate"]) {
    mkdirSync(join(root, side, "internal/example"), { recursive: true });
    writeFileSync(join(root, side, "go.mod"), "module example.test/project\n\ngo 1.26.8\n");
    writeFileSync(join(root, side, "internal/example/source.go"), `package example // ${side}\n`);
    writeFileSync(join(root, side, "internal/example/old_test.go"), "removed harness\n");
    writeFileSync(join(root, side, "internal/example/performance_test.go"), `common harness from ${side}\n`);
  }
  const tier = { harness: ["internal/example/performance_test.go"], suites: [{ package: "./internal/example", bench: "^BenchmarkList$", benchmarks: ["BenchmarkList"] }] };
  const manifest = join(root, "suites.json"); writeFileSync(manifest, JSON.stringify({ tiers: { fast: tier } }));
  const config = options(["--baseline", join(root, "baseline"), "--candidate", join(root, "candidate"), "--out", join(root, "evidence"), "--environment", "isolated-test-runner", "--manifest", manifest, "--cgo", "0", "--idle"]);
  const calls = [];
  const run = (file, args, settings = {}) => {
    calls.push({ file, args, settings });
    if (file === "git") {
      if (args.includes("rev-parse")) return "a".repeat(40) + "\n";
      if (args.includes("status")) return " M internal/example/source.go\n";
      return "go.mod\0internal/example/source.go\0internal/example/old_test.go\0internal/example/performance_test.go\0";
    }
    if (basename(file) === "go") {
      if (args[0] === "env") return JSON.stringify({ GOVERSION: "go1.26.8", GOROOT: "/synthetic/go", GOOS: "linux", GOARCH: "amd64", CGO_ENABLED: "0" });
      assert.equal(args[0], "test");
      assert.equal(settings.env.GOTOOLCHAIN, "local");
      assert.equal(settings.env.GOWORK, "off");
      assert.equal(settings.env.GOENV, "off");
      assert.equal(settings.env.GOFLAGS, "-mod=readonly");
      assert.equal(readFileSync(join(settings.cwd, "internal/example/performance_test.go"), "utf8"), "common harness from candidate\n");
      assert.equal(existsSync(join(settings.cwd, "internal/example/old_test.go")), false);
      assert.equal(calls.some((call) => call.file.endsWith(".test")), false, "compile both sides before measurement");
      return "";
    }
    assert.equal(basename(settings.cwd), "example", "testdata resolves in the package directory");
    assert.ok(args.includes("-test.count=1"));
    return "goos: linux\ngoarch: amd64\ncpu: synthetic test CPU\nBenchmarkList-2 1 100 ns/op 0 B/op 0 allocs/op\nPASS\n";
  };
  return { root, config, tier, calls, run };
}

test("runs each Go benchmark once per source with identical harness and owned cleanup", (t) => {
  const f = fixture(t);
  const record = collect(f.config, f.run, () => {});
  assert.equal(record.complete, true);
  assert.notEqual(record.sources.baseline.sha256, record.sources.candidate.sha256);
  assert.deepEqual(record.runs.map((run) => run.side), ["baseline", "candidate"]);
  assert.equal(record.runs.length, 2);
  assert.equal(record.suites[0].benchtime, "1s");
  for (const call of f.calls.filter((call) => call.file.endsWith(".test"))) assert.ok(call.args.includes("-test.benchtime=1s"));
  assert.equal(readdirSync(f.root).some((name) => name.startsWith(".yatm-performance-")), false);
  assert.equal(validatePair(f.config.out).complete, true);
  assert.equal(readFileSync(join(f.root, "baseline/internal/example/old_test.go"), "utf8"), "removed harness\n");
  const result = comparePair(f.config.out);
  assert.equal(result.flagged, false);
  writeFileSync(join(f.config.out, "candidate.bench"), "replaced evidence");
  assert.throws(() => validatePair(f.config.out), /changed/);
});

test("comparison CLI retains visible review flags while invalid evidence still fails", (t) => {
  const f = fixture(t);
  collect(f.config, (file, args, settings) => {
    const output = f.run(file, args, settings);
    return basename(file) === "candidate-0.test" ? output.replace("100 ns/op", "150 ns/op") : output;
  }, () => {});
  const compare = () => spawnSync(process.execPath, [fileURLToPath(new URL("../check-performance.mjs", import.meta.url)), f.config.out], {
    encoding: "utf8", env: { ...process.env, GITHUB_ACTIONS: "true" },
  });
  const result = compare();
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /flagged workloads require review/);
  assert.match(result.stdout, /::warning::Benchmark increases require review/);
  const report = JSON.parse(readFileSync(join(f.config.out, "comparison.json"), "utf8"));
  assert.equal(report.flagged, true);
  assert.equal("passed" in report, false, "completed measurements do not grant acceptance");
  writeFileSync(join(f.config.out, "candidate.bench"), "incomplete output");
  assert.notEqual(compare().status, 0);
});

test("Scan uses identical fresh-operation args while List retains duration calibration", (t) => {
  // Exercise both suite policies in one collection using synthetic command results only.
  const f = fixture(t);
  const release = JSON.parse(readFileSync(new URL("./suites.json", import.meta.url), "utf8")).tiers.release;
  const scan = release.suites.find((suite) => suite.package === "./internal/executor/scan");
  const harness = release.harness.filter((name) => name.startsWith("internal/executor/scan/"));
  f.tier.suites.push(scan);
  f.tier.harness.push(...harness);
  f.tier.structural = release.structural.filter((check) => check.package === scan.package);
  f.config.tier = "release";
  f.config.benchtime = "250ms";
  writeFileSync(f.config.manifest, JSON.stringify({ tiers: { release: f.tier } }));
  for (const side of ["baseline", "candidate"]) {
    mkdirSync(join(f.root, side, "internal/executor/scan"), { recursive: true });
    for (const name of harness) writeFileSync(join(f.root, side, name), `${name} from ${side}\n`);
  }
  const record = collect(f.config, (file, args, settings) => {
    if (file === "git" && args.includes("ls-files")) return f.run(file, args, settings) + harness.join("\0") + "\0";
    if (basename(file) === "go" && args[0] === "test") {
      for (const name of harness) assert.equal(readFileSync(join(settings.cwd, name), "utf8"), `${name} from candidate\n`);
    }
    if (file.endsWith("-1.test")) {
      f.calls.push({ file, args, settings });
      if (args.some((arg) => arg.includes("TestScanInputMillionStructural"))) {
        assert.equal(basename(file), "candidate-1.test");
        assert.equal(settings.env.YATM_SCAN_PERF_MILLION, "1");
        if (args[0].startsWith("-test.list=")) return "TestScanInputMillionStructural\n";
        return "=== RUN   TestScanInputMillionStructural\n--- PASS: TestScanInputMillionStructural (0.00s)\nPASS\n";
      }
      assert.equal(settings.env.YATM_SCAN_PERF_MILLION, undefined, "the structural opt-in is not a timed fixture setting");
      return "goos: linux\ngoarch: amd64\ncpu: synthetic test CPU\n" + scan.benchmarks.map((name) =>
        `${name}-2 1 100 ns/op 0 B/op 0 allocs/op 20 create-ns/op 70 complete-ns/op 10 page-ns/op\n`).join("") + "PASS\n";
    }
    return f.run(file, args, settings);
  }, () => {});

  // Effective policies are recorded per suite and applied unchanged to both sources.
  assert.equal(record.environment.benchtime, "250ms");
  assert.deepEqual(record.suites.map((suite) => suite.benchtime), ["250ms", "1x"]);
  assert.equal(record.runs.length, 4);
  for (const [index, suite] of record.suites.entries()) {
    const calls = f.calls.filter((call) => call.file.endsWith(`-${index}.test`) && call.args.includes("-test.run=^$"));
    assert.equal(calls.length, 2);
    assert.ok(calls[0].args.includes(`-test.benchtime=${suite.benchtime}`));
    assert.ok(calls[0].args.includes("-test.count=1"));
    for (const call of calls) assert.deepEqual(call.args, calls[0].args);
    assert.deepEqual(record.runs.filter((run) => run.package === suite.package)
      .map((run) => run.side), ["baseline", "candidate"]);
  }
  assert.deepEqual(record.harness.files.map(([name]) => name), f.tier.harness);
  assert.equal(validatePair(f.config.out).complete, true);
  assert.equal(record.structural[0].scope, "candidate");
  assert.deepEqual(Object.keys(record.structural[0].outputs), ["candidate"]);
  const samples = readSamples(readFileSync(join(f.config.out, "candidate.bench"), "utf8"));
  for (const name of scan.benchmarks) {
    const rows = samples.samples.get(JSON.stringify([record.suites[1].importPath, `${name}-2`]));
    assert.equal(rows.length, 1);
    assert.equal(rows[0]["create-sec/op"], 20 / 1e9);
    assert.equal(rows[0]["complete-sec/op"], 70 / 1e9);
    assert.equal(rows[0]["page-sec/op"], 10 / 1e9);
  }

  const result = comparePair(f.config.out);
  assert.equal(result.investigationThreshold, 0.1);
  assert.equal(result.flagged, false);
  assert.equal(result.results.filter((row) => scan.metrics.includes(row.unit)).length, 15);
});

test("suite overrides accept only one fresh operation and reject invalid policies before collection", async (t) => {
  for (const benchtime of [null, "", 1, true, "0x", "2x", "10x", "1s", "1X", " 1x", "1x "]) {
    await t.test(JSON.stringify(benchtime), (t) => {
      const f = fixture(t);
      f.tier.suites[0].benchtime = benchtime;
      writeFileSync(f.config.manifest, JSON.stringify({ tiers: { fast: f.tier } }));
      assert.throws(() => collect(f.config, f.run, () => {}), /Suite benchtime override must be 1x/);
      assert.equal(f.calls.length, 0);
      assert.equal(existsSync(f.config.out), false);
    });
  }
  assert.throws(() => options(["--baseline", "a", "--candidate", "b", "--out", "c", "--environment", "d", "--idle", "--benchtime", "1x"]), /duration benchtime/);
});

test("missing Scan create, complete or page metrics cannot produce completed evidence on either side", async (t) => {
  const metrics = ["create-sec/op", "complete-sec/op", "page-sec/op"];
  for (const side of ["baseline", "candidate"]) for (const missing of metrics) {
    await t.test(`${side}/${missing}`, (t) => {
      const f = fixture(t);
      f.tier.suites[0].metrics = metrics;
      f.tier.suites[0].benchtime = "1x";
      writeFileSync(f.config.manifest, JSON.stringify({ tiers: { fast: f.tier } }));
      assert.throws(() => collect(f.config, (file, args, settings) => {
        const stdout = f.run(file, args, settings);
        if (!file.endsWith(".test")) return stdout;
        const present = metrics.filter((metric) => !file.endsWith(`${side}-0.test`) || metric !== missing);
        return stdout.replace("0 allocs/op", "0 allocs/op" + present.map((metric) => ` 10 ${metric.replace("sec/op", "ns/op")}`).join(""));
      }, () => {}), /missing required/);
      const record = JSON.parse(readFileSync(join(f.config.out, "pair.json"), "utf8"));
      assert.equal(record.complete, false);
      assert.ok(record.error.includes(missing));
      assert.throws(() => validatePair(f.config.out), /completed/);
    });
  }
});

test("every tier requires the complete List/Search and Scan inventories with the exact common harness", () => {
  const tiers = JSON.parse(readFileSync(new URL("./suites.json", import.meta.url), "utf8")).tiers;
  for (const [name, count] of [["fast", 10000], ["critical", 100000], ["release", 100000]]) {
    const tier = validateTier(tiers[name]);
    assert.deepEqual(tier.harness, [
      "internal/apis/files_performance_test.go", "internal/apis/files_performance_fixture_test.go",
      "internal/apis/files_list_test_helpers_test.go", "internal/executor/scan/input_fixture_test.go",
      "internal/executor/scan/input_performance_test.go",
    ]);
    assert.deepEqual(tier.suites.map((suite) => suite.package), ["./internal/apis", "./internal/executor/scan"]);
    const [files, scan] = tier.suites;
    assert.equal(files.benchtime, undefined);
    assert.deepEqual(files.metrics, ["first-sec/op"]);
    assert.deepEqual(files.benchmarks, ["BenchmarkFilesListComplete/mixed/random", "BenchmarkFilesListComplete/mixed/reverse",
      "BenchmarkFilesListRules/deep/random", "BenchmarkFilesSearchPage/first/random", "BenchmarkFilesSearchPage/next/random"]
      .map((benchmark) => `${benchmark}/${count}`));
    assert.equal(scan.benchtime, "1x");
    assert.deepEqual(scan.metrics, ["create-sec/op", "complete-sec/op", "page-sec/op"]);
    assert.deepEqual(scan.benchmarks, ["logical/1", "logical/3", "logical/9", "partial/1", "relocation/3"]
      .map((workload) => `BenchmarkScanInputManifest/${workload}/${count}`));

    // Match the Go selector's slash-separated components and fail if any required result is absent.
    for (const suite of tier.suites) {
      for (const benchmark of suite.benchmarks) {
        const parts = suite.bench.split("/").map((part) => new RegExp(part));
        assert.equal(parts.length, benchmark.split("/").length);
        assert.ok(benchmark.split("/").every((part, index) => parts[index].test(part)));
        assert.equal(parts.at(-1).test(String(count === 10000 ? 100000 : 10000)), false);
      }
    }
    const rows = scan.benchmarks.map((benchmark) => `${benchmark}-2 1 100 ns/op 0 B/op 0 allocs/op 20 create-ns/op 70 complete-ns/op 10 page-ns/op\n`);
    const header = "pkg: example.test/scan\ngoos: linux\ngoarch: amd64\ncpu: synthetic test CPU\n";
    const suites = [{ ...scan, importPath: "example.test/scan" }];
    assert.doesNotThrow(() => validateInventory(readSamples(header + rows.join("")), suites));
    for (let missing = 0; missing < rows.length; missing++) {
      assert.throws(() => validateInventory(readSamples(header + rows.filter((_, index) => index !== missing).join("")), suites), /inventory/);
    }
    assert.equal(tier.structural?.length ?? 0, name === "release" ? 2 : 0);
  }
  assert.deepEqual(tiers.release.structural, [
    { scope: "candidate", package: "./internal/apis", run: "^TestFilesListMillionStructural$",
      tests: ["TestFilesListMillionStructural"], env: { YATM_PERF_MILLION: "1" } },
    { scope: "candidate", package: "./internal/executor/scan", run: "^TestScanInputMillionStructural$",
      tests: ["TestScanInputMillionStructural"], env: { YATM_SCAN_PERF_MILLION: "1" } },
  ]);
});

test("records incomplete evidence and closes staging on a failed benchmark", (t) => {
  const f = fixture(t);
  assert.throws(() => collect(f.config, (file, args, settings) => {
    if (file.endsWith("candidate-0.test")) throw Object.assign(new Error("fixture incompatible with this implementation"), { stdout: "partial benchmark output", stderr: "fixture error" });
    return f.run(file, args, settings);
  }, () => {}), /incompatible/);
  assert.equal(JSON.parse(readFileSync(join(f.config.out, "pair.json"))).complete, false);
  assert.throws(() => validatePair(f.config.out), /completed/);
  assert.match(readFileSync(join(f.config.out, "failure.log"), "utf8"), /partial benchmark output\nfixture error/);
  assert.equal(readdirSync(f.root).some((name) => name.startsWith(".yatm-performance-")), false);
});

test("rejects missing metrics, subsets, skipped suites and mismatched environments", () => {
  const text = "pkg: p\ngoos: linux\ngoarch: amd64\ncpu: fixture\n" + "BenchmarkList-2 1 100 ns/op 0 B/op 0 allocs/op\n";
  const samples = readSamples(text), suites = [{ importPath: "p", benchmarks: ["BenchmarkList"] }];
  assert.doesNotThrow(() => validateSamples(samples, samples, suites));
  assert.throws(() => validateSamples(samples, samples, [{ ...suites[0], benchmarks: ["BenchmarkList", "BenchmarkAbsent"] }]), /inventory/);
  assert.throws(() => validateSamples(samples, readSamples(text.replace("cpu: fixture", "cpu: other")), suites), /environments/);
  assert.throws(() => validateSamples(samples, readSamples(text + text), suites), /one benchmark result/);
  assert.throws(() => readSamples(text.replaceAll("0 allocs/op", "")), /lacks/);
  assert.throws(() => readSamples("PASS\n"), /No benchmark/);
  const first = readSamples(text.replaceAll("0 allocs/op", "0 allocs/op 50 first-ns/op"));
  assert.equal([...first.samples.values()][0][0]["first-sec/op"], 50 / 1e9);
});

test("rejects unreviewed fixture paths, absent idle declaration and obsolete repetition options", (t) => {
  const f = fixture(t);
  assert.throws(() => validateTier({ ...f.tier, harness: ["internal/example/source.go"] }), /only selected/);
  assert.throws(() => options(["--baseline", "a", "--candidate", "b", "--out", "c", "--environment", "d"]), /idle/);
  assert.throws(() => options(["--baseline", "a", "--candidate", "b", "--out", "c", "--environment", "d", "--idle", "--samples", "9"]), /Unknown or incomplete option --samples/);
  mkdirSync(f.config.out);
  assert.throws(() => collect(f.config, f.run, () => {}), /never overwritten/);
});

function releaseFixture(t) {
  const f = fixture(t);
  const structural = [{ scope: "candidate", package: "./internal/example", run: "^TestMillionRows$", tests: ["TestMillionRows"], env: { YATM_PERF_SIZE: "1000000" } }];
  writeFileSync(f.config.manifest, JSON.stringify({ tiers: { release: { ...f.tier, structural } } }));
  f.config.tier = "release";
  return f;
}

test("release gates candidate correctness and retains identical paired timing requirements", (t) => {
  const f = releaseFixture(t);
  const record = collect(f.config, (file, args, settings) => {
    if (args.some((arg) => arg.includes("TestMillionRows"))) assert.equal(basename(file), "candidate-0.test", "the old baseline's completeness defect must not gate the fix");
    if (args.includes("-test.list=^TestMillionRows$")) return "TestMillionRows\n";
    if (args.includes("-test.run=^TestMillionRows$")) {
      assert.equal(settings.env.YATM_PERF_SIZE, "1000000");
      return "=== RUN   TestMillionRows\n--- PASS: TestMillionRows (1.00s)\nPASS\n";
    }
    return f.run(file, args, settings);
  }, () => {});
  assert.equal(record.structural[0].passed, true);
  assert.equal(record.structural[0].scope, "candidate");
  assert.deepEqual(Object.keys(record.structural[0].outputs), ["candidate"]);
  for (const side of ["baseline", "candidate"]) assert.equal(record.runs.filter((run) => run.side === side).length, 1);
  assert.equal(validatePair(f.config.out).complete, true);
  for (const change of [
    (value) => { delete value.structural[0].scope; },
    (value) => { value.structural[0].scope = "baseline"; },
    (value) => { value.structural[0].outputs.baseline = value.structural[0].outputs.candidate; },
    (value) => { delete value.structural[0].outputs.candidate; },
  ]) {
    const invalid = structuredClone(record); change(invalid);
    writeFileSync(join(f.config.out, "pair.json"), JSON.stringify(invalid));
    assert.throws(() => validatePair(f.config.out), /candidate correctness/);
  }
  writeFileSync(join(f.config.out, "pair.json"), JSON.stringify(record));
  writeFileSync(join(f.config.out, record.structural[0].outputs.candidate.file), "--- SKIP: TestMillionRows\nPASS\n");
  assert.throws(() => validatePair(f.config.out), /Structural evidence changed/);
});

test("missing, skipped or failing candidate structural evidence cannot pass release", async (t) => {
  for (const outcome of ["missing", "skipped", "failed"]) await t.test(outcome, (t) => {
    const f = releaseFixture(t);
    assert.throws(() => collect(f.config, (file, args, settings) => {
      if (args.includes("-test.list=^TestMillionRows$")) return outcome === "missing" ? "" : "TestMillionRows\n";
      if (args.includes("-test.run=^TestMillionRows$")) {
        if (outcome === "failed") throw new Error("candidate structural failure");
        return "--- SKIP: TestMillionRows\nPASS\n";
      }
      return f.run(file, args, settings);
    }, () => {}), /structural/);
    const record = JSON.parse(readFileSync(join(f.config.out, "pair.json")));
    assert.equal(record.complete, false);
    assert.equal(record.structural[0].scope, "candidate");
    assert.equal(record.structural[0].passed, false);
    assert.throws(() => validatePair(f.config.out), /completed/);
  });
});

test("load rejection leaves no accepted evidence and preserves prior results", (t) => {
  const f = fixture(t);
  assert.throws(() => collect(f.config, f.run, () => { throw new Error("host busy"); }), /host busy/);
  assert.equal(existsSync(f.config.out), false);
  assert.throws(() => collect({ ...f.config, out: join(f.config.candidate, "output") }, f.run, () => {}), /outside all/);
  symlinkSync(f.config.candidate, join(f.root, "alias"), "dir");
  assert.throws(() => collect({ ...f.config, out: join(f.root, "alias/new/output") }, f.run, () => {}), /outside all/);
});

test("unmatched output remains available and metadata cannot conceal a different toolchain target", (t) => {
  const f = fixture(t);
  assert.throws(() => collect(f.config, (file, args, settings) => file.endsWith(".test") ? "PASS\n" : f.run(file, args, settings), () => {}), /No benchmark/);
  assert.match(readFileSync(join(f.config.out, "baseline.bench"), "utf8"), /PASS/);
  rmSync(f.config.out, { recursive: true });
  const record = collect(f.config, f.run, () => {});
  record.environment.go.GOARCH = "other";
  writeFileSync(join(f.config.out, "pair.json"), JSON.stringify(record));
  assert.throws(() => validatePair(f.config.out), /recorded toolchain/);
});

test("snapshot rejects source symlinks including dangling links", async (t) => {
  for (const kind of ["existing", "dangling"]) await t.test(kind, (t) => {
    const f = fixture(t), path = join(f.config.candidate, "internal/example/source.go");
    rmSync(path);
    symlinkSync(kind === "existing" ? join(f.config.baseline, "internal/example/source.go") : "missing-source.go", path);
    assert.throws(() => collect(f.config, f.run, () => {}), /ordinary files, not symlinks/);
    assert.equal(JSON.parse(readFileSync(join(f.config.out, "pair.json"))).complete, false);
  });
});

function packageFixture(t) {
  const f = fixture(t);
  const extra = "internal/other/performance_test.go";
  f.tier.harness.push(extra);
  f.tier.suites.push({ package: "./internal/other", bench: "^BenchmarkOther$", benchmarks: ["BenchmarkOther"], benchtime: "1x" });
  f.tier.structural = f.tier.suites.map((suite, index) => ({ scope: "candidate", package: suite.package,
    run: `^TestRows${index}$`, tests: [`TestRows${index}`] }));
  f.config.tier = "release";
  writeFileSync(f.config.manifest, JSON.stringify({ tiers: { release: f.tier } }));
  for (const side of ["baseline", "candidate"]) {
    mkdirSync(join(f.root, side, "internal/other"), { recursive: true });
    writeFileSync(join(f.root, side, extra), `other harness from ${side}\n`);
  }
  const original = f.run;
  f.run = (file, args, settings = {}) => {
    if (file === "git" && args.includes("ls-files")) return original(file, args, settings) + extra + "\0";
    if (basename(file) === "go" && args[0] === "test") {
      f.calls.push({ file, args, settings });
      const pkg = args.at(-1).replace(/^\.\//, "");
      const expected = pkg.endsWith("other") ? "other harness from candidate\n" : "common harness from candidate\n";
      assert.equal(readFileSync(join(settings.cwd, pkg, "performance_test.go"), "utf8"), expected);
      assert.equal(f.calls.some((call) => call.file.endsWith(".test")), false);
      return "";
    }
    if (file.endsWith(".test")) {
      f.calls.push({ file, args, settings });
      const list = /^-test.list=\^(TestRows\d+)\$$/.exec(args[0]);
      if (list) return list[1] + "\n";
      const structural = /^-test.run=\^(TestRows\d+)\$$/.exec(args[0]);
      if (structural) return `--- PASS: ${structural[1]} (0.00s)\nPASS\n`;
      const benchmark = basename(settings.cwd) === "other" ? "BenchmarkOther" : "BenchmarkList";
      return `goos: linux\ngoarch: amd64\ncpu: synthetic test CPU\n${benchmark}-2 1 100 ns/op 0 B/op 0 allocs/op\nPASS\n`;
    }
    return original(file, args, settings);
  };
  return f;
}

test("package selection filters an existing tier, harness and applicable structural checks", (t) => {
  const f = packageFixture(t);
  f.config.packages = ["./internal/other", "./internal/other"];
  const record = collect(f.config, (file, args, settings) => {
    if (args.includes("-test.run=^$")) {
      const state = join(settings.env.TMPDIR, "mutable-fixture");
      assert.equal(existsSync(state), false, "the baseline fixture cannot leak into the candidate");
      writeFileSync(state, "owned by this source");
    }
    return f.run(file, args, settings);
  }, () => {});
  assert.deepEqual(record.scope, { requestedPackages: ["./internal/other"], selectedPackages: ["./internal/other"],
    executedPackages: ["./internal/other"], skippedPackages: ["./internal/example"], fullTier: false,
    structural: [{ package: "./internal/other", tests: ["TestRows1"] }],
    skippedStructural: [{ package: "./internal/example", tests: ["TestRows0"] }] });
  assert.equal(record.fullReleaseComparison, false, "a completed subset cannot claim release comparison");
  assert.deepEqual(record.harness.files.map(([name]) => name), ["internal/other/performance_test.go"]);
  assert.deepEqual(record.runs.map(({ side, package: pkg }) => [side, pkg]), [["baseline", "./internal/other"], ["candidate", "./internal/other"]]);
  const benchmarks = f.calls.filter((call) => call.args.includes("-test.run=^$"));
  assert.equal(benchmarks.length, 2);
  assert.deepEqual(benchmarks[0].args, benchmarks[1].args);
  assert.ok(benchmarks[0].args.includes("-test.count=1"));
  assert.ok(benchmarks[0].args.includes("-test.benchtime=1x"));
  assert.notEqual(benchmarks[0].settings.env.TMPDIR, benchmarks[1].settings.env.TMPDIR);
  assert.equal(validatePair(f.config.out).fullReleaseComparison, false);
  assert.equal(comparePair(f.config.out).flagged, false);

  for (const change of [
    (value) => { value.fullReleaseComparison = true; },
    (value) => { value.scope.fullTier = true; },
    (value) => { value.scope.executedPackages = []; },
    (value) => { value.structural = []; },
    (value) => { delete value.scope; },
  ]) {
    const invalid = structuredClone(record); change(invalid);
    writeFileSync(join(f.config.out, "pair.json"), JSON.stringify(invalid));
    assert.throws(() => validatePair(f.config.out), /scope|structural/);
  }
});

test("explicit package requests retain manifest order and complete default release scope", (t) => {
  const f = packageFixture(t);
  f.config.packages = ["./internal/other", "./internal/example"];
  const record = collect(f.config, f.run, () => {});
  assert.deepEqual(record.scope.requestedPackages, f.config.packages);
  assert.deepEqual(record.scope.executedPackages, ["./internal/example", "./internal/other"]);
  assert.equal(record.fullReleaseComparison, true);
  assert.equal(validatePair(f.config.out).scope.fullTier, true);
  const compile = f.calls.filter((call) => call.args.includes("-c"));
  const benchmarks = f.calls.filter((call) => call.args.includes("-test.run=^$"));
  assert.equal(compile.length, 4);
  assert.equal(benchmarks.length, 4);
  for (const call of benchmarks) {
    const side = basename(call.file).split("-")[0], pkg = "./internal/" + basename(call.settings.cwd);
    assert.equal(call.settings.env.TMPDIR, compile.find((item) => basename(item.settings.cwd) === side && item.args.at(-1) === pkg).settings.env.TMPDIR);
  }
});

test("unknown or incomplete package requests fail before source or fixture setup", (t) => {
  const f = packageFixture(t);
  assert.throws(() => collect({ ...f.config, packages: ["./internal/absent"] }, f.run, () => {}), /not in the release tier/);
  assert.equal(f.calls.length, 0);
  assert.equal(existsSync(f.config.out), false);
  const args = ["--baseline", "a", "--candidate", "b", "--out", "c", "--environment", "d", "--idle"];
  assert.deepEqual(options([...args, "--package", "./internal/apis", "--package", "./internal/executor/scan"]).packages,
    ["./internal/apis", "./internal/executor/scan"]);
  assert.throws(() => options([...args, "--package"]), /incomplete option --package/);
});

test("collection records attempted package scope on failure and hashes the consumed immutable manifest", (t) => {
  const f = packageFixture(t);
  f.config.packages = ["./internal/other"];
  const manifest = readFileSync(f.config.manifest);
  assert.throws(() => collect(f.config, (file, args, settings) => {
    if (basename(file) === "go" && args[0] === "env") writeFileSync(f.config.manifest, "changed after selection");
    if (file.endsWith("baseline-0.test")) throw new Error("selected benchmark failed");
    return f.run(file, args, settings);
  }, () => {}), /selected benchmark failed/);
  const record = JSON.parse(readFileSync(join(f.config.out, "pair.json")));
  assert.equal(record.collector.manifestSHA256, digest(manifest));
  assert.deepEqual(record.scope.executedPackages, ["./internal/other"]);
  assert.equal(record.complete, false);
  assert.equal(record.fullReleaseComparison, false);
  assert.equal(readdirSync(f.root).some((name) => name.startsWith(".yatm-performance-")), false);
});
