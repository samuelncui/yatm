import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { createFrontendArtifact, installFrontendArtifact, validateFrontendArtifact } from './frontend-artifact.mjs';

const version = 'v1.0.0-alpha.2';
const commit = 'a'.repeat(40);

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-frontend-artifact-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const artifact = path.join(root, 'artifact');
  const lockfile = path.join(root, 'pnpm-lock.yaml');
  fs.writeFileSync(lockfile, 'locked dependency graph');
  function write(name, content) {
    const target = path.join(artifact, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, content);
  }
  write('frontend/index.html', `<meta name="yatm-version" content="${version}"><meta name="yatm-commit" content="${commit}">`);
  write('frontend/assets/index-abcd1234.js', 'public frontend');
  write('licenses/dependencies.json', JSON.stringify([{ kind: 'node', name: '@public/example', version: '1.0.0', notices: ['LICENSE'] }]));
  write('licenses/node/@public/example@1.0.0/LICENSE', 'public license');
  write('licenses/node/@public/example@1.0.0/package.json', '{"name":"@public/example","version":"1.0.0"}');
  write('licenses/react-dnd.LICENSE', 'copied attribution');
  createFrontendArtifact(artifact, version, commit, lockfile);
  return { root, artifact, lockfile, write, validate: () => validateFrontendArtifact(artifact, version, commit, lockfile) };
}

test('every target receives identical frontend and notices without the internal manifest', t => {
  const f = fixture(t);
  const contents = f.validate();
  for (const target of ['linux', 'darwin', 'freebsd']) {
    const destination = path.join(f.root, target);
    installFrontendArtifact(f.artifact, destination, version, commit, f.lockfile);
    for (const [name, bytes] of contents) assert.deepEqual(fs.readFileSync(path.join(destination, name)), bytes);
    assert(!fs.existsSync(path.join(destination, 'manifest.json')));
  }
});

test('version, commit and lock identity must match actual build inputs', t => {
  const f = fixture(t);
  assert.throws(() => validateFrontendArtifact(f.artifact, 'v1.0.0-alpha.3', commit, f.lockfile), /version mismatch/);
  assert.throws(() => validateFrontendArtifact(f.artifact, version, 'b'.repeat(40), f.lockfile), /commit mismatch/);
  fs.writeFileSync(f.lockfile, 'other dependencies');
  assert.throws(f.validate, /lockfile mismatch/);
});

test('modified, missing, extra and private files are rejected', async t => {
  const cases = [
    ['modified', f => f.write('frontend/assets/index-abcd1234.js', 'changed'), /checksum mismatch/],
    ['missing', f => fs.unlinkSync(path.join(f.artifact, 'frontend/assets/index-abcd1234.js')), /file list mismatch/],
    ['extra asset', f => f.write('frontend/assets/extra-abcd1234.js', 'extra'), /file list mismatch/],
    ['source map', f => f.write('frontend/assets/index-abcd1234.js.map', '{}'), /Unexpected frontend artifact file/],
    ['private config', f => f.write('config.yaml', 'token: fake-secret'), /Unexpected frontend artifact file/],
    ['missing license', f => fs.unlinkSync(path.join(f.artifact, 'licenses/node/@public/example@1.0.0/LICENSE')), /notice missing/],
    ['symlink', f => fs.symlinkSync(f.lockfile, path.join(f.artifact, 'linked')), /ordinary files/],
  ];
  for (const [name, mutate, error] of cases) await t.test(name, t => {
    const f = fixture(t);
    mutate(f);
    assert.throws(f.validate, error);
  });
});

test('a stale dist cannot acquire the identity of a new artifact', t => {
  const f = fixture(t);
  f.write('frontend/index.html', '<meta name="yatm-version" content="development">');
  assert.throws(() => createFrontendArtifact(f.artifact, version, commit, f.lockfile), /Frontend version/);
});

test('only owned Node notices enter the shared artifact', t => {
  const f = fixture(t);
  f.write('licenses/dependencies.json', JSON.stringify([{ kind: 'go', name: 'private', version: '1', notices: ['LICENSE'] }]));
  assert.throws(() => createFrontendArtifact(f.artifact, version, commit, f.lockfile), /notice ownership/);
});
