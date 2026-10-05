import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import {
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  rm,
  symlink,
  writeFile,
} from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";

const frontend = fileURLToPath(new URL("../frontend/", import.meta.url));
const record = `
import { appendFileSync } from "node:fs";
const args = process.argv.slice(2);
const log = (kind) => appendFileSync(process.env.DEMO_TEST_LOG, JSON.stringify({
  kind, args, cwd: process.cwd(), pid: process.pid, proxy: process.env.DEV_SERVICE_BASE,
}) + "\\n");
`;
const backend = `#!/usr/bin/env node
${record}
import { createServer } from "node:http";
log("backend");
if (process.env.DEMO_TEST_BACKEND_EXIT) process.exit(Number(process.env.DEMO_TEST_BACKEND_EXIT));
const address = new URL("http://" + process.env.YATM_DEMO_LISTEN);
const server = createServer((request, response) => response.end("Demo " + request.url));
server.on("error", (error) => { console.error(error.message); process.exit(23); });
server.listen(Number(address.port), address.hostname, () => log("backend-ready"));
process.on("SIGTERM", () => server.close(() => process.exit(0)));
`;
const go = `#!/usr/bin/env node
${record}
import { copyFileSync, mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
log("go");
if (process.env.DEMO_TEST_GO_FAIL === args[0]) process.exit(31);
if (args[0] === "run") {
  const root = args[args.indexOf("-root") + 1];
  mkdirSync(root, { recursive: true });
  writeFileSync(join(root, "config.yaml"), "Demo fixture");
} else if (args[0] === "build") {
  const output = args[args.indexOf("-o") + 1];
  mkdirSync(dirname(output), { recursive: true });
  copyFileSync(process.env.DEMO_TEST_BACKEND, output);
}
`;
const pnpm = `#!/usr/bin/env node
${record}
import { mkdirSync, writeFileSync } from "node:fs";
log("pnpm");
mkdirSync("frontend/dist/assets", { recursive: true });
writeFileSync("frontend/dist/index.html", "production frontend");
writeFileSync("frontend/dist/assets/app.js", "production assets");
`;
const vite = `${record}
log("frontend");
if (process.env.DEMO_TEST_FRONTEND_EXIT) process.exit(Number(process.env.DEMO_TEST_FRONTEND_EXIT));
setInterval(() => {}, 1000);
process.on("SIGTERM", () => process.exit(0));
`;

async function until(predicate, detail) {
  const deadline = Date.now() + 15000;
  while (Date.now() < deadline) {
    if (await predicate()) return;
    await delay(20);
  }
  assert.fail(`Timed out: ${detail}`);
}

async function listen(server, host = "127.0.0.1", port = 0) {
  server.listen(port, host);
  await once(server, "listening");
  return server.address().port;
}

async function freePort() {
  const server = createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function alive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
    return false;
  }
}

async function fixture(
  t,
  { realVite = false, env = {}, args = ["--dev"] } = {},
) {
  const root = await realpath(
    await mkdtemp(join(tmpdir(), "yatm-demo-script-")),
  );
  const repo = join(root, "repo");
  const caller = join(root, "caller directory");
  const demoRoot = join(caller, "yatm-demo-review");
  const logPath = join(root, "commands.jsonl");
  const bin = join(root, "bin");
  await Promise.all([
    mkdir(join(repo, "dev"), { recursive: true }),
    mkdir(join(repo, "frontend"), { recursive: true }),
    mkdir(caller),
    mkdir(bin),
  ]);
  await copyFile(
    new URL("./demo.sh", import.meta.url),
    join(repo, "dev/demo.sh"),
  );
  await writeFile(logPath, "");
  await writeFile(join(bin, "backend"), backend, { mode: 0o755 });
  await writeFile(join(bin, "go"), go, { mode: 0o755 });
  await writeFile(join(bin, "pnpm"), pnpm, { mode: 0o755 });
  if (realVite) {
    await symlink(
      join(frontend, "node_modules"),
      join(repo, "frontend/node_modules"),
    );
    for (const file of ["vite.config.ts", "index.html", "package.json"]) {
      await copyFile(join(frontend, file), join(repo, "frontend", file));
    }
  } else {
    await mkdir(join(repo, "frontend/node_modules/vite/bin"), {
      recursive: true,
    });
    await writeFile(join(repo, "frontend/package.json"), '{"type":"module"}');
    await writeFile(join(repo, "frontend/node_modules/vite/bin/vite.js"), vite);
  }
  await mkdir(join(demoRoot, "frontend"), { recursive: true });
  await writeFile(join(demoRoot, "frontend/reviewer.txt"), "retained frontend");

  const environment = { ...process.env };
  for (const key of Object.keys(environment)) {
    if (key.startsWith("YATM_DEMO_") || key.startsWith("DEMO_TEST_"))
      delete environment[key];
  }
  const backendPort = await freePort();
  const child = spawn("bash", [join(repo, "dev/demo.sh"), ...args], {
    cwd: caller,
    detached: true,
    env: {
      ...environment,
      PATH: `${bin}:${process.env.PATH}`,
      YATM_DEMO_ROOT: "yatm-demo-review",
      YATM_DEMO_LISTEN: `127.0.0.1:${backendPort}`,
      DEMO_TEST_LOG: logPath,
      DEMO_TEST_BACKEND: join(bin, "backend"),
      ...env,
    },
  });
  let output = "";
  child.stdout.on("data", (data) => (output += data));
  child.stderr.on("data", (data) => (output += data));
  let exit;
  child.on("exit", (code, signal) => (exit = { code, signal }));
  t.after(async () => {
    // The test owns this isolated process group, including cleanup after failed assertions.
    if (alive(-child.pid)) process.kill(-child.pid, "SIGTERM");
    await until(() => exit, output);
    if (alive(-child.pid)) process.kill(-child.pid, "SIGKILL");
    await rm(root, { force: true, recursive: true });
  });
  const commands = async () =>
    (await readFile(logPath, "utf8"))
      .trim()
      .split("\n")
      .filter(Boolean)
      .map(JSON.parse);
  return {
    child,
    commands,
    repo,
    demoRoot,
    backendPort,
    output: () => output,
    async exited() {
      await until(() => exit, output);
      return exit;
    },
    async ready() {
      await until(async () => {
        const rows = await commands();
        return (
          rows.some((row) => row.kind === "backend-ready") &&
          rows.some((row) => row.kind === "frontend")
        );
      }, "both Demo children started");
    },
  };
}

