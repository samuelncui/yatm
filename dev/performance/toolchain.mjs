import { join } from "node:path";

// Resolve in the reviewed harness module, then use that exact binary outside it too.
export function resolveGo(version, cwd, env, run) {
  if (!/^go\d+\.\d+\.\d+$/.test(version)) throw new Error("An exact Go patch version is required");
  const info = JSON.parse(run("go", ["env", "-json", "GOVERSION", "GOROOT", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOEXPERIMENT", "CGO_ENABLED", "CC", "CXX"], { cwd, env: { ...env, GOTOOLCHAIN: version } }));
  if (info.GOVERSION !== version || !info.GOROOT) throw new Error(`Go toolchain mismatch: need ${version}, found ${info.GOVERSION}`);
  const file = join(info.GOROOT, "bin", "go");
  delete info.GOROOT;
  return { file, info, env: { ...env, GOTOOLCHAIN: "local" } };
}
