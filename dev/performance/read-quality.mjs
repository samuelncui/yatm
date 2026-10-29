import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const statuses = new Set(["complete", "no-model", "budget", "skipped", "failed", "not-run"]);
const number = (value) => Number.isSafeInteger(value) && value >= 0;
const hash = (value) => /^[a-f0-9]{64}$/.test(value ?? "");

function cost(value) {
  return value && number(value.estimateNS) && number(value.uncertaintyNS);
}

// Quality evidence is metadata-only. Its inventory prevents missing or skipped cases becoming wins.
export function validateReadQuality(value, { allowIncomplete = false } = {}) {
  if (value?.format !== 1 || (value.complete !== true && !allowIncomplete) || !value.generatorVersion || !["development", "acceptance", "all", "regressions"].includes(value.profile)
    || !Array.isArray(value.inventory) || !value.inventory.length || !Array.isArray(value.cases)) throw new Error("Incomplete read quality report");
  const ids = value.cases.map((row) => row.id);
  if (new Set(ids).size !== ids.length || new Set(value.inventory).size !== value.inventory.length
    || JSON.stringify([...ids].sort()) !== JSON.stringify([...value.inventory].sort())) throw new Error("Read quality case inventory differs");
  for (const row of value.cases) {
    if (!row.id || !row.family || !["tar", "ltfs"].includes(row.backend) || !row.split || !hash(row.inputSHA256)
      || !hash(row.modelSHA256) || !statuses.has(row.status) || !number(row.plannedNS)
      || !row.input || typeof row.input !== "object") throw new Error(`Invalid read quality identity or input: ${row.id}`);
    if (row.status === "not-run" && value.complete) throw new Error(`Unexecuted read quality case: ${row.id}`);
    // Failed reports retain identity and full inputs even when execution stopped before scoring.
    if (["failed", "not-run", "skipped"].includes(row.status)) continue;
    if (!row.termination) throw new Error(`Missing read quality termination: ${row.id}`);
    if (!number(row.selectedFiles) || !number(row.selectedBytes) || !number(row.populationFiles) || !number(row.populationBytes)
      || row.selectedFiles > row.populationFiles || row.selectedBytes > row.populationBytes) throw new Error(`Invalid read selection: ${row.id}`);
    const route = row.route, logical = row.logicalRoute;
    if (!Array.isArray(route) || !Array.isArray(logical) || logical.length !== row.selectedFiles || route.length !== logical.length
      || new Set(logical).size !== logical.length || new Set(route).size !== route.length
      || JSON.stringify([...route].sort((a, b) => a - b)) !== JSON.stringify([...logical].sort((a, b) => a - b))) throw new Error(`Incomplete read route: ${row.id}`);
    if (row.status === "no-model") continue;
    if (!cost(row.baseline) || !cost(row.retained) || !cost(row.best)) throw new Error(`Missing read route cost: ${row.id}`);
    if (row.referenceNS !== undefined && !number(row.referenceNS)) throw new Error(`Invalid read quality reference: ${row.id}`);
    if (row.exact && (!number(row.exact.unconstrainedNS)
      || (row.exact.eligibleNS !== null && row.exact.eligibleNS !== undefined && !number(row.exact.eligibleNS)))) throw new Error(`Invalid exact read reference: ${row.id}`);
    if (row.admissionPolicy !== undefined && typeof row.admissionPolicy !== "string") throw new Error(`Invalid read policy metadata: ${row.id}`);
    const sensitivity = row.sensitivity ?? [];
    if (sensitivity.length !== 9 || new Set(sensitivity.map((sample) => `${sample.factor}/${sample.multiplier}`)).size !== 9) throw new Error(`Incomplete read sensitivity: ${row.id}`);
    for (const sample of sensitivity) {
      if (!["locate", "longitudinal", "read"].includes(sample.factor) || ![0.5, 1, 2].includes(sample.multiplier)
        || !number(sample.baselineNS) || !number(sample.retainedNS)) throw new Error(`Invalid read sensitivity: ${row.id}`);
    }
  }
  return value;
}

