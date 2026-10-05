import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { checkReleaseSource, checkSourceInventory } from './check-source.mjs';

function fixture(t) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-source-gate-'));
  t.after(() => fs.rmSync(temporary, { recursive: true, force: true }));
  const root = path.join(temporary, 'repository');
  fs.mkdirSync(root);
  const git = (...args) => execFileSync('git', ['-C', root, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  const write = (name, bytes) => {
    const filename = path.join(root, name);
    fs.mkdirSync(path.dirname(filename), { recursive: true });
    fs.writeFileSync(filename, bytes);
  };
  const commit = () => {
    git('add', '-A');
    const parent = git('rev-parse', '--verify', 'HEAD');
    const value = execFileSync('git', ['-C', root, '-c', 'user.name=Source Gate Test', '-c', 'user.email=source@example.invalid',
      'commit-tree', git('write-tree'), '-p', parent], { encoding: 'utf8', input: 'Fixture update\n' }).trim();
    git('update-ref', 'HEAD', value);
    return value;
  };
  git('init', '-q');
  write('README.md', 'Fixture\n');
  write('.gitignore', 'output/\n.local/\nfrontend/dist/\n');
  git('add', '-A');
  const initial = execFileSync('git', ['-C', root, '-c', 'user.name=Source Gate Test', '-c', 'user.email=source@example.invalid',
    'commit-tree', git('write-tree')], { encoding: 'utf8', input: 'Initial fixture\n' }).trim();
  git('update-ref', 'HEAD', initial);
  return { root, temporary, git, write, commit, initial };
}

test('owned templates, generated source, migration and licensed Demo media remain releasable', t => {
  const { root, write, commit } = fixture(t);
  for (const name of ['frontend/.env.example', '.vscode/settings.json', 'config.example.yaml',
    'entity/file.pb.go', 'frontend/src/entity/file.ts', 'internal/migrate/legacy/pb/legacy.pb.go',
    'e2e/testdata/mount-file.sh', 'e2e/ltfs-file-backend/mount', 'previewworker/build/ffmpeg-sysctl-header.patch',
    '.agents/skills/yatm/SKILL.md', 'internal/demo/assets/timeline.png', 'internal/demo/assets/big-buck-bunny.mp4']) write(name, 'Public fixture');
  const head = commit();
  write('.local/private.db', 'Ignored workstation data');
  write('output/private.log', 'Ignored output');
  assert.equal(checkReleaseSource(root, head), head);
});

test('a committed history-exception file can reach the content scanner', t => {
  const { root, write, commit, initial } = fixture(t);
  write('.gitleaksignore', `# Exact historical fingerprint; the content scanner validates its scope.\n${initial}:README.md:personal-build-path:1\n`);
  const head = commit();
  assert.equal(checkReleaseSource(root, head), head);
});

for (const [name, bytes, reason] of [
  ['internal/library/catalog.db', 'runtime data', /Runtime data/],
  ['internal/library/snapshot.dat', 'SQLite format 3\0rest', /Runtime database/],
  ['cmd/yatm-cli/runtime', Buffer.from('7f454c4601020304', 'hex'), /Compiled executable/],
  ['internal/preview/run.log', 'runtime log', /Runtime data/],
  ['e2e/testdata/backup.tar.gz', 'archive', /Runtime data/],
  ['frontend/src/components/draft.tsx.bak', 'scratch', /temporary artifact/],
  ['frontend/.env.production', 'private configuration', /Local environment/],
  ['dev/config.yaml', 'local deployment', /Local configuration/],
  ['frontend/node_modules/package/index.js', 'dependency output', /Private or generated/],
  ['output/README.md', 'build output', /no repository owner/],
  ['experiments/main.go', 'unowned prototype', /no repository owner/],
  ['build/README.md', 'unowned build content', /no repository owner/],
]) {
  test(`committing ${name} cannot bypass the release boundary`, t => {
    const { root, write, git, commit } = fixture(t);
    write(name, bytes);
    git('add', '-f', name);
    commit();
    assert.throws(() => checkReleaseSource(root), reason);
  });
}

test('a release rejects dirty inputs and incomplete history before scanning', t => {
  const { root, temporary, write, commit, initial } = fixture(t);
  write('cmd/demo/main.go', 'Uncommitted source');
  assert.throws(() => checkReleaseSource(root), /inputs must be clean/);
  const head = commit();
  assert.throws(() => checkReleaseSource(root, initial), /must match actual HEAD/);
  const clone = path.join(temporary, 'shallow');
  execFileSync('git', ['clone', '-q', '--depth=1', `file://${root}`, clone]);
  assert.throws(() => checkReleaseSource(clone, head), /history is incomplete/);
  execFileSync('git', ['-C', clone, 'fetch', '-q', '--unshallow']);
  assert.equal(checkReleaseSource(clone, head), head);
});

test('source inventory rejects both resolvable and dangling symlinks before source freeze', t => {
  const { root, write } = fixture(t);
  write('internal/demo/public.txt', 'Fixture');
  fs.symlinkSync('public.txt', path.join(root, 'internal/demo/link.txt'));
  assert.throws(() => checkSourceInventory(root), /ordinary file/);
  fs.unlinkSync(path.join(root, 'internal/demo/public.txt'));
  assert.throws(() => checkSourceInventory(root), /ordinary file/);
});
