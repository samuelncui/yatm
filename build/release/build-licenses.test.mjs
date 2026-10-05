import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { directoryFiles } from './committed-inputs.mjs';

test('split notice collection preserves the full graph without frontend dependencies on platform jobs', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-notices-split-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const write = (name, data, mode = 0o644) => {
    const file = path.join(root, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, data, { mode });
  };
  for (const name of ['build-licenses.mjs', 'native-notices.mjs']) write(`build/release/${name}`, fs.readFileSync(new URL(name, import.meta.url)));
  for (const name of ['THIRD_PARTY_NOTICES', 'licenses/lto-info.LICENSE', 'licenses/react-dnd.LICENSE', 'module/LICENSE', 'goroot/LICENSE', 'frontend/node_modules/example/LICENSE']) write(name, 'public attribution');
  write('frontend/package.json', JSON.stringify({ dependencies: { example: '1.0.0' } }));
  write('frontend/node_modules/example/package.json', JSON.stringify({ name: 'example', version: '1.0.0', license: 'MIT' }));
  write('bin/go', `#!/bin/sh
    echo "$*" >> "$NOTICE_TEST_CALLS"
    case "$*" in
      'list '*) printf 'example.org/module\\tv1.0.0\\t%s/module\\n' "$NOTICE_TEST_ROOT" ;;
      'env GOROOT') printf '%s/goroot\\n' "$NOTICE_TEST_ROOT" ;;
      'env GOVERSION') printf 'go1.26.8\\n' ;;
      *) exit 2 ;;
    esac
  `, 0o755);
  const calls = path.join(root, 'calls');
  const collect = (output, mode) => {
    fs.writeFileSync(calls, '');
    const result = spawnSync(process.execPath, [path.join(root, 'build/release/build-licenses.mjs'), path.join(root, output), mode],
      { env: { ...process.env, GOOS: 'darwin', PATH: `${root}/bin:${process.env.PATH}`, NOTICE_TEST_ROOT: root, NOTICE_TEST_CALLS: calls }, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    return fs.readFileSync(calls, 'utf8');
  };
  assert(collect('full', 'all').includes('env GOROOT'));
  assert.equal(collect('split', 'frontend'), '', 'Frontend collection invoked Go');
  const frontend = JSON.parse(fs.readFileSync(path.join(root, 'split/dependencies.json')));
  assert.deepEqual(frontend.map(item => item.kind), ['node']);
  fs.rmSync(path.join(root, 'frontend/node_modules'), { recursive: true });
  assert(collect('split', 'backend').includes('env GOROOT'));
  const full = directoryFiles(path.join(root, 'full'));
  const split = directoryFiles(path.join(root, 'split'));
  const records = bytes => JSON.parse(bytes).sort((a, b) => `${a.kind}/${a.name}`.localeCompare(`${b.kind}/${b.name}`));
  assert.deepEqual(records(full.get('dependencies.json')), records(split.get('dependencies.json')));
  full.delete('dependencies.json');
  split.delete('dependencies.json');
  assert.deepEqual(split, full);
});
