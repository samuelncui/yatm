import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { checkMemberPath } from './archive-members.mjs';

export const nativeSources = new Map([
  ['libwebp-1.6.0.tar.gz', 'e4ab7009bf0629fd11982d4c2aa83964cf244cffba7347ecd39019a9e38c4564'],
  ['ffmpeg-8.0.tar.xz', 'b2751fccb6cc4c77708113cd78b561059b6fa904b24162fa0be2d60273d27b8e'],
  ['zlib-1.3.1.tar.gz', '9a93b2b7dfdac77ceba5a558a580e74667dd6fede4585b91eefb60f03b72df23'],
  ['jpegsrc.v9f.tar.gz', '04705c110cb2469caa79fb71fba3d7bf834914706e9641a4589485c1f832565b'],
  ['LibRaw-0.22.2.tar.gz', 'de86b035655accff8d4010f1a221fdf50d353cb7b1422ba26f14a0db92612cfa'],
]);

export const yatmSourceFiles = [
  'LICENSE', 'go.mod', 'go.sum', 'internal/previewprotocol/protocol.go',
  'build/release/committed-inputs.mjs', 'build/release/check-preview-sources.mjs', 'build/release/archive-members.mjs',
  'build/release/sanitize-native.mjs',
  'build/release/check-preview-fixtures.mjs',
  'build/release/native-notices.mjs',
  ...['README.md', 'go.mod', 'go.sum', 'main.go', 'decode.go', 'generate.go', 'generate_test.go',
    'render.go', 'raw.go', 'raw_test.go', 'native.go', 'build/build.sh', 'build/README.md',
    'build/ffmpeg-sysctl-header.patch', 'build/package-sources.mjs', 'build/native-notices.mjs',
    'build/fetch-raw-fixtures.mjs', 'testdata/raw-fixtures.json'].map(name => `previewworker/${name}`),
];

export function privateSourcePath(name) {
  return /(^|\/)(?:\.git|\.local|node_modules|\.env(?:\..*)?|config\.ya?ml|run\.log(?:\..*)?)(\/|$)/i.test(name)
    || /(?:\.log|\.tmp|\.temp|\.bak|~)$/i.test(name);
}

export function sourceChecksums(files) {
  return [...files].filter(([name]) => name !== 'SHA256SUMS').sort(([a], [b]) => a.localeCompare(b))
    .map(([name, bytes]) => `${crypto.createHash('sha256').update(bytes).digest('hex')}  ${name}\n`).join('');
}

// SHA256SUMS owns exact source membership as well as bytes; module ownership comes from Go's vendor manifest.
export function validateSourceInventory(files, commit) {
  assert.match(commit, /^[a-f0-9]{40}$/, 'Invalid source commit');
  assert.equal(files.get('COMMIT')?.toString().trim(), commit, 'Corresponding source commit mismatch');
  const inventory = new Map();
  for (const line of (files.get('SHA256SUMS')?.toString() || '').trim().split('\n')) {
    const record = line.match(/^([a-f0-9]{64})  (.+)$/);
    assert(record, 'Invalid corresponding-source checksum record');
    checkMemberPath(record[2]);
    assert(record[2] !== 'SHA256SUMS' && !inventory.has(record[2]), 'Duplicate or self-referencing source checksum');
    inventory.set(record[2], record[1]);
  }
  assert.deepEqual([...files.keys()].filter(name => name !== 'SHA256SUMS').sort(), [...inventory.keys()].sort(),
    'Corresponding source members do not match SHA256SUMS');
  for (const [name, digest] of inventory) {
    assert.equal(crypto.createHash('sha256').update(files.get(name)).digest('hex'), digest, `Corresponding source checksum mismatch: ${name}`);
  }
  return inventory;
}

export function validatePreviewSources(files, commit, dependencies) {
  const inventory = validateSourceInventory(files, commit);
  checkPreviewSourceOwnership(files, dependencies);
  for (const [name, digest] of nativeSources) assert.equal(inventory.get(name), digest, `Native source checksum mismatch: ${name}`);
}

export function checkPreviewSourceOwnership(files, dependencies) {
  const required = ['COMMIT', 'ffmpeg-config.mak', ...nativeSources.keys(), ...yatmSourceFiles.map(name => `yatm/${name}`),
    'yatm/previewworker/vendor/modules.txt'];
  for (const name of required) assert(files.has(name), `Missing corresponding source: ${name}`);

  const goMod = files.get('yatm/previewworker/go.mod').toString();
  const modules = new Map();
  const vendor = files.get('yatm/previewworker/vendor/modules.txt').toString();
  for (const line of vendor.split('\n')) {
    const match = line.match(/^# (\S+) (v\S+)(?: => .+)?$/);
    if (!match || match[1] === 'github.com/samuelncui/yatm') continue;
    assert(goMod.includes(`${match[1]} ${match[2]}`), `Unowned vendored module: ${match[1]}`);
    modules.set(match[1], match[2]);
  }
  assert(modules.size, 'Missing vendored module ownership');
  if (dependencies) {
    assert.deepEqual(dependencies.map(({ name, version }) => `${name}@${version}`).sort(),
      [...modules].map(([name, version]) => `${name}@${version}`).sort(), 'Helper/source module ownership mismatch');
  }
  const owned = new Set(required.concat('SHA256SUMS'));
  for (const name of files.keys()) {
    assert(!privateSourcePath(name), `Private/development corresponding source: ${name}`);
    if (owned.has(name)) continue;
    const module = [...modules].some(([owner, version]) => name.startsWith(`modules/${owner}@${version}/`)
      || name.startsWith(`yatm/previewworker/vendor/${owner}/`));
    const protocol = ['internal/previewprotocol/protocol.go', 'LICENSE'].some(relative =>
      name === `yatm/previewworker/vendor/github.com/samuelncui/yatm/${relative}`);
    assert(module || protocol, `Unexpected corresponding source: ${name}`);
  }
  for (const [name, version] of modules) {
    assert([...files.keys()].some(member => member.startsWith(`modules/${name}@${version}/`)), `Missing module corresponding source: ${name}`);
    assert([...files.keys()].some(member => member.startsWith(`yatm/previewworker/vendor/${name}/`)), `Missing vendored source: ${name}`);
  }
}
