import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';
import { committedInputs, directoryFiles, copyPreviewSources } from './committed-inputs.mjs';
import { readArchive } from './archive-members.mjs';
import { yatmSourceFiles, nativeSources, sourceChecksums, validatePreviewSources } from './check-preview-sources.mjs';

const repository = path.resolve(import.meta.dirname, '../..');
const version = 'v1.0.0-alpha.2';
function write(root, name, data, mode = 0o644) {
  const destination = path.join(root, name);
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.writeFileSync(destination, data, { mode });
}
function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-release-input-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}
function fixture(t) {
  const temporaryRoot = temporary(t);
  const root = path.join(temporaryRoot, 'repo');
  fs.mkdirSync(root);
  const git = (...args) => execFileSync('git', ['-C', root, ...args], { encoding: 'utf8' }).trim();
  git('init', '-q');
  write(root, '.gitignore', '.local/\noutput/\n*.log\n*.tmp\nvendor/\n');
  for (const name of yatmSourceFiles) {
    write(root, name, fs.readFileSync(path.join(repository, name)), fs.statSync(path.join(repository, name)).mode);
  }
  for (const name of ['LICENSE', 'README.md', 'CONTEXT.md', 'install-release.sh', 'config.example.yaml', 'cmd/httpd/yatm-httpd.service',
    'docs/README.md', ...['install', 'migration', 'library', 'locations'].map(guide => `docs/operations/${guide}.md`), '.agents/skills/yatm/SKILL.md',
    ...['encrypt', 'get_device', 'mkfs', 'mount', 'mount.openltfs', 'readinfo', 'umount'].map(name => `scripts/${name}`),
    ...['encrypt', 'get_device', 'mkfs', 'mount', 'readinfo', 'umount', 'README.md'].map(name => `e2e/ltfs-file-backend/${name}`)]) write(root, name, 'committed fixture');
  write(root, 'e2e/ltfs-file-backend/mount.test.mjs', '// maintained local regression, not a runtime adapter');
  for (const name of ['build/release/build.sh', 'build/release/check-release.mjs', 'build/release/build-frontend.sh', 'build/release/frontend-artifact.mjs', 'build/release/package-assets.mjs']) {
    write(root, name, fs.readFileSync(path.join(repository, name)), fs.statSync(path.join(repository, name)).mode);
  }
  write(root, 'build/backend/build.sh', '#!/usr/bin/env bash\nset -eu\nfor item in yatm-httpd yatm-cli yatm-export-library yatm-lto-info yatm-migrate; do printf fixture > "$OUTPUT_DIRECTORY/$item"; done\n', 0o755);
  write(root, 'frontend/scripts/build.sh', '#!/usr/bin/env bash\nset -eu\nmkdir -p "$OUTPUT_DIRECTORY/frontend"\nprintf \'<meta name="yatm-version" content="%s"><meta name="yatm-commit" content="%s">\' "$RELEASE_VERSION" "$RELEASE_COMMIT" > "$OUTPUT_DIRECTORY/frontend/index.html"\n', 0o755);
  write(root, 'build/release/build-documents.mjs', '// fixture document conversion\n');
  write(root, 'frontend/pnpm-lock.yaml', 'fixture locked dependencies');
  write(root, 'build/release/build-licenses.mjs', `import fs from 'node:fs'; import path from 'node:path';
    const output=process.argv[2]; fs.mkdirSync(output, {recursive:true});
    if (process.argv[3] === 'frontend') {
      const dir=path.join(output,'node/fixture@1'); fs.mkdirSync(dir,{recursive:true});
      fs.writeFileSync(path.join(dir,'LICENSE'),'fixture'); fs.writeFileSync(path.join(dir,'package.json'),'{}');
      fs.writeFileSync(path.join(output,'dependencies.json'),JSON.stringify([{kind:'node',name:'fixture',version:'1',notices:['LICENSE']}]));
      fs.writeFileSync(path.join(output,'react-dnd.LICENSE'),'fixture');
    } else fs.writeFileSync(path.join(output,'THIRD_PARTY_NOTICES'),'fixture');
  `);
  git('add', '.');
  const tree = git('write-tree');
  const commit = execFileSync('git', ['-C', root, '-c', 'user.name=Packaging Test', '-c', 'user.email=packaging@example.invalid', 'commit-tree', tree],
    { encoding: 'utf8', input: 'Isolated fixture\n' }).trim();
  git('update-ref', 'HEAD', commit);
  return { root, commit, temporaryRoot, git };
}

