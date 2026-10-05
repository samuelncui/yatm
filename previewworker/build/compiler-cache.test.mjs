import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const repository = path.resolve(import.meta.dirname, '../..');
const script = path.join(import.meta.dirname, 'compiler-cache.sh');
const recipes = ['previewworker/build/build.sh', 'previewworker/build/compiler-cache.sh', 'previewworker/build/ffmpeg-sysctl-header.patch', 'build/release/sanitize-native.mjs'];
const run = (args, env = {}, cwd) => {
  const result = spawnSync('bash', args, { cwd, env: { ...process.env, ...env }, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
};
function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-compiler-cache-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

test('native cache identities follow recipe, target, compiler and SDK, not checkout paths', t => {
  const root = temporary(t);
  const tool = path.join(root, 'compiler');
  fs.writeFileSync(tool, '#!/bin/sh\nprintf "%s\\n" "$COMPILER_VERSION"\n', { mode: 0o755 });
  fs.writeFileSync(path.join(root, 'xcrun'), '#!/bin/sh\nprintf "%s\\n" "$SDK_VERSION"\n', { mode: 0o755 });
  function copy(name) {
    const dir = path.join(root, name);
    for (const recipe of recipes) {
      fs.mkdirSync(path.dirname(path.join(dir, recipe)), { recursive: true });
      fs.copyFileSync(path.join(repository, recipe), path.join(dir, recipe));
    }
    return dir;
  }
  const first = copy('one');
  const second = copy('two');
  const env = { PREVIEW_TARGET: 'linux-amd64', PREVIEW_CC: tool, PREVIEW_CXX: tool, COMPILER_VERSION: 'compiler-1', SDK_VERSION: '15.0', PATH: `${root}:${process.env.PATH}` };
  const key = (dir, extra = {}) => run([path.join(dir, 'previewworker/build/compiler-cache.sh'), 'key'], { ...env, ...extra });
  const original = key(first);
  assert.match(original, /^[a-f0-9]{64}$/);
  assert.equal(key(second), original);
  assert.notEqual(key(first, { PREVIEW_TARGET: 'linux-arm64' }), original);
  assert.notEqual(key(first, { COMPILER_VERSION: 'compiler-2' }), original);
  const mac = key(first, { PREVIEW_TARGET: 'darwin-arm64' });
  assert.notEqual(key(first, { PREVIEW_TARGET: 'darwin-arm64', SDK_VERSION: '16.0' }), mac);
  fs.appendFileSync(path.join(second, recipes[0]), '\n# changed native recipe\n');
  assert.notEqual(key(second), original);
});

test('cache opt-in preserves the underlying Go compiler and uncached fallback', t => {
  const root = temporary(t);
  const shell = `set -euo pipefail; REPOSITORY="$CACHE_REPOSITORY"; TARGET=linux-amd64; CC=cc; CXX=c++; BUILD_DIRECTORY="$CACHE_BUILD"; source "$CACHE_SCRIPT"; printf '%s\\n' "$CC" "$CXX" "$NATIVE_CC" "$NATIVE_CXX"`;
  const env = { CACHE_REPOSITORY: repository, CACHE_BUILD: root, CACHE_SCRIPT: script, CCACHE_DIR: '' };
  assert.equal(run(['-c', shell], env), 'cc\nc++\ncc\nc++');
  fs.writeFileSync(path.join(root, 'ccache'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  assert.equal(run(['-c', shell], { ...env, CCACHE_DIR: path.join(root, 'cache'), PATH: `${root}:${process.env.PATH}` }), 'cc\nc++\nccache cc\nccache c++');
});

const hasCompilerCache = spawnSync('ccache', ['--version']).status === 0 && spawnSync('cc', ['--version']).status === 0;
test('real ccache reuses relocated objects and still detects changed source', { skip: !hasCompilerCache }, t => {
  const root = temporary(t);
  const cache = path.join(root, 'cache');
  const objects = [];
  for (const [name, value] of [['cold', 1], ['warm', 1], ['changed', 2]]) {
    const build = path.join(root, name);
    fs.mkdirSync(build);
    fs.writeFileSync(path.join(build, 'sample.c'), `const char *source_path = __FILE__; int sample(void) { return ${value}; }\n`);
    const object = path.join(build, 'sample.o');
    run(['-c', 'set -euo pipefail; REPOSITORY="$CACHE_REPOSITORY"; TARGET=linux-amd64; CC=cc; CXX=c++; BUILD_DIRECTORY="$CACHE_BUILD"; source "$CACHE_SCRIPT"; $NATIVE_CC -O2 -g0 "-ffile-prefix-map=$BUILD_DIRECTORY=/build/yatm-preview" "-fdebug-prefix-map=$BUILD_DIRECTORY=/build/yatm-preview" -c sample.c -o sample.o'],
      { CCACHE_DIR: cache, CACHE_REPOSITORY: repository, CACHE_BUILD: build, CACHE_SCRIPT: script }, build);
    objects.push(fs.readFileSync(object));
  }
  assert.deepEqual(objects[0], objects[1]);
  assert.notDeepEqual(objects[1], objects[2]);
  assert(!objects[1].includes(Buffer.from(root)), 'Cached object leaked a build path');
  const stats = spawnSync('ccache', ['--print-stats'], { env: { ...process.env, CCACHE_DIR: cache }, encoding: 'utf8' });
  assert.equal(stats.status, 0, stats.stderr);
  const counters = new Map(stats.stdout.trim().split('\n').map(line => line.split(/\s+/)));
  assert(Number(counters.get('direct_cache_hit')) + Number(counters.get('preprocessed_cache_hit')) >= 1, stats.stdout);
});
