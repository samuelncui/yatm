import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { metrics, readSamples, validatePair, validateSamples } from "./performance/evidence.mjs";
import { compareReadQuality, readQuality } from "./performance/read-quality.mjs";

// A measured increase flags a workload for investigation; it is not a confidence estimate.
export function evaluateComparison(baseline, candidate, suites) {
  validateSamples(baseline, candidate, suites);
  const results = [];
  for (const [key, [before]] of baseline.samples) {
    const [pkg, benchmark] = JSON.parse(key), [after] = candidate.samples.get(key);
    const suite = suites.find((value) => value.importPath === pkg);
    for (const unit of new Set([...metrics, ...(suite.metrics ?? []), ...(suite.optionalMetrics ?? [])])) {
      const base = before[unit] ?? null, current = after[unit] ?? null;
      if (base === null || current === null) {
        results.push({ package: pkg, benchmark, unit, baseline: base, candidate: current, change: null, status: "unavailable" });
        continue;
      }
      results.push({ package: pkg, benchmark, unit, baseline: base, candidate: current,
        change: base === 0 ? (current === 0 ? 0 : null) : current / base - 1,
        status: current <= base * 1.10 ? "pass" : "investigate" });
    }
  }
  return { flagged: results.some((row) => row.status === "investigate"), investigationThreshold: 0.1, results };
}

export function comparePair(directory) {
  const evidence = validatePair(directory);
  const baseline = readSamples(readFileSync(resolve(directory, "baseline.bench"), "utf8"));
  const candidate = readSamples(readFileSync(resolve(directory, "candidate.bench"), "utf8"));
  const result = { sources: evidence.sources, structural: evidence.structural,
    ...evaluateComparison(baseline, candidate, evidence.suites) };
  if (evidence.quality?.length) {
    result.quality = evidence.quality.map((check) => compareReadQuality(readQuality(directory, check.outputs.baseline.file), readQuality(directory, check.outputs.candidate.file)));
    result.rawRouteRegressions = result.quality.flatMap((report) => report.regressions);
    result.routeReferenceGaps = result.quality.flatMap((report) => report.referenceGaps);
    result.routeBudgetTerminations = result.quality.flatMap((report) => report.budgetTerminated);
    result.failedRouteCases = result.quality.flatMap((report) => report.failures);
  }
  writeFileSync(resolve(directory, "comparison.json"), JSON.stringify(result, null, 2) + "\n");
  return result;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 3) throw new Error("Usage: node dev/check-performance.mjs PAIR_DIRECTORY (created by dev/performance/collect.mjs)");
    const result = comparePair(resolve(process.argv[2]));
    for (const row of result.results) {
      const change = row.status === "unavailable" ? "not compared: metric unavailable" : row.change === null ? "increase from zero" : `${(row.change * 100).toFixed(1)}%`;
      console.log(`${row.status}: ${row.benchmark} ${row.unit}: ${row.baseline} -> ${row.candidate} (${change})`);
    }
    console.log(`Benchmark comparison complete: ${result.flagged ? "flagged workloads require review" : "no increases above 10%"}; ${result.results.length} metrics`);
    if (result.flagged && process.env.GITHUB_ACTIONS === "true") {
      console.log("::warning::Benchmark increases require review before accepting this change; see comparison.json and raw measurements.");
    }
    if (result.quality) {
      for (const report of result.quality) for (const family of report.families) {
        console.log(`read quality ${family.family}: ${family.improved} improved, ${family.unchanged} unchanged, ${family.regressed} regressed, ${family.excluded} excluded`);
      }
      if (result.rawRouteRegressions.length) {
        console.log(`Higher modeled route costs for review: ${result.rawRouteRegressions.join(", ")}`);
      }
      if (result.routeReferenceGaps.length) {
        console.log(`Modeled costs above fixed references for review: ${result.routeReferenceGaps.join(", ")}`);
      }
      if (result.routeBudgetTerminations.length) {
        console.log(`Complete routes retained at the planning budget: ${result.routeBudgetTerminations.join(", ")}`);
      }
      if (result.failedRouteCases.length) {
        console.error(`Failed read quality cases: ${result.failedRouteCases.join(", ")}`);
        process.exitCode = 1;
      }
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
