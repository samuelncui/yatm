// Validate a built archive without executing cross-compiled programs.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import crypto from 'node:crypto';
import path from 'node:path';
import { readArchive, checkDirectories, checkMemberPath } from './archive-members.mjs';

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
for (const { kind, name, version, notices } of dependencies) {
  assert(['go', 'node', 'native'].includes(kind) && typeof name === 'string' && typeof version === 'string'
    && Array.isArray(notices) && notices.length, 'Invalid dependency notice ownership');
  if (kind === 'native') assert((name === 'musl' && version === 'zig-0.15.2') || (name === 'zig' && version === '0.15.2'), 'Unowned native runtime notice');
  const directory = `licenses/${kind}/${name}@${version}`;
  checkMemberPath(directory);
  for (const notice of notices) {
    assert(typeof notice === 'string' && /^(licen[cs]e|notice|copying|copyright|patents)([.-].*)?$/i.test(notice)
      && !notice.includes('/'), 'Invalid dependency notice filename');
    const entry = `${directory}/${notice}`;
    allowed.add(entry);
    assert(files.has(entry), `Required dependency notice missing: ${entry}`);
  }
  if (kind === 'node') {
    allowed.add(`${directory}/package.json`);
    assert(files.has(`${directory}/package.json`), `Required dependency metadata missing: ${directory}`);
  }
}
for (const entry of files.keys()) {
  assert(!/(^|\/)(\.git|\.local|node_modules|config\.yaml)(\/|$)|(?:\.log|\.tmp|\.temp|~)$/i.test(entry), `Development/private file in archive: ${entry}`);
  if (entry.startsWith('docs/')) assert(documents.includes(entry), `Unexpected packaged documentation: ${entry}`);
  // Vite owns the flat, hashed asset output. Source maps and arbitrary nested files are not runtime assets.
  assert(allowed.has(entry) || /^frontend\/assets\/[A-Za-z0-9_-]+-[A-Za-z0-9_-]{8}\.(?:js|css|svg)$/.test(entry), `Unexpected archive entry: ${entry}`);
}
checkDirectories(members);
const read = entry => files.get(entry).toString().trim();
const version = read('VERSION');
const commit = read('COMMIT');
assert.match(version, /^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/);
assert.match(commit, /^[a-f0-9]{40}$/);
const html = read('frontend/index.html');
assert(html.includes(`name="yatm-version" content="${version}"`), 'Frontend version does not match package');
assert(html.includes(`name="yatm-commit" content="${commit}"`), 'Frontend commit does not match package');
console.log(`Validated ${path.basename(archive)} (${files.size} files; ${commit}).`);
