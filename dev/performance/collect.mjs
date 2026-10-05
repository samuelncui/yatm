import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, statfsSync, writeFileSync, appendFileSync } from "node:fs";
import { cpus, loadavg, platform, release, totalmem } from "node:os";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { resolveGo } from "./toolchain.mjs";
import { digest, readSamples, validateInventory, validateSamples } from "./evidence.mjs";

export function options(args) {
  const result = { tier: "fast", cpu: 2, benchtime: "1s", cgo: "1" };
  for (let index = 0; index < args.length; index++) {
    const key = args[index].replace(/^--/, "");
    if (key === "idle") { result.idle = true; continue; }
    if (!["baseline", "candidate", "harness", "out", "tier", "cpu", "benchtime", "cgo", "environment", "manifest"].includes(key) || !args[index + 1] || args[index + 1].startsWith("--")) throw new Error(`Unknown or incomplete option ${args[index]}`);
    result[key] = args[++index];
  }
  for (const key of ["baseline", "candidate", "out", "environment"]) if (!result[key]) throw new Error(`--${key} is required`);
  if (!result.idle) throw new Error("--idle requires the operator to confirm an otherwise idle, stable host");
  result.cpu = Number(result.cpu);
  if (!Number.isInteger(result.cpu) || result.cpu < 1 || !/^[1-9]\d*(?:ms|s)$/.test(result.benchtime) || !["0", "1"].includes(result.cgo)) throw new Error("Require a positive CPU count, duration benchtime and cgo 0 or 1");
  return result;
}

function command(file, args, config = {}) {
  return execFileSync(file, args, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024, timeout: 60 * 60 * 1000, stdio: ["ignore", "pipe", "pipe"], ...config });
}

