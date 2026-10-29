import { appendFileSync, readFileSync, readdirSync, existsSync } from "node:fs";
import { resolve, join } from "node:path";
import { fileURLToPath } from "node:url";

// Baselines are source revisions, remeasured alongside the candidate on this runner.
export function revisions(eventName, event, accepted) {
  const pr = eventName === "pull_request";
  const baseline = pr ? event.pull_request?.base?.sha : accepted;
  const candidate = pr ? event.pull_request?.head?.sha : event.inputs?.candidate;
  const tier = pr ? "fast" : event.inputs?.tier ?? "release";
  for (const [name, value] of [["baseline", baseline], ["candidate", candidate]]) if (!/^[a-f0-9]{40}$/.test(value ?? "")) throw new Error(`A full immutable ${name} commit is required${name === "baseline" && !pr ? "; set PERFORMANCE_ACCEPTED_BASELINE only after explicit review" : ""}`);
  if (!["fast", "critical", "release", "read-fast", "read", "read-resources"].includes(tier)) throw new Error("Unknown performance tier");
  if (!pr && tier === "fast") throw new Error("Release-baseline dispatch requires critical or release evidence");
  return { baseline, candidate, tier };
}

// Compare already checked-out bytes; the scope check needs neither another checkout nor network access.
export function readPlannerChanged(baseline, candidate) {
  const roots = ["internal/media", "internal/executor/restore", "dev/performance", "e2e/fixtures/restore-1p5tb-v1"];
  const names = new Set(["dev/check-performance.mjs", ".github/workflows/performance.yml"]);
  const collect = (source, directory) => {
    if (!existsSync(join(source, directory))) return;
    for (const entry of readdirSync(join(source, directory), { withFileTypes: true })) {
      const name = `${directory}/${entry.name}`;
      if (entry.isDirectory()) collect(source, name);
      else if (entry.isFile()) names.add(name);
    }
  };
  for (const source of [baseline, candidate]) for (const directory of roots) collect(source, directory);
  return [...names].some((name) => {
    const before = join(baseline, name), after = join(candidate, name);
    if (existsSync(before) !== existsSync(after)) return true;
    return existsSync(before) && !readFileSync(before).equals(readFileSync(after));
  });
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv[2] === "read-affected") {
      if (process.argv.length !== 5) throw new Error("Usage: ci.mjs read-affected BASELINE CANDIDATE");
      appendFileSync(process.env.GITHUB_OUTPUT, `read=${readPlannerChanged(resolve(process.argv[3]), resolve(process.argv[4]))}\n`);
    } else {
      const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
      event.inputs = { candidate: process.env.PERF_CANDIDATE || event.inputs?.candidate, tier: process.env.PERF_TIER || event.inputs?.tier };
      const selected = revisions(process.env.GITHUB_EVENT_NAME, event, process.env.PERFORMANCE_ACCEPTED_BASELINE);
      for (const [key, value] of Object.entries(selected)) appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
    }
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
