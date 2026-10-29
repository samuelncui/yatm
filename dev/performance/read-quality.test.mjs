import assert from "node:assert/strict";
import test from "node:test";
import { compareReadQuality, validateReadQuality } from "./read-quality.mjs";

function sample(id, estimateNS, extra = {}) {
  return { id, family: "clustered", backend: "tar", split: "development", seed: 1,
    input: { fixture: id },
    inputSHA256: "a".repeat(64), modelSHA256: "b".repeat(64), status: "complete",
    baseline: { estimateNS: 1000, uncertaintyNS: 10 }, retained: { estimateNS, uncertaintyNS: 10 },
    best: { estimateNS, uncertaintyNS: 10 }, plannedNS: 20, termination: "bounded local minimum",
    selectedFiles: 2, selectedBytes: 20, populationFiles: 10, populationBytes: 100,
    route: [2, 1], logicalRoute: [1, 2], sensitivity: ["locate", "longitudinal", "read"].flatMap((factor) =>
      [0.5, 1, 2].map((multiplier) => ({ factor, multiplier, baselineNS: 1000, retainedNS: estimateNS }))), ...extra };
}
function report(cases) {
  return { format: 1, complete: true, generatorVersion: "v1", profile: "development", inventory: cases.map((row) => row.id), cases };
}

test("quality reports retain every deterministic regression independently of aggregate gains", () => {
  const before = report([sample("win", 500), sample("loss", 500), sample("tie", 500)]);
  const after = report([sample("win", 100), sample("loss", 501), sample("tie", 500)]);
  const result = compareReadQuality(before, after);
  assert.deepEqual(result.regressions, ["loss"]);
  assert.deepEqual(result.families.map(({ improved, unchanged, regressed }) => [improved, unchanged, regressed]), [[1, 1, 1]]);
  assert.equal(result.families[0].worstCase, "loss");
  assert.equal(result.families[0].absoluteNS.median, 0);
  assert.equal(result.results[1].deltaNS, 1, "no 10% timing allowance applies to model cost");
  assert.deepEqual(result.results[1].candidate.route, [2, 1], "worst-case evidence remains reconstructable");
});

test("best found, adopted cost, computation cost and exact optimum gaps remain separate", () => {
  const before = report([sample("a", 600)]);
  const after = report([sample("a", 550, { best: { estimateNS: 400, uncertaintyNS: 200 }, plannedNS: 100,
    exact: { unconstrainedNS: 350, eligibleNS: 450 } })]);
  const row = compareReadQuality(before, after).results[0];
  assert.equal(row.deltaNS, -50);
  assert.equal(row.totalDeltaNS, 30, "a better route can cost more after planning");
  assert.equal(row.exactGapNS, 200);
  assert.equal(row.eligibleGapNS, 100);
  assert.equal(row.candidate.best.estimateNS, 400);
  assert.equal(row.candidate.sensitivity[0].retainedNS, 550);
  assert.equal(row.baselineLogicalDeltaNS, -400);
  assert.equal(row.candidateLogicalDeltaNS, -450);
  assert.equal(row.bestRetainedGapNS, 150);
});

test("complete budget-limited routes remain comparable and missing models stay explicit", () => {
  const before = report([sample("budget", 500), sample("unknown", 0, { status: "no-model", baseline: null, retained: null, best: null })]);
  const after = report([sample("budget", 400, { status: "budget", termination: "planning budget expired" }), before.cases[1]]);
  const result = compareReadQuality(before, after);
  assert.deepEqual(result.excluded, ["unknown"]);
  assert.deepEqual(result.budgetTerminated, ["budget"]);
  assert.equal(result.families[0].improved, 1);
  assert.equal(result.results[1].deltaNS, null);
});

test("inventory, input, model and LOGICAL mismatches invalidate the comparison", () => {
  const before = report([sample("a", 500)]);
  for (const change of [
    (value) => { value.inventory.push("missing"); },
    (value) => { value.cases.push(value.cases[0]); value.inventory.push("a"); },
    (value) => { value.cases[0].inputSHA256 = "c".repeat(64); },
    (value) => { value.cases[0].modelSHA256 = "c".repeat(64); },
    (value) => { value.cases[0].baseline.estimateNS++; },
    (value) => { value.profile = "acceptance"; },
    (value) => { value.cases[0].route = [1, 1]; },
    (value) => { value.cases[0].retained.estimateNS = -1; },
    (value) => { value.complete = false; },
    (value) => { value.cases[0].sensitivity.pop(); },
    (value) => { value.cases[0].sensitivity[0].baselineNS++; },
  ]) {
    const after = structuredClone(before); change(after);
    assert.throws(() => compareReadQuality(before, after), /quality|route|sensitivity/i);
  }
  assert.throws(() => validateReadQuality(report([])), /Incomplete/);
});

