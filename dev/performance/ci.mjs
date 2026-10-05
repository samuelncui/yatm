import { appendFileSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

// Baselines are source revisions, remeasured alongside the candidate on this runner.
export function revisions(eventName, event, accepted) {
  const pr = eventName === "pull_request";
  const baseline = pr ? event.pull_request?.base?.sha : accepted;
  const candidate = pr ? event.pull_request?.head?.sha : event.inputs?.candidate;
  const tier = pr ? "fast" : event.inputs?.tier ?? "release";
  for (const [name, value] of [["baseline", baseline], ["candidate", candidate]]) if (!/^[a-f0-9]{40}$/.test(value ?? "")) throw new Error(`A full immutable ${name} commit is required${name === "baseline" && !pr ? "; set PERFORMANCE_ACCEPTED_BASELINE only after explicit review" : ""}`);
  if (!["fast", "critical", "release"].includes(tier)) throw new Error("Unknown performance tier");
  if (!pr && tier === "fast") throw new Error("Release-baseline dispatch requires critical or release evidence");
  return { baseline, candidate, tier };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, "utf8"));
    event.inputs = { candidate: process.env.PERF_CANDIDATE || event.inputs?.candidate, tier: process.env.PERF_TIER || event.inputs?.tier };
    const selected = revisions(process.env.GITHUB_EVENT_NAME, event, process.env.PERFORMANCE_ACCEPTED_BASELINE);
    for (const [key, value] of Object.entries(selected)) appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${value}\n`);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
