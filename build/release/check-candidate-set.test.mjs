import assert from 'node:assert/strict';
import test from 'node:test';
import { checkArchiveNames, expectedArchives } from './check-candidate-set.mjs';

const version = 'v1.0.0-alpha.2';
test('candidate set includes matching optional source archives and excludes ARMv5', () => {
  const names = expectedArchives(version);
  assert.equal(names.length, 15);
  assert.equal(names.filter(name => name.startsWith('yatm-preview-source-')).length, 3);
  assert(!names.some(name => name.includes('arm5')));
  checkArchiveNames([...names, ...names.map(name => `${name}.sha256`), 'SHA256SUMS'], version);
});
for (const target of ['linux-amd64', 'darwin-amd64', 'darwin-arm64']) {
  test(`reject missing corresponding source for ${target}`, () => {
    assert.throws(() => checkArchiveNames(expectedArchives(version).filter(name => name !== `yatm-preview-source-${target}-${version}.tar.gz`), version), /Candidate set/);
  });
}
test('reject unexpected fallback ARMv5 archive', () => {
  assert.throws(() => checkArchiveNames([...expectedArchives(version), `yatm-linux-arm5-experimental-${version}.tar.gz`], version), /ARMv5/);
});

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { checkPreviewMembers, checkPreviewPackage } from './check-candidate-set.mjs';
import { readArchive, checkDirectories } from './archive-members.mjs';
import { checkPreviewSourceOwnership, validateSourceInventory, validatePreviewSources, nativeSources, yatmSourceFiles, sourceChecksums } from './check-preview-sources.mjs';

