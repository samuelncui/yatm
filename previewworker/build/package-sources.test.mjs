import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';

const fixtureModule = 'example.com/fixture';
const fixtureVersion = 'v1.0.0';

function crc32(bytes) {
  let value = 0xffffffff;
  for (const byte of bytes) {
    value ^= byte;
    for (let bit = 0; bit < 8; bit++) value = value & 1 ? (value >>> 1) ^ 0xedb88320 : value >>> 1;
  }
  return (value ^ 0xffffffff) >>> 0;
}

function uint16(value) {
  const bytes = Buffer.alloc(2);
  bytes.writeUInt16LE(value);
  return bytes;
}

function uint32(value) {
  const bytes = Buffer.alloc(4);
  bytes.writeUInt32LE(value);
  return bytes;
}

function zip(entries) {
  const local = [];
  const central = [];
  let offset = 0;
  for (const [name, value] of entries) {
    const fileName = Buffer.from(name);
    const content = Buffer.from(value);
    const crc = crc32(content);
    const header = Buffer.concat([Buffer.from([0x50, 0x4b, 0x03, 0x04]), uint16(20), uint16(0), uint16(0), uint16(0), uint16(0),
      uint32(crc), uint32(content.length), uint32(content.length), uint16(fileName.length), uint16(0), fileName, content]);
    local.push(header);
    central.push(Buffer.concat([Buffer.from([0x50, 0x4b, 0x01, 0x02]), uint16(20), uint16(20), uint16(0), uint16(0), uint16(0), uint16(0),
      uint32(crc), uint32(content.length), uint32(content.length), uint16(fileName.length), uint16(0), uint16(0), uint16(0), uint16(0),
      uint32(0), uint32(offset), fileName]));
    offset += header.length;
  }
  const directory = Buffer.concat(central);
  return Buffer.concat([...local, directory, Buffer.from([0x50, 0x4b, 0x05, 0x06]), uint16(0), uint16(0), uint16(entries.length),
    uint16(entries.length), uint32(directory.length), uint32(offset), uint16(0)]);
}

function run(command, args, options) {
  const result = spawnSync(command, args, { encoding: 'utf8', ...options });
  assert.equal(result.error, undefined, result.error?.message);
  return result;
}

