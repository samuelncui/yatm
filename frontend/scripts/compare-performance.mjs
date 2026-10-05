import { spawnSync } from "node:child_process";
import console from "node:console";
import { closeSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, realpathSync, rmSync, writeFileSync } from "node:fs";
import { arch, cpus, loadavg, platform, release, totalmem } from "node:os";
import { basename, delimiter, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";
import process from "node:process";
import { fileURLToPath, URL } from "node:url";
import { parseArgs } from "node:util";
import { digest } from "../../dev/performance/evidence.mjs";

const benchmark = "src/components/files-browser.bench.ts";
const harnessPath = `frontend/${benchmark}`;
const sides = ["baseline", "candidate"];
const usage = "Usage: node frontend/scripts/compare-performance.mjs --baseline SOURCE --candidate SOURCE --harness SOURCE --out NEW_DIRECTORY";

export function options(args) {
  const { values } = parseArgs({
    args,
    options: {
      baseline: { type: "string" },
      candidate: { type: "string" },
      harness: { type: "string" },
      out: { type: "string" },
      help: { type: "boolean" },
    },
  });
  if (!values.help)
    for (const key of ["baseline", "candidate", "harness", "out"]) {
      if (!values[key]) throw new Error(`--${key} is required\n${usage}`);
    }
  return values;
}

function inside(root, path) {
  const offset = relative(root, path);
  return offset !== ".." && !offset.startsWith(`..${sep}`) && !isAbsolute(offset);
}

function sourceFile(root, name) {
  const path = join(root, name);
  if (
    !name.startsWith("frontend/") ||
    name
      .split(/[\\/]/)
      .some((part) => ["..", ".git", "node_modules", ".local", ".npmrc"].includes(part) || (part.startsWith(".env") && part !== ".env.example"))
  ) {
    throw new Error(`Not a frontend source file: ${name}`);
  }
  const info = lstatSync(path, { throwIfNoEntry: false });
  if (!info) return null; // A tracked deletion is part of the supplied source.
  if (!info.isFile() || realpathSync(path) !== path) throw new Error(`Source must use ordinary files and directories, without symlinks: ${name}`);
  return { bytes: readFileSync(path), mode: info.mode & 0o777 };
}

// The current harness has exactly one benchmark. A zero-exit empty/skipped run is not evidence.
function measurement(path, frontend) {
  const report = JSON.parse(readFileSync(path, "utf8"));
  const files = report.files ?? [];
  const results = files.flatMap((file) => (file.groups ?? []).flatMap((group) => group.benchmarks ?? []));
  const result = results[0];
  if (
    files.length !== 1 ||
    resolve(frontend, files[0].filepath) !== join(frontend, benchmark) ||
    results.length !== 1 ||
    !result.id ||
    !result.name ||
    result.error ||
    !Number.isInteger(result.sampleCount) ||
    result.sampleCount < 1 ||
    !Number.isFinite(result.mean) ||
    result.mean <= 0 ||
    !Number.isFinite(result.hz) ||
    result.hz <= 0
  ) {
    throw new Error("Missing or invalid frontend benchmark measurement");
  }
  return { id: result.id, name: result.name };
}

export function collect(config, spawn = spawnSync) {
  if (Number(process.versions.node.split(".")[0]) < 24) throw new Error("Node.js >=24 is required; use the same current Node for both sources");
  const roots = Object.fromEntries([...sides, "harness"].map((side) => [side, realpathSync(config[side])]));
  // Requiring an existing parent keeps output validation physical, including symlink aliases.
  const output = join(realpathSync(dirname(resolve(config.out))), basename(resolve(config.out)));
  if (lstatSync(output, { throwIfNoEntry: false })) throw new Error("Output must be a new directory; existing evidence is never overwritten");
  if (Object.values(roots).some((root) => inside(root, output))) throw new Error("Output must be outside all source checkouts");
  mkdirSync(output);
  const workspace = mkdtempSync(join(output, ".work-"));
  const record = { complete: false, startedAt: new Date().toISOString(), sources: {}, commands: [], outputs: {} };
  const save = () => writeFileSync(join(output, "pair.json"), JSON.stringify(record, null, 2) + "\n");
  try {
    const home = join(workspace, "home"),
      tmp = join(workspace, "tmp");
    mkdirSync(home);
    mkdirSync(tmp);
    const userConfig = join(home, "user.npmrc"),
      globalConfig = join(home, "global.npmrc");
    writeFileSync(userConfig, "");
    writeFileSync(globalConfig, "");
    const env = Object.fromEntries(["PATH", "SYSTEMROOT", "LANG", "LC_ALL", "TZ"].filter((key) => process.env[key]).map((key) => [key, process.env[key]]));
    Object.assign(env, {
      PATH: `${dirname(process.execPath)}${delimiter}${env.PATH ?? ""}`,
      HOME: home,
      TMPDIR: tmp,
      TMP: tmp,
      TEMP: tmp,
      CI: "1",
      XDG_CONFIG_HOME: join(home, ".config"),
      XDG_CACHE_HOME: join(home, ".cache"),
      XDG_DATA_HOME: join(home, ".local/share"),
      npm_config_userconfig: userConfig,
      npm_config_globalconfig: globalConfig,
      npm_config_manage_package_manager_versions: "false",
      npm_config_package_manager_strict: "false",
      COREPACK_ENABLE_PROJECT_SPEC: "0",
      COREPACK_DEFAULT_TO_LATEST: "0",
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: userConfig,
      GIT_OPTIONAL_LOCKS: "0",
    });
    function run(file, args, cwd = workspace, log) {
      const entry = log ? { file, args, cwd: relative(workspace, cwd), log, complete: false } : null;
      if (entry) {
        record.commands.push(entry);
        save();
      }
      const fd = log ? openSync(join(output, log), "wx") : null;
      try {
        const result = spawn(file, args, {
          cwd,
          env,
          encoding: "utf8",
          maxBuffer: 16 * 1024 * 1024,
          stdio: fd === null ? ["ignore", "pipe", "pipe"] : ["ignore", fd, fd],
        });
        if (entry) {
          entry.status = result.status;
          entry.signal = result.signal;
        }
        if (result.error || result.status !== 0)
          throw new Error(
            `${file} ${args.join(" ")} failed: ${result.error?.message ?? result.signal ?? `exit ${result.status}`}${log ? ` (see ${log})` : `\n${result.stderr ?? ""}`}`,
          );
        if (entry) entry.complete = true;
        return result.stdout?.trim() ?? "";
      } finally {
        if (fd !== null) {
          closeSync(fd);
          entry.sha256 = digest(readFileSync(join(output, log)));
          save();
        }
      }
    }
    const git = (root, ...args) => run("git", ["-C", root, ...args]);
    const commits = {};
    for (const [side, root] of Object.entries(roots)) {
      if (realpathSync(git(root, "rev-parse", "--show-toplevel")) !== root) throw new Error(`${side} must be a Git checkout root`);
      commits[side] = git(root, "rev-parse", "HEAD");
    }
    git(roots.harness, "ls-files", "--error-unmatch", "--", harnessPath);
    const harness = sourceFile(roots.harness, harnessPath);
    if (!harness) throw new Error(`Missing tracked harness: ${harnessPath}`);
    record.harness = { commit: commits.harness, path: harnessPath, sha256: digest(harness.bytes) };
    record.runner = {
      sha256: digest(readFileSync(fileURLToPath(import.meta.url))),
      helperSHA256: digest(readFileSync(new URL("../../dev/performance/evidence.mjs", import.meta.url))),
    };
    record.environment = { platform: platform(), release: release(), arch: arch(), cpu: cpus()[0]?.model, logicalCPUs: cpus().length, totalMemory: totalmem() };
    record.tools = { node: process.version, nodeSHA256: digest(readFileSync(process.execPath)), pnpm: run("pnpm", ["--version"]), vitest: {} };

    for (const side of sides) {
      const destination = join(workspace, side),
        files = [];
      const names = [...new Set(git(roots[side], "ls-files", "--cached", "-z", "--", "frontend/").split("\0").filter(Boolean))].sort();
      for (const name of names) {
        const file = sourceFile(roots[side], name);
        if (!file) continue;
        files.push([name, file.mode, digest(file.bytes)]);
        mkdirSync(dirname(join(destination, name)), { recursive: true });
        writeFileSync(join(destination, name), file.bytes, { mode: file.mode });
      }
      const frontend = join(destination, "frontend");
      const lockfileSHA256 = digest(readFileSync(join(frontend, "pnpm-lock.yaml")));
      record.sources[side] = { commit: commits[side], sha256: digest(JSON.stringify(files)), files, lockfileSHA256 };
      mkdirSync(dirname(join(destination, harnessPath)), { recursive: true });
      writeFileSync(join(destination, harnessPath), harness.bytes);
      save();
    }
    // Finish both installs before either measurement. Never use a source checkout's dependencies.
    for (const side of sides) {
      const frontend = join(workspace, side, "frontend");
      if (run("pnpm", ["--version"], frontend) !== record.tools.pnpm) throw new Error(`${side} selected a different pnpm version`);
      run(
        "pnpm",
        ["install", "--frozen-lockfile", "--registry=https://registry.npmjs.org", `--store-dir=${join(workspace, "store")}`],
        frontend,
        `${side}-install.log`,
      );
      if (digest(readFileSync(join(frontend, "pnpm-lock.yaml"))) !== record.sources[side].lockfileSHA256)
        throw new Error(`${side} installation changed the frozen lockfile`);
      if (realpathSync(run("pnpm", ["exec", "node", "-p", "process.execPath"], frontend)) !== realpathSync(process.execPath))
        throw new Error(`${side} selected a different Node executable`);
      record.tools.vitest[side] = JSON.parse(readFileSync(join(frontend, "node_modules/vitest/package.json"), "utf8")).version;
    }
    let baseline;
    for (const side of sides) {
      const frontend = join(workspace, side, "frontend"),
        json = join(output, `${side}.json`);
      const args = ["exec", "vitest", "bench", "--run", "--maxWorkers=1", benchmark, `--outputJson=${json}`];
      if (side === "candidate") args.push(`--compare=${join(output, "baseline.json")}`);
      record.environment[`${side}LoadAverage`] = loadavg();
      run("pnpm", args, frontend, `${side}.log`);
      const measured = measurement(json, frontend);
      record.outputs[side] = { file: `${side}.json`, sha256: digest(readFileSync(json)), ...measured };
      if (side === "baseline") baseline = measured;
      else if (JSON.stringify(measured) !== JSON.stringify(baseline))
        throw new Error("Baseline/candidate benchmark identities differ; Vitest cannot compare them");
      save();
    }
    record.complete = true;
    record.finishedAt = new Date().toISOString();
    save();
    return record;
  } catch (error) {
    record.error = error.message;
    save();
    throw error;
  } finally {
    rmSync(workspace, { recursive: true, force: true });
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const config = options(process.argv.slice(2));
    if (config.help) console.log(usage);
    else {
      collect(config);
      process.stdout.write(readFileSync(join(config.out, "candidate.log")));
      console.log(`Frontend comparison evidence: ${resolve(config.out)}`);
    }
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