async function childrenStopped(demo) {
  const children = (await demo.commands()).filter(
    (row) => row.kind === "backend" || row.kind === "frontend",
  );
  assert.equal(children.length, 2, demo.output());
  for (const child of children)
    assert.equal(alive(child.pid), false, `${child.kind} must be reaped`);
}

test("production Demo builds and installs assets and forwards explicit fixture options", async (t) => {
  const demo = await fixture(t, {
    args: [],
    env: {
      YATM_DEMO_RESET: "1",
      YATM_DEMO_VIDEO: "/tmp/clip with spaces.mp4",
      YATM_DEMO_IDENTICAL_FILES: "10000",
      DEMO_TEST_BACKEND_EXIT: "0",
    },
  });
  assert.deepEqual(await demo.exited(), { code: 0, signal: null });
  const rows = await demo.commands();
  assert.deepEqual(
    rows.map((row) => row.kind),
    ["go", "pnpm", "go", "backend"],
  );
  assert.deepEqual(rows[0].args, [
    "run",
    "./cmd/demo",
    "-root",
    demo.demoRoot,
    "-listen",
    `127.0.0.1:${demo.backendPort}`,
    "-reset",
    "-video",
    "/tmp/clip with spaces.mp4",
    "-identical-files",
    "10000",
  ]);
  assert.deepEqual(rows[1].args, ["--dir", "frontend", "build"]);
  assert.deepEqual(rows[2].args, [
    "build",
    "-o",
    join(demo.demoRoot, "yatm-httpd"),
    "./cmd/httpd",
  ]);
  assert.equal(rows[0].cwd, demo.repo);
  assert.equal(rows[3].cwd, demo.demoRoot);
  assert.deepEqual(rows[3].args, ["-config", "./config.yaml"]);
  assert.equal(
    await readFile(join(demo.demoRoot, "frontend/index.html"), "utf8"),
    "production frontend",
  );
  assert.equal(
    await readFile(join(demo.demoRoot, "frontend/assets/app.js"), "utf8"),
    "production assets",
  );
});

for (const [signal, code] of [
  ["SIGTERM", 143],
  ["SIGINT", 130],
]) {
  test(`development Demo preserves fixtures and cleans both children on ${signal}`, async (t) => {
    const demo = await fixture(t);
    await demo.ready();
    const rows = await demo.commands();
    const frontend = rows.find((row) => row.kind === "frontend");
    assert.deepEqual(
      rows.filter((row) => row.kind === "go").map((row) => row.args),
      [
        [
          "run",
          "./cmd/demo",
          "-root",
          demo.demoRoot,
          "-listen",
          `127.0.0.1:${demo.backendPort}`,
        ],
        ["build", "-o", join(demo.demoRoot, "yatm-httpd"), "./cmd/httpd"],
      ],
    );
    assert.equal(
      rows.some((row) => row.kind === "pnpm"),
      false,
    );
    assert.deepEqual(frontend.args, [
      "--host",
      "localhost",
      "--port",
      "5173",
      "--strictPort",
    ]);
    assert.equal(frontend.proxy, `http://127.0.0.1:${demo.backendPort}`);
    assert.equal(frontend.cwd, join(demo.repo, "frontend"));
    assert.equal(
      await readFile(join(demo.demoRoot, "frontend/reviewer.txt"), "utf8"),
      "retained frontend",
    );
    demo.child.kill(signal);
    assert.deepEqual(await demo.exited(), { code, signal: null });
    await childrenStopped(demo);
  });
}