test('package sources verifies module inputs and preserves offline guide links', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-package-sources-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));

  const proxy = path.join(root, 'proxy', fixtureModule, '@v');
  const worker = path.join(root, 'worker');
  const build = path.join(worker, 'build');
  const cache = path.join(root, 'module-cache');
  const goCache = path.join(root, 'go-cache');
  fs.mkdirSync(proxy, { recursive: true });
  fs.mkdirSync(build, { recursive: true });
  fs.writeFileSync(path.join(proxy, `${fixtureVersion}.info`), '{"Version":"v1.0.0","Time":"2000-01-01T00:00:00Z"}\n');
  fs.writeFileSync(path.join(proxy, `${fixtureVersion}.mod`), `module ${fixtureModule}\n\ngo 1.23.0\n`);
  fs.writeFileSync(path.join(proxy, 'list'), `${fixtureVersion}\n`);
  fs.writeFileSync(path.join(proxy, `${fixtureVersion}.zip`), zip([
    [`${fixtureModule}@${fixtureVersion}/go.mod`, `module ${fixtureModule}\n\ngo 1.23.0\n`],
    [`${fixtureModule}@${fixtureVersion}/fixture.go`, 'package fixture\n'],
    [`${fixtureModule}@${fixtureVersion}/LICENSE`, 'Synthetic fixture license.\n'],
  ]));
  fs.writeFileSync(path.join(worker, 'go.mod'), `module example.com/worker\n\ngo 1.23.0\n\nrequire ${fixtureModule} ${fixtureVersion}\n`);
  fs.writeFileSync(path.join(worker, 'worker.go'), `package worker\n\nimport _ "${fixtureModule}"\n`);
  fs.copyFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), 'package-sources.mjs'), path.join(build, 'package-sources.mjs'));
  fs.mkdirSync(path.join(root, 'build/release'), { recursive: true });
  fs.copyFileSync(path.resolve(import.meta.dirname, '../../build/release/native-notices.mjs'), path.join(root, 'build/release/native-notices.mjs'));
  const guide = [
    '# Synthetic build guide',
    '[Build](build.sh)',
    '[Worker tests](../README.md#local-tests)',
    '[Release checks](../../docs/operations/testing.md#release-sop)',
    '[External](https://example.invalid/guide)',
    '[Section](#synthetic-build-guide)',
    '',
  ].join('\n');
  fs.writeFileSync(path.join(build, 'README.md'), guide);
  // The checkout has this guide, but the corresponding source does not include it.
  fs.mkdirSync(path.join(root, 'docs/operations'), { recursive: true });
  fs.writeFileSync(path.join(root, 'docs/operations/testing.md'), '# Release SOP\n');

  const environment = {
    ...process.env,
    GOCACHE: goCache,
    GOMODCACHE: cache,
    GOPROXY: pathToFileURL(path.join(root, 'proxy')).href,
    GOSUMDB: 'off',
    GOTOOLCHAIN: 'local',
  };
  const download = run('go', ['mod', 'tidy'], { cwd: worker, env: environment });
  assert.equal(download.status, 0, download.stderr);

  const cleanPackage = path.join(root, 'clean-package');
  fs.mkdirSync(cleanPackage);
  const sourceBuild = path.join(cleanPackage, 'sources/yatm/previewworker/build');
  fs.mkdirSync(sourceBuild, { recursive: true });
  fs.copyFileSync(path.join(build, 'README.md'), path.join(sourceBuild, 'README.md'));
  fs.writeFileSync(path.join(sourceBuild, 'build.sh'), '# Synthetic build script\n');
  fs.writeFileSync(path.join(sourceBuild, '../README.md'), '# Local tests\n');
  const commit = '0000000000000000000000000000000000000000';
  const copied = run('node', [path.join(build, 'package-sources.mjs'), cleanPackage, commit], { cwd: worker, env: environment });
  assert.equal(copied.status, 0, copied.stderr);
  assert(fs.existsSync(path.join(cleanPackage, 'sources', 'modules', `${fixtureModule}@${fixtureVersion}`, 'LICENSE')));
  const publicRoot = `https://github.com/samuelncui/yatm/blob/${commit}`;
  const sourceReadme = guide.replace('](../../docs/operations/testing.md#release-sop)', `](${publicRoot}/docs/operations/testing.md#release-sop)`);
  assert.equal(fs.readFileSync(path.join(sourceBuild, 'README.md'), 'utf8'), sourceReadme);
  assert.equal(fs.readFileSync(path.join(cleanPackage, 'README.md'), 'utf8'), sourceReadme
    .replace('](build.sh)', `](${publicRoot}/previewworker/build/build.sh)`)
    .replace('](../README.md#local-tests)', `](${publicRoot}/previewworker/README.md#local-tests)`));

  const moduleDirectory = execFileSync('go', ['list', '-m', '-f', '{{.Dir}}', fixtureModule], { cwd: worker, env: environment, encoding: 'utf8' }).trim();
  fs.chmodSync(moduleDirectory, 0o755);
  fs.writeFileSync(path.join(moduleDirectory, 'untracked-private-fixture.txt'), 'disposable test input\n');
  const rejectedPackage = path.join(root, 'rejected-package');
  fs.mkdirSync(rejectedPackage);
  const rejected = run('node', [path.join(build, 'package-sources.mjs'), rejectedPackage, commit], { cwd: worker, env: environment });
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /dir has been modified/);
  assert(!fs.existsSync(path.join(rejectedPackage, 'sources', 'modules', `${fixtureModule}@${fixtureVersion}`)));
});
