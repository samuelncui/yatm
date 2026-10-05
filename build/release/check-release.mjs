// Validate a built archive without executing cross-compiled programs.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import crypto from 'node:crypto';
import path from 'node:path';
import { readArchive, checkDirectories } from './archive-members.mjs';
import { dependencyFiles, frontendFiles } from './package-assets.mjs';

const archive = process.argv[2];
assert(archive, 'usage: node check-release.mjs <archive.tar.gz>');
const checksum = fs.readFileSync(`${archive}.sha256`, 'utf8').trim();
assert.equal(checksum, `${crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex')}  ${path.basename(archive)}`);
const members = readArchive(archive);
const { files } = members;
const roots = ['yatm-httpd', 'yatm-cli', 'yatm-export-library', 'yatm-lto-info', 'yatm-migrate',
  'install-release.sh', 'VERSION', 'COMMIT', 'LICENSE', 'README.md', 'CONTEXT.md'];
const documents = ['docs/README.md', ...['install', 'migration', 'library', 'locations'].map(name => `docs/operations/${name}.md`)];
const scripts = ['encrypt', 'get_device', 'mkfs', 'mount', 'mount.openltfs', 'readinfo', 'umount'].map(name => `templates/scripts/${name}`);
const testing = ['encrypt', 'get_device', 'mkfs', 'mount', 'readinfo', 'umount', 'README.md'].map(name => `templates/testing/ltfs-file-backend/${name}`);
const required = [...roots, ...documents, ...scripts, 'frontend/index.html', 'templates/config.example.yaml',
  'templates/yatm-httpd.service', 'skills/yatm/SKILL.md', 'licenses/dependencies.json', 'licenses/THIRD_PARTY_NOTICES'];
for (const name of required) assert(files.has(name), `Required package file missing: ${name}`);
for (const name of testing) assert(files.has(name), `Required testing adapter missing: ${name}`);
const allowed = new Set([...required, ...testing, 'licenses/lto-info.LICENSE', 'licenses/react-dnd.LICENSE']);
// The existing dependency manifest owns copied notices, including Node's license metadata.
const dependencies = JSON.parse(files.get('licenses/dependencies.json').toString());
assert(Array.isArray(dependencies), 'Invalid dependency notices manifest');
if (path.basename(archive).startsWith('yatm-linux-')) {
  for (const [name, version, notices] of [['musl', 'zig-0.15.2', ['COPYRIGHT', 'NOTICE']], ['zig', '0.15.2', ['LICENSE', 'NOTICE']]]) {
    const record = dependencies.find(item => item.kind === 'native' && item.name === name);
    assert(record && record.version === version && notices.every(notice => record.notices?.includes(notice)), `Missing pinned ${name} runtime notices`);
  }
}
for (const entry of dependencyFiles(files)) allowed.add(entry);
const version = files.get('VERSION').toString().trim();
const commit = files.get('COMMIT').toString().trim();
assert.match(version, /^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/);
assert.match(commit, /^[a-f0-9]{40}$/);
for (const entry of frontendFiles(files, version, commit)) allowed.add(entry);
for (const entry of files.keys()) {
  assert(!/(^|\/)(\.git|\.local|node_modules|config\.yaml)(\/|$)|(?:\.log|\.tmp|\.temp|~)$/i.test(entry), `Development/private file in archive: ${entry}`);
  if (entry.startsWith('docs/')) assert(documents.includes(entry), `Unexpected packaged documentation: ${entry}`);
  // Vite owns the flat, hashed asset output. Source maps and arbitrary nested files are not runtime assets.
  assert(allowed.has(entry), `Unexpected archive entry: ${entry}`);
}
checkDirectories(members);
console.log(`Validated ${path.basename(archive)} (${files.size} files; ${commit}).`);