test("fixed references report a signed cost gap without rejecting a candidate", () => {
  const before = report([sample("frozen", 900, { referenceNS: 700 })]);
  const after = report([sample("frozen", 800, { referenceNS: 700 })]);
  assert.deepEqual(compareReadQuality(before, after).referenceGaps, ["frozen"]);
  after.cases[0].retained.estimateNS = 700;
  assert.deepEqual(compareReadQuality(before, after).referenceGaps, []);
});

test("failed reports retain complete inputs and partial routes without becoming accepted evidence", () => {
  const value = report([sample("failed", 500, { status: "failed", route: [2], termination: "", retained: null, best: null, sensitivity: [] }),
    sample("not-run", 500, { status: "not-run", route: [], termination: "" })]);
  value.complete = false;
  assert.throws(() => validateReadQuality(value), /Incomplete/);
  assert.equal(validateReadQuality(value, { allowIncomplete: true }).cases[0].input.fixture, "failed");
  value.complete = true;
  assert.throws(() => validateReadQuality(value, { allowIncomplete: true }), /Unexecuted/);
  const failed = report([value.cases[0]]);
  assert.deepEqual(compareReadQuality(report([sample("failed", 500)]), failed).failures, ["failed"]);
});

test("policy names, nominal losses and parameter tradeoffs never filter valid routes", () => {
  for (const policy of ["complete-nominal-cost", "nominal-and-six-variants", "experimental-search", "variants-unavailable"]) {
    const before = report([sample("case", 500)]);
    const value = sample("case", 1100, { admissionPolicy: policy, planned: true });
    value.sensitivity[0].retainedNS = 400;
    value.sensitivity[2].retainedNS = 1400;
    const after = report([value]);
    assert.equal(validateReadQuality(after).cases[0], value);
    const result = compareReadQuality(before, after);
    assert.deepEqual(result.regressions, ["case"]);
    assert.equal(result.results[0].deltaNS, 600);
    assert.equal(result.results[0].candidateLogicalDeltaNS, 100);
    assert.equal(result.results[0].variantDeltas[0].deltaNS, -100);
    assert.equal(result.results[0].variantDeltas[1].deltaNS, 900);
    assert.deepEqual(result.excluded, []);
  }
});

test("LOGICAL ties in the core workload remain ordinary comparison results", () => {
  for (const backend of ["tar", "ltfs"]) {
    const value = sample(`${backend}/frozen/restore1p5TB`, 1000,
      { backend, admissionPolicy: "complete-nominal-cost", planned: false, route: [1, 2] });
    const result = compareReadQuality(report([value]), report([value]));
    assert.equal(result.summary.unchanged, 1);
    assert.deepEqual(result.regressions, []);
    assert.deepEqual(result.referenceGaps, []);
    assert.deepEqual(result.excluded, []);
  }
});

test("nominal and independently constrained exact references retain their stated scope", () => {
  for (const admissibility of ["complete-nominal-cost", "logical-reference-envelope"]) {
    const before = report([sample("case", 500)]);
    const after = report([sample("case", 550, {
      exact: { unconstrainedNS: 400, eligibleNS: 450, admissibility },
    })]);
    const row = compareReadQuality(before, after).results[0];
    assert.equal(row.exactGapNS, 150);
    assert.equal(row.eligibleGapNS, 100);
    assert.equal(row.eligibleGapScope, admissibility);
  }
});

test("focused evidence and invalid report metadata cannot masquerade as full comparable results", () => {
  const focused = report([sample("focused", 500)]); focused.profile = "regressions";
  assert.equal(validateReadQuality(focused).profile, "regressions");
  assert.throws(() => compareReadQuality(report([sample("focused", 500)]), focused), /profiles/);
  assert.throws(() => validateReadQuality(report([sample("bad-policy", 500, { admissionPolicy: 123 })])), /metadata/);
});