for (const dirty of ['tracked', 'staged', 'untracked', 'deleted']) {
  test(`reject ${dirty} source before extracting a release`, t => {
    const { root, commit, temporaryRoot, git } = fixture(t);
    if (dirty === 'untracked') write(root, 'e2e/ltfs-file-backend/local.sh', 'fixture');
    else if (dirty === 'deleted') fs.unlinkSync(path.join(root, 'previewworker/main.go'));
    else {
      write(root, 'previewworker/main.go', 'changed fixture');
      if (dirty === 'staged') git('add', 'previewworker/main.go');
    }
    const output = path.join(temporaryRoot, 'source');
    assert.throws(() => committedInputs(root, output, commit), /inputs must be clean/);
    assert(!fs.existsSync(output));
  });
}
test('reject commit mismatch against actual HEAD', t => {
  const { root, temporaryRoot } = fixture(t);
  assert.throws(() => committedInputs(root, path.join(temporaryRoot, 'source'), 'f'.repeat(40)), /must match actual HEAD/);
});
test('ignored local files are permitted and excluded from committed assembly', t => {
  const { root, commit, temporaryRoot } = fixture(t);
  for (const name of ['.local/private.txt', 'output/private.txt', 'e2e/ltfs-file-backend/private.log',
    'previewworker/local.tmp', 'previewworker/vendor/extra.go']) write(root, name, 'local fixture');
  const source = path.join(temporaryRoot, 'source');
  assert.equal(committedInputs(root, source), commit);
  for (const name of ['.local', 'output', 'e2e/ltfs-file-backend/private.log', 'previewworker/local.tmp', 'previewworker/vendor']) {
    assert(!fs.existsSync(path.join(source, name)), name);
  }
  const corresponding = path.join(temporaryRoot, 'corresponding');
  copyPreviewSources(source, corresponding);
  assert.deepEqual([...directoryFiles(corresponding).keys()].sort(), [...yatmSourceFiles].sort());
});
test('main builder includes only committed runtime adapters, excluding tests and ignored files', t => {
  const { root, commit, temporaryRoot } = fixture(t);
  write(root, 'e2e/ltfs-file-backend/private.log', 'local fixture');
  write(root, '.local/private.txt', 'local fixture');
  const releases = path.join(temporaryRoot, 'releases');
  const result = spawnSync('bash', [path.join(root, 'build/release/build.sh')], { cwd: temporaryRoot, encoding: 'utf8',
    env: { ...process.env, GOOS: 'darwin', RELEASE_VERSION: version, TARGET_NAME: 'fixture', RELEASE_COMMIT: commit, RELEASE_DIRECTORY: releases } });
  assert.equal(result.status, 0, result.stderr);
  const { files } = readArchive(path.join(releases, `yatm-fixture-${version}.tar.gz`));
  assert.equal(files.get('templates/testing/ltfs-file-backend/mount').toString(), 'committed fixture');
  assert(!files.has('templates/testing/ltfs-file-backend/private.log'));
  assert(!files.has('templates/testing/ltfs-file-backend/mount.test.mjs'));
  assert.equal(files.get('COMMIT').toString().trim(), commit);
});
for (const builder of ['build/release/build.sh', 'previewworker/build/build.sh']) {
  test(`${builder} rejects dirty source and mismatched commit before build tools run`, t => {
    const { root, commit, temporaryRoot } = fixture(t);
    const tools = path.join(temporaryRoot, 'tools');
    const marker = path.join(temporaryRoot, 'go-invoked');
    write(tools, 'go', `#!/usr/bin/env bash\ntouch '${marker}'\nexit 99\n`, 0o755);
    const env = { ...process.env, PATH: `${tools}:${process.env.PATH}`, RELEASE_VERSION: version, TARGET_NAME: 'fixture',
      RELEASE_DIRECTORY: path.join(temporaryRoot, 'releases'), TMPDIR: temporaryRoot, RELEASE_COMMIT: 'f'.repeat(40) };
    let result = spawnSync('bash', [path.join(root, builder)], { env, encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /must match actual HEAD/);
    write(root, 'e2e/ltfs-file-backend/extra.sh', 'fixture');
    result = spawnSync('bash', [path.join(root, builder)], { env: { ...env, RELEASE_COMMIT: commit }, encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /inputs must be clean/);
    assert(!fs.existsSync(marker));
  });
}
test('Git-free Preview rebuild requires explicit source directory and commit', t => {
  const root = temporary(t);
  const repo = path.join(root, 'yatm');
  fs.mkdirSync(repo);
  assert.throws(() => committedInputs(repo, path.join(root, 'out')), /requires PREVIEW_SOURCE_DIRECTORY and explicit RELEASE_COMMIT/);
});

// Native archives are large licensed inputs, supplied explicitly for the offline regression.
const nativeDirectory = process.env.YATM_TEST_PREVIEW_SOURCE_DIRECTORY;
test('Git-free Preview rebuild beneath a checkout preserves verified provenance, vendor and full module sources', { skip: !nativeDirectory }, t => {
  const root = temporary(t);
  execFileSync('git', ['init', '-q', root]);
  const sources = path.join(root, 'sources');
  fs.mkdirSync(sources);
  const commit = 'a'.repeat(40);
  for (const name of yatmSourceFiles) write(sources, `yatm/${name}`, fs.readFileSync(path.join(repository, name)), fs.statSync(path.join(repository, name)).mode);
  for (const name of nativeSources.keys()) write(sources, name, fs.readFileSync(path.join(nativeDirectory, name)));
  fs.cpSync(path.join(nativeDirectory, 'modules'), path.join(sources, 'modules'), { recursive: true });
  fs.cpSync(path.join(nativeDirectory, 'yatm/previewworker/vendor'), path.join(sources, 'yatm/previewworker/vendor'), { recursive: true });
  write(sources, 'ffmpeg-config.mak', 'fixture configuration');
  write(sources, 'COMMIT', `${commit}\n`);
  write(sources, 'SHA256SUMS', sourceChecksums(directoryFiles(sources)));
  validatePreviewSources(directoryFiles(sources), commit);
  const output = path.join(root, 'out');
  assert.equal(committedInputs(path.join(sources, 'yatm'), output, commit, sources), commit);
  assert(fs.existsSync(path.join(output, 'previewworker/vendor/modules.txt')));
  assert(!fs.existsSync(path.join(output, '.git')));
  assert.throws(() => committedInputs(path.join(sources, 'yatm'), path.join(root, 'wrong'), 'b'.repeat(40), sources), /source commit mismatch/);
  write(sources, 'yatm/previewworker/extra.go', 'local fixture');
  assert.throws(() => committedInputs(path.join(sources, 'yatm'), path.join(root, 'dirty'), commit, sources), /members do not match SHA256SUMS/);
});
