import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { readArchive, checkDirectories, checkMemberPath } from './archive-members.mjs';
import { validatePreviewSources, privateSourcePath } from './check-preview-sources.mjs';

export const mainTargets = ['linux-amd64', 'linux-386-experimental', 'linux-arm64-experimental', 'linux-arm6-experimental', 'linux-arm7-experimental', 'linux-s390x-experimental', 'darwin-amd64-experimental', 'darwin-arm64-experimental', 'freebsd-amd64-experimental'];
export const previewTargets = ['linux-amd64', 'darwin-amd64', 'darwin-arm64'];

export function expectedArchives(version) {
  return [...mainTargets.map(target => `yatm-${target}-${version}.tar.gz`),
    ...previewTargets.flatMap(target => [`yatm-preview-${target}-${version}.tar.gz`, `yatm-preview-source-${target}-${version}.tar.gz`])].sort();
}

export function checkArchiveNames(names, version) {
  assert.deepEqual(names.filter(name => name.endsWith('.tar.gz')).sort(), expectedArchives(version), 'Candidate set must contain exactly nine main, three helper and three source archives; ARMv5 is unsupported');
}

function checksum(archive) {
  const digest = crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
  assert.equal(fs.readFileSync(`${archive}.sha256`, 'utf8').trim(), `${digest}  ${path.basename(archive)}`, `Checksum mismatch: ${path.basename(archive)}`);
  return digest;
}

const read = (files, entry) => {
  assert(files.has(entry), `Missing helper entry: ${entry}`);
  return files.get(entry).toString().trim();
};

export function checkPreviewMembers(members, version, commit, target) {
  const { files } = members;
  const required = ['yatm-preview', ...['VERSION', 'COMMIT', 'SOURCE.txt', 'README.md', 'VALIDATION',
    'licenses/YATM-LICENSE', 'licenses/Go-LICENSE', 'licenses/COPYING.LGPLv2.1', 'licenses/LICENSE.md',
    'licenses/libwebp-COPYING', 'licenses/libwebp-PATENTS', 'licenses/zlib-LICENSE', 'licenses/libjpeg-README',
    'licenses/LibRaw-NOTICE', 'licenses/FFmpeg-NOTICE', 'licenses/libwebp-NOTICE', 'licenses/zlib-NOTICE', 'licenses/libjpeg-NOTICE', 'licenses/IJG-NOTICE',
    'licenses/COPYRIGHT', 'licenses/LICENSE.LGPL', 'licenses/LICENSE.CDDL', 'licenses/go-dependencies.json']
    .map(name => `preview-support/${name}`)];
  for (const entry of required) assert(files.has(entry), `Missing helper entry: ${entry}`);
  const allowed = new Set([...required, 'preview-support/CAPABILITIES.json',
    'preview-support/licenses/zig-runtime/Zig-LICENSE', 'preview-support/licenses/zig-runtime/compiler-rt-NOTICE',
    ...['libcxx', 'libcxxabi', 'libunwind'].map(name => `preview-support/licenses/zig-runtime/${name}-LICENSE.TXT`)]);
  const zigNotices = [...allowed].filter(name => name.startsWith('preview-support/licenses/zig-runtime/'));
  if (zigNotices.some(name => files.has(name))) {
    for (const name of zigNotices) assert(files.has(name), `Missing Zig runtime notice: ${name}`);
  }
  const dependencies = JSON.parse(read(files, 'preview-support/licenses/go-dependencies.json'));
  assert(Array.isArray(dependencies) && dependencies.length, 'Missing helper dependency ownership');
  for (const { name, version: dependencyVersion, source, notices } of dependencies) {
    assert(typeof name === 'string' && typeof dependencyVersion === 'string'
      && source === `modules/${name}@${dependencyVersion}` && Array.isArray(notices) && notices.length, 'Invalid helper dependency ownership');
    const directory = `preview-support/licenses/go/${name}@${dependencyVersion}`;
    checkMemberPath(directory);
    for (const notice of notices) {
      assert(typeof notice === 'string' && /^(licen[cs]e|notice|copying|copyright)([.-].*)?$/i.test(notice)
        && !notice.includes('/'), 'Invalid helper notice filename');
      const entry = `${directory}/${notice}`;
      allowed.add(entry);
      assert(files.has(entry), `Missing helper entry: ${entry}`);
    }
  }
  const libraries = new Set(['avcodec', 'avdevice', 'avfilter', 'avformat', 'avutil', 'swresample', 'swscale', 'webp', 'sharpyuv', 'z', 'jpeg', 'raw', 'raw_r']);
  const found = new Set();
  for (const entry of files.keys()) {
    assert(!privateSourcePath(entry), `Private/development helper entry: ${entry}`);
    if (allowed.has(entry)) continue;
    const library = target.startsWith('linux-')
      ? entry.match(/^preview-lib\/lib([a-z0-9_]+)\.so(?:\.\d+)*$/)
      : entry.match(/^preview-lib\/lib([a-z0-9_]+)(?:\.\d+)*\.dylib$/);
    assert(library && libraries.has(library[1]), `Unexpected helper entry: ${entry}`);
    found.add(library[1]);
  }
  for (const library of libraries) assert(found.has(library), `Missing helper library: ${library}`);
  checkDirectories(members);
  assert.equal(read(files, 'preview-support/VERSION'), version);
  assert.equal(read(files, 'preview-support/COMMIT'), commit);
  return dependencies;
}

export function checkPreviewPackage(archive, version, commit) {
  checksum(archive);
  const target = path.basename(archive).match(/^yatm-preview-(linux-amd64|darwin-amd64|darwin-arm64)-/)?.[1];
  assert(target, 'Invalid helper archive target');
  const members = readArchive(archive);
  const dependencies = checkPreviewMembers(members, version, commit, target);
  const source = path.join(path.dirname(archive), path.basename(archive).replace('yatm-preview-', 'yatm-preview-source-'));
  const digest = checksum(source);
  const offer = read(members.files, 'preview-support/SOURCE.txt');
  assert(offer.includes(`Corresponding source archive: ${path.basename(source)}\n`), 'Source offer filename mismatch');
  assert(offer.includes(`SHA-256: ${digest}\n`), 'Source offer checksum mismatch');
  const sourceMembers = readArchive(source);
  validatePreviewSources(sourceMembers.files, commit, dependencies);
  checkDirectories(sourceMembers);
}

export function checkCandidateSet(directory, version, commit) {
  checkArchiveNames(fs.readdirSync(directory), version);
  for (const target of mainTargets) {
    const archive = path.join(directory, `yatm-${target}-${version}.tar.gz`);
    execFileSync(process.execPath, [fileURLToPath(new URL('./check-release.mjs', import.meta.url)), archive], { stdio: 'inherit' });
    const { files } = readArchive(archive);
    assert.equal(read(files, 'VERSION'), version);
    assert.equal(read(files, 'COMMIT'), commit);
  }
  for (const target of previewTargets) checkPreviewPackage(path.join(directory, `yatm-preview-${target}-${version}.tar.gz`), version, commit);
  console.log('Validated 15 candidate archives, identities, helper boundaries and corresponding sources.');
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [directory, version, commit] = process.argv.slice(2);
  assert(directory && version && commit, 'Usage: check-candidate-set.mjs <directory> <version> <commit>');
  checkCandidateSet(directory, version, commit);
}