function localPath(value) {
  if (!value || isAbsolute(value) || value.split(/[\\/]/).some((part) => part === ".." || part === ".git")) throw new Error(`Unsafe source path: ${value}`);
  return value.replace(/^\.\//, "");
}

function physicalPath(path) {
  const missing = [];
  let parent = resolve(path);
  while (!existsSync(parent)) { missing.unshift(basename(parent)); parent = dirname(parent); }
  return join(realpathSync(parent), ...missing);
}

export function validateTier(tier) {
  if (!tier?.harness?.length || !tier.suites?.length) throw new Error("Tier requires an explicit common harness and benchmark suites");
  tier.harness.forEach(localPath);
  const packages = new Set();
  for (const suite of tier.suites) {
    localPath(suite.package);
    if (packages.has(suite.package) || !suite.bench?.startsWith("^") || !suite.bench.endsWith("$") || !suite.benchmarks?.length || new Set(suite.benchmarks).size !== suite.benchmarks.length) throw new Error("Each benchmark package needs one anchored selector and complete unique benchmark inventory");
    if (suite.benchtime !== undefined && suite.benchtime !== "1x") throw new Error("Suite benchtime override must be 1x for a fresh operation");
    packages.add(suite.package);
  }
  for (const env of [tier.env, ...(tier.structural ?? []).map((check) => check.env)]) {
    for (const [key, value] of Object.entries(env ?? {})) if (!/^YATM_[A-Z0-9_]+$/.test(key) || typeof value !== "string") throw new Error("Only explicit YATM fixture environment variables belong in the tier");
  }
  for (const check of tier.structural ?? []) if (check.scope !== "candidate" || !packages.has(check.package) || !check.run?.startsWith("^") || !check.run.endsWith("$") || !check.tests?.length) throw new Error("Structural checks must explicitly scope candidate correctness, use the common harness and list required tests");
  for (const name of tier.harness) {
    if (![...packages].some((pkg) => name.startsWith(localPath(pkg) + "/")) || !(name.endsWith("_test.go") || name.includes("/testdata/"))) throw new Error("Harness may replace only selected package test files and testdata");
  }
  return tier;
}

// Copy the exact tracked/dirty source bytes; never modify either user's checkout.
function snapshot(source, destination, run) {
  const root = resolve(source);
  const commit = run("git", ["-C", root, "rev-parse", "HEAD"]).trim();
  const paths = [...new Set(run("git", ["-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z"]).split("\0").filter(Boolean))].sort();
  const hashes = [];
  mkdirSync(destination, { recursive: true });
  for (const name of paths) {
    localPath(name);
    const path = join(root, name);
    const info = lstatSync(path, { throwIfNoEntry: false });
    if (!info) continue;
    if (!info.isFile()) throw new Error(`Source must contain ordinary files, not symlinks/submodules: ${name}`);
    const bytes = readFileSync(path);
    hashes.push([name, digest(bytes)]);
    mkdirSync(dirname(join(destination, name)), { recursive: true });
    writeFileSync(join(destination, name), bytes, { mode: info.mode });
  }
  return { commit, sha256: digest(JSON.stringify(hashes)), dirty: !!run("git", ["-C", root, "status", "--porcelain", "--untracked-files=normal"]).trim() };
}

function idle() {
  if (loadavg()[0] > Math.max(0.5, cpus().length / 4)) throw new Error("Host load is too high; collect both sides later on an idle host");
}

export function collect(config, run = command, checkIdle = idle) {
  const manifestPath = resolve(config.manifest ?? join(dirname(fileURLToPath(import.meta.url)), "suites.json"));
  const tier = validateTier(JSON.parse(readFileSync(manifestPath, "utf8")).tiers[config.tier]);
  if (config.tier === "release" && !tier.structural?.length) throw new Error("Release tier requires explicit 1m structural checks");
  const output = physicalPath(config.out), harnessRoot = realpathSync(config.harness ?? config.candidate);
  if (existsSync(output)) throw new Error("Output must be a new directory; existing evidence is never overwritten");
  for (const root of [config.baseline, config.candidate, harnessRoot]) {
    const offset = relative(realpathSync(root), output);
    if (!offset.startsWith(`..${sep}`) && !isAbsolute(offset)) throw new Error("Evidence output must be outside all source checkouts");
  }
  checkIdle();
  mkdirSync(output, { recursive: true });
  const workspace = mkdtempSync(join(dirname(output), ".yatm-performance-"));
  const record = { format: 1, complete: false, tier: config.tier, idleConfirmed: true, startedAt: new Date().toISOString(), sources: {}, runs: [], structural: [] };
  const save = () => writeFileSync(join(output, "pair.json"), JSON.stringify(record, null, 2) + "\n");
  try {
    // One environment and fixture filesystem serve both source revisions.
    let env = Object.fromEntries(["PATH", "HOME", "USER", "LANG", "LC_ALL", "SYSTEMROOT", "GOCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "SSL_CERT_FILE", "SSL_CERT_DIR"].filter((key) => process.env[key]).map((key) => [key, process.env[key]]));
    const toolchain = /^go\s+(\d+\.\d+\.\d+)$/m.exec(readFileSync(join(harnessRoot, "go.mod"), "utf8"))?.[1];
    if (!toolchain) throw new Error("Harness go.mod must pin a full Go patch version");
    Object.assign(env, { GOTOOLCHAIN: `go${toolchain}`, GOENV: "off", GOWORK: "off", GOFLAGS: "-mod=readonly", CGO_ENABLED: config.cgo, GOMAXPROCS: String(config.cpu), GOGC: "100", GOMEMLIMIT: "off", GODEBUG: "", TMPDIR: join(workspace, "tmp"), ...tier.env });
    mkdirSync(env.TMPDIR);
    const selectedGo = resolveGo(`go${toolchain}`, harnessRoot, env, run);
    env = selectedGo.env;
    const go = selectedGo.info;
    const fs = statfsSync(workspace);
    record.environment = { label: config.environment, go, compiler: config.cgo === "1" ? run(go.CC, ["--version"], { env }).split("\n")[0] : null,
      host: { platform: platform(), release: release(), cpu: cpus()[0]?.model, logicalCPUs: cpus().length, totalMemory: totalmem() }, filesystem: { type: fs.type, blockSize: fs.bsize },
      cpu: config.cpu, benchtime: config.benchtime, fixtureEnv: tier.env ?? {}, GOGC: env.GOGC, GOMEMLIMIT: env.GOMEMLIMIT };
    for (const side of ["baseline", "candidate"]) record.sources[side] = snapshot(config[side], join(workspace, side), run);
    const moduleName = /^module\s+(\S+)/m.exec(readFileSync(join(workspace, "candidate", "go.mod"), "utf8"))?.[1];
    if (!moduleName || !readFileSync(join(workspace, "baseline", "go.mod"), "utf8").includes(`module ${moduleName}\n`)) throw new Error("Source module identities differ");
    record.suites = tier.suites.map((suite) => ({ ...suite, benchtime: suite.benchtime ?? config.benchtime, importPath: `${moduleName}/${localPath(suite.package)}` }));

    // Install exactly the same reviewed test files, dropping each implementation's old package tests.
    const harness = tier.harness.map((name) => {
      const path = join(harnessRoot, localPath(name));
      if (!lstatSync(path).isFile()) throw new Error(`Harness paths must name files: ${name}`);
      return { name, bytes: readFileSync(path) };
    });
    record.collector = { sha256: digest(readFileSync(fileURLToPath(import.meta.url))), manifestSHA256: digest(readFileSync(manifestPath)) };
    record.harness = { commit: run("git", ["-C", harnessRoot, "rev-parse", "HEAD"]).trim(), files: harness.map(({ name, bytes }) => [name, digest(bytes)]), sha256: digest(JSON.stringify(harness.map(({ name, bytes }) => [name, digest(bytes)]))) };
    for (const side of ["baseline", "candidate"]) {
      for (const suite of tier.suites) {
        const directory = join(workspace, side, localPath(suite.package));
        for (const name of readdirSync(directory)) if (name.endsWith("_test.go") || name === "testdata") rmSync(join(directory, name), { recursive: true, force: true });
      }
      for (const { name, bytes } of harness) {
        mkdirSync(dirname(join(workspace, side, name)), { recursive: true });
        writeFileSync(join(workspace, side, name), bytes);
      }
      writeFileSync(join(output, `${side}.bench`), "");
      for (const [index, suite] of record.suites.entries()) run(selectedGo.file, ["test", "-c", "-p=1", "-o", join(workspace, `${side}-${index}.test`), suite.package], { cwd: join(workspace, side), env });
    }
    save();

    // Run each standard Go benchmark suite once per source. Go calibrates its own loop.
    for (const [index, suite] of record.suites.entries()) {
      for (const side of ["baseline", "candidate"]) {
        const args = ["-test.run=^$", `-test.bench=${suite.bench}`, "-test.benchmem", `-test.benchtime=${suite.benchtime}`, "-test.count=1", `-test.cpu=${config.cpu}`, "-test.parallel=1", "-test.timeout=30m"];
        const stdout = run(join(workspace, `${side}-${index}.test`), args, { cwd: join(workspace, side, localPath(suite.package)), env });
        const text = `pkg: ${suite.importPath}\n${stdout}\n`;
        appendFileSync(join(output, `${side}.bench`), text);
        if (!/^PASS\s*$/m.test(stdout)) throw new Error("Benchmark suite did not complete");
        validateInventory(readSamples(text), [suite]);
        record.runs.push({ side, package: suite.package, finishedAt: new Date().toISOString(), loadAverage: loadavg(), sha256: digest(text) });
        save();
      }
    }
    for (const [index, check] of (tier.structural ?? []).entries()) {
      const suiteIndex = tier.suites.findIndex((suite) => suite.package === check.package);
      const result = { ...check, passed: false, outputs: {} }; record.structural.push(result);
      // Structural correctness is checked once on the candidate, separately from timing.
      const binary = join(workspace, `candidate-${suiteIndex}.test`);
      const settings = { cwd: join(workspace, "candidate", localPath(check.package)), env: { ...env, ...check.env } };
      const listed = run(binary, [`-test.list=${check.run}`], settings).trim().split(/\r?\n/).sort();
      if (JSON.stringify(listed) !== JSON.stringify([...check.tests].sort())) throw new Error("Required candidate structural tests are absent");
      const stdout = run(binary, [`-test.run=${check.run}`, "-test.count=1", "-test.v", "-test.timeout=60m"], settings);
      const file = `structural-${index}-candidate.log`; writeFileSync(join(output, file), stdout);
      if (/--- SKIP:/.test(stdout) || !/^PASS\s*$/m.test(stdout)) throw new Error("Candidate structural checks skipped or did not pass");
      result.outputs.candidate = { file, sha256: digest(stdout) };
      result.passed = true; save();
    }
    validateSamples(readSamples(readFileSync(join(output, "baseline.bench"), "utf8")), readSamples(readFileSync(join(output, "candidate.bench"), "utf8")), record.suites);
    record.outputs = Object.fromEntries(["baseline", "candidate"].map((side) => [side, digest(readFileSync(join(output, `${side}.bench`)))]));
    record.complete = true; record.finishedAt = new Date().toISOString(); save();
    return record;
  } catch (error) {
    record.error = error.message; save();
    writeFileSync(join(output, "failure.log"), [error.message, error.stdout, error.stderr].filter(Boolean).join("\n"));
    throw error;
  } finally { rmSync(workspace, { recursive: true, force: true }); }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { collect(options(process.argv.slice(2))); }
  catch (error) { console.error(error.message); process.exitCode = 1; }
}
