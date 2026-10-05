// Run one cold and one warm native build with an isolated compiler cache.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { performance } from 'node:perf_hooks';
import { releaseCommit } from '../../build/release/committed-inputs.mjs';

const repository = path.resolve(import.meta.dirname, '../..');
const output = path.resolve(process.argv[2] || 'output/preview-cache-check');
assert(!fs.existsSync(output), 'Select a new cache-check output directory');
assert(process.env.RELEASE_VERSION, 'Set RELEASE_VERSION before comparing native builds');
const commit = releaseCommit(repository, process.env.RELEASE_COMMIT);
const env = { ...process.env, RELEASE_COMMIT: commit, CCACHE_DIR: path.join(output, 'cache') };
const probe = spawnSync('ccache', ['--version'], { encoding: 'utf8', env });
assert.equal(probe.status, 0, 'Install ccache before checking cache performance');
fs.mkdirSync(output, { recursive: true });
const report = { commit, version: env.RELEASE_VERSION, ccache: probe.stdout.split('\n')[0], builds: [] };
const stats = () => {
  const result = spawnSync('ccache', ['--print-stats'], { encoding: 'utf8', env });
  assert.equal(result.status, 0, result.stderr);
  return Object.fromEntries(result.stdout.trim().split('\n').map(line => {
    const [name, count] = line.split(/\s+/);
    return [name, Number(count)];
  }));
};
for (const phase of ['cold', 'warm']) {
  const log = fs.openSync(path.join(output, `${phase}.log`), 'w');
  const before = stats();
  const start = performance.now();
  let result;
  try {
    result = spawnSync('bash', ['previewworker/build/build.sh'], { cwd: repository,
      env: { ...env, RELEASE_DIRECTORY: path.join(output, phase) }, stdio: ['ignore', log, log] });
  } finally { fs.closeSync(log); }
  const after = stats();
  const item = { phase, seconds: (performance.now() - start) / 1000, exit: result.status,
    counters: Object.fromEntries(Object.keys(after).map(name => [name, after[name] - (before[name] || 0)])) };
  report.builds.push(item);
  fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify(report, null, 2) + '\n');
  assert.equal(result.status, 0, `${phase} build failed; inspect ${phase}.log`);
  console.log(`${phase}: ${item.seconds.toFixed(1)}s, cache hits ${(item.counters.direct_cache_hit || 0) + (item.counters.preprocessed_cache_hit || 0)}`);
}
const warm = report.builds[1].counters;
assert((warm.direct_cache_hit || 0) + (warm.preprocessed_cache_hit || 0) > 0, 'Warm build had no compiler cache hits');
