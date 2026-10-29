import assert from "node:assert/strict";
import test from "node:test";
import { evaluateComparison } from "./check-performance.mjs";
import { readSamples } from "./performance/evidence.mjs";

const suites = [{ importPath: "example/list", benchmarks: ["BenchmarkList"], metrics: ["first-sec/op"] }];
const output = (time = 100, memory = 100, allocations = 10, first = 50) =>
  `goos: linux\ngoarch: amd64\npkg: example/list\ncpu: fixture\nBenchmarkList-2 100 ${time} ns/op ${memory} B/op ${allocations} allocs/op ${first} first-ns/op\nPASS\n`;
const check = (before, after) => evaluateComparison(readSamples(before), readSamples(after), suites);

test("one standard Go benchmark result compares absolute values without a statistical gate", () => {
  const result = check(output(), output(108));
  assert.equal(result.flagged, false);
  assert.equal(result.results.length, 4);
  assert.equal(result.results[0].baseline, 100 / 1e9);
  assert.equal(result.results[0].candidate, 108 / 1e9);
  assert.ok(Math.abs(result.results[0].change - 0.08) < 1e-12);
  assert.equal("confidence" in result, false);
});

test("flags each measured time or allocation increase without hiding it in an aggregate", () => {
  for (const after of [output(120), output(100, 120), output(100, 100, 12), output(100, 100, 10, 60)]) {
    const result = check(output(), after);
    assert.equal(result.flagged, true);
    assert.equal(result.results.filter((row) => row.status === "investigate").length, 1);
  }
  assert.equal(check(output(), output(90, 90, 9, 40)).flagged, false);
});

test("handles zero allocations without inventing a percentage or treating new allocations as zero", () => {
  assert.equal(check(output(100, 0, 0), output(100, 0, 0)).flagged, false);
  const result = check(output(100, 0, 0), output(100, 1, 0));
  assert.equal(result.flagged, true);
  assert.equal(result.results.find((row) => row.unit === "B/op").change, null);
});

test("missing, repeated, mismatched or invalid results are not comparisons", () => {
  assert.throws(() => check(output(), output().replace("50 first-ns/op", "")), /missing required/);
  assert.throws(() => check(output(), output() + output()), /one benchmark result/);
  assert.throws(() => check(output(), output().replace("BenchmarkList", "BenchmarkOther")), /inventory/);
  assert.throws(() => check(output(), output().replace("cpu: fixture", "cpu: other")), /environments/);
  assert.throws(() => check(output(), output().replace("100 ns/op", "NaN ns/op")), /Invalid/);
  assert.throws(() => check(output(), "PASS\n"), /No benchmark/);
});


test("optional diagnostic metrics preserve unavailable counts and compare only measured pairs", () => {
  const optionalSuites = [{ ...suites[0], optionalMetrics: ["total-evaluations/op"] }];
  const measured = (count) => output().replace(" first-ns/op", ` first-ns/op ${count} total-evaluations/op`);
  const compare = (before, after) => evaluateComparison(readSamples(before), readSamples(after), optionalSuites);
  const missing = compare(output(), measured(20));
  assert.equal(missing.flagged, false);
  assert.deepEqual(missing.results.at(-1), { package: "example/list", benchmark: "BenchmarkList-2", unit: "total-evaluations/op", baseline: null, candidate: 20, change: null, status: "unavailable" });
  assert.equal(compare(measured(20), output()).results.at(-1).candidate, null);
  assert.equal(compare(measured(0), measured(0)).results.at(-1).status, "pass");
  assert.equal(compare(measured(20), measured(24)).results.at(-1).status, "investigate");
});
