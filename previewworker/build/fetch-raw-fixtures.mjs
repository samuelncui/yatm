// Camera bytes stay outside source and release packages; only public provenance is committed.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

assert(process.argv[2], 'Usage: fetch-raw-fixtures.mjs <external-fixture-directory>');
const destination = path.resolve(process.argv[2]);
const fixtures = JSON.parse(fs.readFileSync(new URL('../testdata/raw-fixtures.json', import.meta.url), 'utf8'));
const checksum = filename => createHash('sha256').update(fs.readFileSync(filename)).digest('hex');
fs.mkdirSync(destination, { recursive: true });
const temporary = fs.mkdtempSync(path.join(destination, '.tmp_raw_'));
try {
  for (const fixture of fixtures) {
    assert.equal(fixture.license, 'CC0-1.0');
    assert.equal(path.basename(fixture.path), fixture.path);
    assert.match(fixture.sha256, /^[a-f0-9]{64}$/);
    assert.equal(new URL(fixture.url).origin, 'https://raw.pixls.us');
    const output = path.join(destination, fixture.path);
    if (!fs.existsSync(output)) {
      const downloaded = path.join(temporary, fixture.path);
      execFileSync('curl', ['--fail', '--location', '--silent', '--show-error', '--proto', '=https',
        '--proto-redir', '=https', '--max-time', '120', '--max-filesize', '67108864',
        '--output', downloaded, fixture.url], { stdio: 'inherit', timeout: 125_000 });
      assert.equal(checksum(downloaded), fixture.sha256, `RAW fixture checksum mismatch: ${fixture.path}`);
      fs.renameSync(downloaded, output);
    }
    assert.equal(checksum(output), fixture.sha256, `RAW fixture checksum mismatch: ${fixture.path}`);
    fixture.path = output;
  }
  fs.writeFileSync(path.join(destination, 'manifest.json'), JSON.stringify(fixtures, null, 2) + '\n');
  console.log(`Verified ${fixtures.length} CC0 RAW fixtures.`);
} finally {
  fs.rmSync(temporary, { recursive: true, force: true });
}