const commit = 'a'.repeat(40);
const repository = path.resolve(import.meta.dirname, '../..');
const modules = ['github.com/asticode/go-astiav v0.42.0', 'github.com/asticode/go-astikit v0.42.0', 'golang.org/x/image v0.36.0'];
const dependencies = modules.map(module => {
  const [name, version] = module.split(' ');
  return { name, version, source: `modules/${name}@${version}`, notices: ['LICENSE'] };
});
function helperFiles(target = 'linux-amd64') {
  return new Map([
    ['yatm-preview', Buffer.from('fixture')],
    ...['README.md', 'VALIDATION', 'SOURCE.txt', 'licenses/YATM-LICENSE', 'licenses/Go-LICENSE',
      'licenses/COPYING.LGPLv2.1', 'licenses/LICENSE.md', 'licenses/libwebp-COPYING', 'licenses/libwebp-PATENTS',
      'licenses/zlib-LICENSE', 'licenses/libjpeg-README', 'licenses/COPYRIGHT', 'licenses/LICENSE.LGPL', 'licenses/LICENSE.CDDL']
      .concat(['licenses/LibRaw-NOTICE', 'licenses/FFmpeg-NOTICE', 'licenses/libwebp-NOTICE', 'licenses/zlib-NOTICE', 'licenses/libjpeg-NOTICE', 'licenses/IJG-NOTICE'])
      .map(name => [`preview-support/${name}`, Buffer.from('fixture')]),
    ['preview-support/VERSION', Buffer.from(version)], ['preview-support/COMMIT', Buffer.from(commit)],
    ['preview-support/licenses/go-dependencies.json', Buffer.from(JSON.stringify(dependencies))],
    ...dependencies.map(({ name, version }) => [`preview-support/licenses/go/${name}@${version}/LICENSE`, Buffer.from('fixture')]),
    ...['avcodec', 'avdevice', 'avfilter', 'avformat', 'avutil', 'swresample', 'swscale', 'webp', 'sharpyuv', 'z', 'jpeg', 'raw', 'raw_r']
      .map(name => [`preview-lib/lib${name}${target.startsWith('linux') ? '.so.1' : '.1.dylib'}`, Buffer.from('fixture')]),
  ]);
}
function sourceFiles() {
  const files = new Map([
    ['COMMIT', Buffer.from(commit)], ['ffmpeg-config.mak', Buffer.from('fixture')],
    ...[...nativeSources.keys()].map(name => [name, Buffer.from('fixture')]),
    ...yatmSourceFiles.map(name => [`yatm/${name}`, fs.readFileSync(path.join(repository, name))]),
    ['yatm/previewworker/vendor/modules.txt', Buffer.from(modules.map(module => `# ${module}\n## explicit; go 1.26.8\n`).join(''))],
    ...dependencies.flatMap(({ name, version }) => [
      [`modules/${name}@${version}/LICENSE`, Buffer.from('fixture')],
      [`yatm/previewworker/vendor/${name}/fixture.go`, Buffer.from('fixture')],
    ]),
  ]);
  files.set('SHA256SUMS', Buffer.from(sourceChecksums(files)));
  return files;
}
function archive(t, files, basename = 'fixture.tar.gz', root) {
  root ||= fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-preview-boundary-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const content = path.join(root, `${basename}.content`);
  for (const [name, bytes] of files) {
    fs.mkdirSync(path.dirname(path.join(content, name)), { recursive: true });
    fs.writeFileSync(path.join(content, name), bytes);
  }
  const output = path.join(root, basename);
  execFileSync('tar', ['--no-xattrs', '--no-acls', '-czf', output, '-C', content, '.'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
  fs.writeFileSync(`${output}.sha256`, `${crypto.createHash('sha256').update(fs.readFileSync(output)).digest('hex')}  ${basename}\n`);
  return output;
}
for (const target of ['linux-amd64', 'darwin-amd64', 'darwin-arm64']) {
  test(`accepts owned helper members for ${target}`, t => {
    const members = readArchive(archive(t, helperFiles(target)));
    assert.deepEqual(checkPreviewMembers(members, version, commit, target), dependencies);
  });
}
for (const name of ['preview-lib/local.txt', 'preview-lib/libunowned.so.1', 'preview-lib/nested/libavcodec.so.1',
  'preview-support/private.txt', 'preview-support/licenses/extra.txt',
  'preview-support/licenses/go/github.com/asticode/go-astiav@v0.42.0/extra.txt',
  'preview-support/config.yaml', 'preview-support/run.log', 'preview-support/local.tmp', 'preview-support/.git/HEAD']) {
  test(`rejects helper member ${name}`, t => {
    const files = helperFiles();
    files.set(name, Buffer.from('fixture'));
    assert.throws(() => checkPreviewMembers(readArchive(archive(t, files)), version, commit, 'linux-amd64'), /Unexpected helper entry|Private\/development helper entry/);
  });
}
test('rejects missing helper private library', () => {
  const files = helperFiles();
  files.delete('preview-lib/libraw.so.1');
  assert.throws(() => checkPreviewMembers({ files, directories: new Set() }, version, commit, 'linux-amd64'), /Missing helper library/);
});
for (const name of ['LibRaw-NOTICE', 'FFmpeg-NOTICE', 'libwebp-NOTICE', 'zlib-NOTICE', 'libjpeg-NOTICE', 'IJG-NOTICE']) {
  test(`rejects missing transitive native notice ${name}`, () => {
    const files = helperFiles();
    files.delete(`preview-support/licenses/${name}`);
    assert.throws(() => checkPreviewMembers({ files, directories: new Set() }, version, commit, 'linux-amd64'), /Missing helper entry/);
  });
}
test('rejects partial Zig C++ runtime notices', () => {
  const files = helperFiles();
  files.set('preview-support/licenses/zig-runtime/libcxx-LICENSE.TXT', Buffer.from('fixture'));
  assert.throws(() => checkPreviewMembers({ files, directories: new Set() }, version, commit, 'linux-amd64'), /Missing Zig runtime notice/);
});
test('rejects helper links before member allowlisting', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-preview-link-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.symlinkSync('/tmp', path.join(root, 'preview-lib'));
  const output = path.join(root, 'fixture.tar.gz');
  execFileSync('tar', ['--no-xattrs', '--no-acls', '-czf', output, '-C', root, 'preview-lib'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
  assert.throws(() => readArchive(output), /ordinary files and directories/);
});
test('rejects unexpected empty package directories', () => {
  assert.throws(() => checkDirectories({ files: helperFiles(), directories: new Set(['preview-support/private']) }), /Unexpected archive directory/);
});
test('accepts generated source membership and checksum inventory', () => {
  const files = sourceFiles();
  validateSourceInventory(files, commit);
  checkPreviewSourceOwnership(files, dependencies);
  assert.throws(() => validatePreviewSources(files, commit, dependencies), /Native source checksum mismatch/);
});
for (const name of ['yatm/previewworker/extra.go', 'yatm/previewworker/build/local.sh', 'yatm/internal/previewprotocol/extra.go',
  'modules/unowned@v1.0.0/file.go', 'yatm/previewworker/vendor/unowned/file.go', 'yatm/previewworker/config.yaml',
  'yatm/previewworker/run.log', 'yatm/previewworker/local.tmp', 'yatm/previewworker/.git/HEAD']) {
  test(`rejects unowned corresponding source ${name}, including when checksummed`, () => {
    const files = sourceFiles();
    files.set(name, Buffer.from('fixture'));
    assert.throws(() => validateSourceInventory(files, commit), /members do not match SHA256SUMS/);
    files.set('SHA256SUMS', Buffer.from(sourceChecksums(files)));
    assert.throws(() => validatePreviewSources(files, commit, dependencies), /Unexpected corresponding source|Private\/development corresponding source/);
  });
}
test('rejects changed corresponding-source bytes and supplied commit mismatch', () => {
  const files = sourceFiles();
  files.set('yatm/previewworker/main.go', Buffer.from('changed fixture'));
  assert.throws(() => validateSourceInventory(files, commit), /source checksum mismatch/);
  assert.throws(() => validateSourceInventory(files, 'b'.repeat(40)), /source commit mismatch/);
});
test('rejects missing generated vendor input and mismatched dependency ownership', () => {
  const files = sourceFiles();
  assert.throws(() => checkPreviewSourceOwnership(files, dependencies.slice(1)), /module ownership mismatch/);
  files.delete('yatm/previewworker/vendor/modules.txt');
  assert.throws(() => checkPreviewSourceOwnership(files, dependencies), /Missing corresponding source/);
});
const nativeDirectory = process.env.YATM_TEST_PREVIEW_SOURCE_DIRECTORY;
test('validates paired helper/source archives with pinned native inputs and rejects added source members', { skip: !nativeDirectory }, t => {
  const files = sourceFiles();
  for (const name of nativeSources.keys()) files.set(name, fs.readFileSync(path.join(nativeDirectory, name)));
  files.set('SHA256SUMS', Buffer.from(sourceChecksums(files)));
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-preview-pair-test-'));
  const sourceName = `yatm-preview-source-linux-amd64-${version}.tar.gz`;
  const source = archive(t, files, sourceName, root);
  const sourceDigest = crypto.createHash('sha256').update(fs.readFileSync(source)).digest('hex');
  const helper = helperFiles();
  helper.set('preview-support/SOURCE.txt', Buffer.from(`Corresponding source archive: ${sourceName}\nSHA-256: ${sourceDigest}\nDownload: fixture\n`));
  const binary = archive(t, helper, `yatm-preview-linux-amd64-${version}.tar.gz`, root);
  checkPreviewPackage(binary, version, commit);
  files.set('yatm/previewworker/extra.go', Buffer.from('fixture'));
  archive(t, files, sourceName, root);
  const alteredDigest = crypto.createHash('sha256').update(fs.readFileSync(source)).digest('hex');
  helper.set('preview-support/SOURCE.txt', Buffer.from(`Corresponding source archive: ${sourceName}\nSHA-256: ${alteredDigest}\nDownload: fixture\n`));
  archive(t, helper, path.basename(binary), root);
  assert.throws(() => checkPreviewPackage(binary, version, commit), /members do not match SHA256SUMS/);
});