export function readQuality(directory, name, options) {
  return validateReadQuality(JSON.parse(readFileSync(resolve(directory, name), "utf8")), options);
}

// These percentiles describe the fixed case population, not timing confidence intervals.
function distribution(values) {
  if (!values.length) return null;
  const sorted = values.toSorted((a, b) => a - b);
  const percentile = (fraction) => {
    const index = (sorted.length - 1) * fraction, lower = Math.floor(index), upper = Math.ceil(index);
    return sorted[lower] + (sorted[upper] - sorted[lower]) * (index - lower);
  };
  return { median: percentile(0.5), p90: percentile(0.9), worst: sorted.at(-1) };
}

export function compareReadQuality(baseline, candidate) {
  validateReadQuality(baseline); validateReadQuality(candidate);
  if (baseline.generatorVersion !== candidate.generatorVersion || baseline.profile !== candidate.profile
    || JSON.stringify([...baseline.inventory].sort()) !== JSON.stringify([...candidate.inventory].sort())) throw new Error("Read quality profiles or inventories differ");
  const old = new Map(baseline.cases.map((row) => [row.id, row]));
  const results = candidate.cases.map((after) => {
    const before = old.get(after.id);
    for (const field of ["family", "backend", "seed", "split", "inputSHA256", "modelSHA256", "selectedFiles", "selectedBytes", "populationFiles", "populationBytes", "referenceNS"]) {
      if (before[field] !== after[field]) throw new Error(`Read quality ${field} differs: ${after.id}`);
    }
    if (JSON.stringify(before.baseline) !== JSON.stringify(after.baseline)
      || JSON.stringify(before.logicalRoute) !== JSON.stringify(after.logicalRoute)) throw new Error(`Read quality LOGICAL reference differs: ${after.id}`);
    if (JSON.stringify(before.input) !== JSON.stringify(after.input)) throw new Error(`Read quality full input differs: ${after.id}`);
    if (before.sensitivity?.length === 9 && after.sensitivity?.length === 9) {
      const reference = new Map(before.sensitivity.map((sample) => [`${sample.factor}/${sample.multiplier}`, sample.baselineNS]));
      if (after.sensitivity.some((sample) => sample.baselineNS !== reference.get(`${sample.factor}/${sample.multiplier}`))) {
        throw new Error(`Read quality sensitivity LOGICAL reference differs: ${after.id}`);
      }
    }
    const measured = [before, after].every((row) => ["complete", "budget"].includes(row.status) && cost(row.retained));
    const deltaNS = measured ? after.retained.estimateNS - before.retained.estimateNS : null;
    const change = measured && before.retained.estimateNS !== 0 ? deltaNS / before.retained.estimateNS : null;
    const excluded = !measured;
    // Retain every parameter tradeoff without using it to admit or reject a measured route.
    const previousVariants = new Map((before.sensitivity ?? []).filter((sample) => sample.multiplier !== 1)
      .map((sample) => [`${sample.factor}/${sample.multiplier}`, sample.retainedNS]));
    const variantDeltas = (after.sensitivity ?? []).filter((sample) => sample.multiplier !== 1).map((sample) => ({
      factor: sample.factor, multiplier: sample.multiplier,
      deltaNS: previousVariants.has(`${sample.factor}/${sample.multiplier}`)
        ? sample.retainedNS - previousVariants.get(`${sample.factor}/${sample.multiplier}`) : null }));
    return { id: after.id, family: after.family, backend: after.backend, split: after.split,
      status: excluded ? "excluded" : deltaNS < 0 ? "improved" : deltaNS > 0 ? "regressed" : "unchanged",
      budgetTerminated: before.status === "budget" || after.status === "budget",
      referenceDeltaNS: measured && number(after.referenceNS) ? after.retained.estimateNS - after.referenceNS : null,
      variantDeltas, deltaNS, change, totalDeltaNS: measured ? deltaNS + after.plannedNS - before.plannedNS : null,
      baselineLogicalDeltaNS: cost(before.baseline) && cost(before.retained) ? before.retained.estimateNS - before.baseline.estimateNS : null,
      candidateLogicalDeltaNS: cost(after.baseline) && cost(after.retained) ? after.retained.estimateNS - after.baseline.estimateNS : null,
      bestRetainedGapNS: cost(after.best) && cost(after.retained) ? after.retained.estimateNS - after.best.estimateNS : null,
      exactGapNS: !number(after.exact?.unconstrainedNS) || !measured ? null : after.retained.estimateNS - after.exact.unconstrainedNS,
      eligibleGapNS: !number(after.exact?.eligibleNS) || !measured ? null : after.retained.estimateNS - after.exact.eligibleNS,
      eligibleGapScope: after.exact?.admissibility ?? "legacy-admissible-reference",
      logicalReferenceEnvelopeGapNS: after.exact?.admissibility === "logical-reference-envelope" && measured
        ? after.retained.estimateNS - after.exact.eligibleNS : null,
      baseline: before, candidate: after };
  });
  const groups = new Map();
  for (const row of results) {
    const key = `${row.backend}/${row.family}`, rows = groups.get(key) ?? [];
    rows.push(row); groups.set(key, rows);
  }
  const summarize = (rows) => ({ cases: rows.length,
    improved: rows.filter((row) => row.status === "improved").length,
    unchanged: rows.filter((row) => row.status === "unchanged").length,
    regressed: rows.filter((row) => row.status === "regressed").length,
    budgetTerminated: rows.filter((row) => row.budgetTerminated).length,
    excluded: rows.filter((row) => row.status === "excluded").length,
    absoluteNS: distribution(rows.filter((row) => row.status !== "excluded" && row.deltaNS !== null).map((row) => row.deltaNS)),
    relative: distribution(rows.filter((row) => row.status !== "excluded" && row.change !== null).map((row) => row.change)),
    exactGapNS: distribution(rows.filter((row) => row.status !== "excluded" && row.exactGapNS !== null).map((row) => row.exactGapNS)),
    eligibleGapNS: distribution(rows.filter((row) => row.status !== "excluded" && row.eligibleGapNS !== null).map((row) => row.eligibleGapNS)),
    baselineLogicalDeltaNS: distribution(rows.filter((row) => row.status !== "excluded" && row.baselineLogicalDeltaNS !== null).map((row) => row.baselineLogicalDeltaNS)),
    candidateLogicalDeltaNS: distribution(rows.filter((row) => row.status !== "excluded" && row.candidateLogicalDeltaNS !== null).map((row) => row.candidateLogicalDeltaNS)),
    bestRetainedGapNS: distribution(rows.filter((row) => row.status !== "excluded" && row.bestRetainedGapNS !== null).map((row) => row.bestRetainedGapNS)),
    totalDeltaNS: distribution(rows.filter((row) => row.status !== "excluded" && row.totalDeltaNS !== null).map((row) => row.totalDeltaNS)),
    worstCase: rows.filter((row) => row.status !== "excluded" && row.deltaNS !== null).toSorted((a, b) => b.deltaNS - a.deltaNS)[0]?.id ?? null });
  const families = [...groups].map(([family, rows]) => ({ family, ...summarize(rows) }));
  // Performance differences are review inputs. Only missing or invalid evidence prevents comparison.
  return { format: 1, generatorVersion: candidate.generatorVersion, profile: candidate.profile,
    percentileScope: "fixed cases; not timing confidence intervals", sensitivityScope: "synthetic one-factor rescoring; not device evidence",
    regressions: results.filter((row) => row.status === "regressed").map((row) => row.id),
    referenceGaps: results.filter((row) => row.referenceDeltaNS > 0).map((row) => row.id),
    failures: results.filter((row) => row.baseline.status === "failed" || row.candidate.status === "failed").map((row) => row.id),
    excluded: results.filter((row) => row.status === "excluded").map((row) => row.id),
    budgetTerminated: results.filter((row) => row.budgetTerminated).map((row) => row.id),
    summary: summarize(results), families, results };
}