for (const [kind, code] of [
  ["BACKEND", 41],
  ["FRONTEND", 42],
]) {
  test(`development Demo cleans its peer on early ${kind.toLowerCase()} failure`, async (t) => {
    const demo = await fixture(t, {
      env: { [`DEMO_TEST_${kind}_EXIT`]: String(code) },
    });
    assert.deepEqual(await demo.exited(), { code, signal: null });
    // A peer may be stopped before its Node entrypoint records startup.
    for (const child of (await demo.commands()).filter(
      (row) => row.kind === "backend" || row.kind === "frontend",
    )) {
      assert.equal(alive(child.pid), false);
    }
    assert.equal(
      alive(-demo.child.pid),
      false,
      "no child process remains in the Demo group",
    );
  });
}

for (const stage of ["run", "build"]) {
  test(`development Demo starts no runtime children after failed Go ${stage}`, async (t) => {
    const demo = await fixture(t, { env: { DEMO_TEST_GO_FAIL: stage } });
    assert.deepEqual(await demo.exited(), { code: 31, signal: null });
    assert.equal(
      (await demo.commands()).every((row) => row.kind === "go"),
      true,
    );
  });
}

test("development Demo forwards reset only when explicitly requested", async (t) => {
  const demo = await fixture(t, {
    env: { YATM_DEMO_RESET: "1", YATM_DEMO_FRONTEND_PORT: "5199" },
  });
  await demo.ready();
  const rows = await demo.commands();
  assert.equal(rows[0].args.at(-1), "-reset");
  assert.deepEqual(rows.find((row) => row.kind === "frontend").args, [
    "--host",
    "localhost",
    "--port",
    "5199",
    "--strictPort",
  ]);
  demo.child.kill("SIGTERM");
  await demo.exited();
  await childrenStopped(demo);
});

test("invalid options and ports fail before fixture preparation", async (t) => {
  for (const options of [
    { args: ["--unknown"] },
    { args: ["--dev", "extra"] },
    ...["0", "65536", "invalid"].map((port) => ({
      env: { YATM_DEMO_FRONTEND_PORT: port },
    })),
  ]) {
    const demo = await fixture(t, options);
    assert.deepEqual(await demo.exited(), { code: 2, signal: null });
    assert.deepEqual(await demo.commands(), []);
  }
});

test("real Vite serves HMR and proxies both Demo backend routes on a custom port", async (t) => {
  const port = await freePort();
  const demo = await fixture(t, {
    realVite: true,
    env: { YATM_DEMO_FRONTEND_PORT: String(port) },
  });
  let html;
  await until(async () => {
    try {
      const response = await fetch(`http://localhost:${port}/`);
      html = await response.text();
      return response.ok;
    } catch {
      return false;
    }
  }, "Vite frontend started");
  assert.match(html, /window\.apiBase = "\/services"/);
  assert.match(html, /\/@vite\/client/);
  assert.equal(
    (await fetch(`http://localhost:${port}/@vite/client`)).status,
    200,
  );
  for (const route of ["/services/probe", "/files/probe"]) {
    assert.equal(
      await (await fetch(`http://localhost:${port}${route}`)).text(),
      `Demo ${route}`,
    );
  }
  demo.child.kill("SIGTERM");
  assert.deepEqual(await demo.exited(), { code: 143, signal: null });
  await assert.rejects(fetch(`http://localhost:${port}/`));
  await assert.rejects(fetch(`http://127.0.0.1:${demo.backendPort}/`));
});

test("an occupied frontend port fails instead of moving and stops the backend", async (t) => {
  const blocker = createServer();
  const port = await listen(blocker, "localhost");
  t.after(() => new Promise((resolve) => blocker.close(resolve)));
  const demo = await fixture(t, {
    realVite: true,
    env: { YATM_DEMO_FRONTEND_PORT: String(port) },
  });
  assert.deepEqual(await demo.exited(), { code: 1, signal: null });
  assert.match(demo.output(), new RegExp(`Port ${port} is already in use`));
  const backend = (await demo.commands()).find((row) => row.kind === "backend");
  assert.equal(alive(backend.pid), false);
  await assert.rejects(fetch(`http://127.0.0.1:${demo.backendPort}/`));
});

test("an occupied backend port fails and stops the frontend", async (t) => {
  const blocker = createServer();
  const port = await listen(blocker);
  t.after(() => new Promise((resolve) => blocker.close(resolve)));
  const demo = await fixture(t, {
    env: { YATM_DEMO_LISTEN: `127.0.0.1:${port}` },
  });
  assert.deepEqual(await demo.exited(), { code: 23, signal: null });
  assert.match(demo.output(), /EADDRINUSE/);
  assert.equal(alive(-demo.child.pid), false);
});
