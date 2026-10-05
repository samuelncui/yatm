import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const metrics = ["sec/op", "B/op", "allocs/op"];
export const digest = (value) => createHash("sha256").update(value).digest("hex");

// Package and full benchmark name are the identity; missing metrics never become zero.
export function readSamples(text) {
  const samples = new Map(), headers = new Map();
  let pkg;
  for (const line of text.split(/\r?\n/)) {
    const header = /^(goos|goarch|cpu|pkg): (.+)$/.exec(line);
    if (header) {
      if (header[1] === "pkg") pkg = header[2];
      else {
        if (headers.has(header[1]) && headers.get(header[1]) !== header[2]) throw new Error(`Mixed ${header[1]} in one input`);
        headers.set(header[1], header[2]);
      }
      continue;
    }
    const match = /^(Benchmark\S+)\s+(\d+)\s+(.+)$/.exec(line);
    if (!match) continue;
    if (!pkg || Number(match[2]) < 1) throw new Error("Benchmark lacks package or iterations");
    const values = match[3].trim().split(/\s+/), row = new Map();
    if (values.length % 2) throw new Error(`Malformed benchmark sample: ${match[1]}`);
    for (let index = 0; index < values.length; index += 2) {
      const value = Number(values[index]), rawUnit = values[index + 1], unit = rawUnit.replace(/(^|-)ns\/op$/, "$1sec/op");
      if (!Number.isFinite(value) || value < 0 || row.has(unit)) throw new Error(`Invalid ${unit} sample`);
      row.set(unit, rawUnit !== unit ? value / 1e9 : value);
    }
    for (const unit of metrics) if (!row.has(unit)) throw new Error(`${match[1]} lacks ${unit}`);
    const key = JSON.stringify([pkg, match[1]]);
    const rows = samples.get(key) ?? [];
    rows.push(Object.fromEntries(row)); samples.set(key, rows);
  }
  if (!samples.size) throw new Error("No benchmark samples; a skipped or unmatched suite is not evidence");
  for (const key of ["goos", "goarch", "cpu"]) if (!headers.has(key)) throw new Error(`Missing ${key} benchmark environment`);
  return { samples, headers: Object.fromEntries([...headers].sort()) };
}

export function validateInventory(result, suites) {
  const expected = suites.flatMap((suite) => suite.benchmarks.map((name) => JSON.stringify([suite.importPath, name]))).sort();
  const actual = [...result.samples.keys()].map((key) => {
    const [pkg, name] = JSON.parse(key);
    return JSON.stringify([pkg, name.replace(/-\d+$/, "")]);
  }).sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error("Benchmark inventory differs from the required suite");
  for (const [key, rows] of result.samples) {
    if (rows.length !== 1) throw new Error(`${key}: expected one benchmark result, got ${rows.length}`);
    const [pkg] = JSON.parse(key), suite = suites.find((value) => value.importPath === pkg);
    for (const row of rows) for (const metric of suite.metrics ?? []) if (!(metric in row)) throw new Error(`${key}: missing required ${metric}`);
  }
}

export function validateSamples(base, candidate, suites) {
  if (JSON.stringify(base.headers) !== JSON.stringify(candidate.headers)) throw new Error("Benchmark environments differ");
  validateInventory(base, suites);
  validateInventory(candidate, suites);
  if (JSON.stringify([...base.samples.keys()].sort()) !== JSON.stringify([...candidate.samples.keys()].sort())) throw new Error("Baseline/candidate benchmark identities differ");
}

export function validatePair(directory) {
  const record = JSON.parse(readFileSync(resolve(directory, "pair.json"), "utf8"));
  if (record.format !== 1 || record.complete !== true || !record.idleConfirmed || !record.environment?.label || !record.environment?.go?.GOVERSION || !record.environment?.host?.platform || !record.harness?.sha256 || !record.suites?.length) throw new Error("Missing completed comparable collection metadata");
  if (record.fullReleaseComparison !== undefined && !record.scope) throw new Error("Missing recorded package scope");
  if (record.scope) {
    const packages = record.suites.map((suite) => suite.package), scope = record.scope;
    if (JSON.stringify(scope.selectedPackages) !== JSON.stringify(packages) || JSON.stringify(scope.executedPackages) !== JSON.stringify(packages)
      || !scope.requestedPackages?.length || JSON.stringify([...scope.requestedPackages].sort()) !== JSON.stringify([...packages].sort())
      || !Array.isArray(scope.skippedPackages) || scope.skippedPackages.some((pkg) => packages.includes(pkg))
      || scope.fullTier !== (scope.skippedPackages.length === 0)
      || record.fullReleaseComparison !== (record.tier === "release" && scope.fullTier)) throw new Error("Recorded package scope differs from executed comparison");
    const checks = (record.structural ?? []).map(({ package: pkg, tests }) => ({ package: pkg, tests }));
    if (JSON.stringify(scope.structural) !== JSON.stringify(checks)) throw new Error("Required selected structural checks did not finish");
  }
  for (const side of ["baseline", "candidate"]) {
    if (!/^[a-f0-9]{40,64}$/.test(record.sources?.[side]?.commit ?? "") || !/^[a-f0-9]{64}$/.test(record.sources[side].sha256 ?? "")) throw new Error(`Missing recorded ${side} source identity`);
    const contents = readFileSync(resolve(directory, `${side}.bench`));
    if (digest(contents) !== record.outputs?.[side]) throw new Error(`${side} evidence was changed or belongs to another collection`);
  }
  const base = readSamples(readFileSync(resolve(directory, "baseline.bench"), "utf8"));
  const candidate = readSamples(readFileSync(resolve(directory, "candidate.bench"), "utf8"));
  validateSamples(base, candidate, record.suites);
  if (base.headers.goos !== record.environment.go.GOOS || base.headers.goarch !== record.environment.go.GOARCH) throw new Error("Benchmark environment differs from recorded toolchain");
  for (const check of record.structural ?? []) {
    if (!check.passed || !check.outputs || !check.tests?.length) throw new Error("Required structural checks did not finish");
    if (check.scope !== "candidate" || JSON.stringify(Object.keys(check.outputs)) !== '["candidate"]') throw new Error("Structural evidence must explicitly cover only candidate correctness");
    if (digest(readFileSync(resolve(directory, check.outputs.candidate.file))) !== check.outputs.candidate.sha256) throw new Error("Structural evidence changed");
  }
  if (record.tier === "release" && record.scope?.fullTier !== false && !record.structural?.length) throw new Error("Release evidence requires the 1m structural checks");
  return record;
}
