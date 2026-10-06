import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

test('legacy fresh installation completes after starting its service without a v1 inspector', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-legacy-install-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  // Exercise the actual legacy inspection, readiness and completion branches; isolate host mutations.
  const result = spawnSync('bash', ['-c', `
source "$1"
INSTALL_DIRECTORY="$2/install"
TEST_ROOT="$2"
RELEASE_VERSION=v0.1.21
CURRENT_VERSION=none
LEGACY=1
REPORT_FILE="$2/report"
identify_platform() { :; }
reject_ambiguous_fresh_root() { :; }
reject_pending_migration() { :; }
resolve_version() { :; }
verify_service_ownership() { :; }
begin_attempt() { WORK_DIRECTORY="$TEST_ROOT/work"; mkdir "$WORK_DIRECTORY"; }
download_release() { :; }
prepare_preview() { :; }
prepare_fresh_config() { :; }
plan_configuration() { :; }
install_managed_files() { echo fixture-installed; }
systemctl() { echo "fixture-service:$1"; }
clean_old_upgrades() { :; }
clean_work_directory() { :; }
offer_skill() { :; }
main
`, 'installer-test', path.resolve(import.meta.dirname, '../../install-release.sh'), root], {
    encoding: 'utf8', input: 'y\n',
  });
  assert.equal(result.status, 0, result.stdout + result.stderr);
  assert.match(result.stdout, /fixture-installed/);
  assert.match(result.stdout, /fixture-service:start/);
  assert.match(result.stdout, /Legacy release started/);
  assert.match(result.stdout, /Installed v0.1.21/);
  assert.doesNotMatch(result.stdout + result.stderr, /unbound variable|did not complete/);
});

test('failed read-only preflight never starts an installer attempt or changes the installation', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-install-preflight-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  fs.writeFileSync(path.join(root, 'VERSION'), 'v0.1.21');
  const result = spawnSync('bash', ['-c', `
source "$1"
INSTALL_DIRECTORY="$2"
CURRENT_VERSION=v0.1.21
RELEASE_VERSION=v1.0.0-alpha.2
identify_platform() { :; }
reject_ambiguous_fresh_root() { :; }
reject_pending_migration() { :; }
resolve_version() { :; }
download_release() { :; }
prepare_preview() { :; }
inspect_installation() { fail 'fixture: nonrelocatable absolute link'; }
begin_attempt() { touch "$INSTALL_DIRECTORY/unexpected-attempt"; }
systemctl() { touch "$INSTALL_DIRECTORY/unexpected-service"; }
main
`, 'installer-test', path.resolve(import.meta.dirname, '../../install-release.sh'), root], { encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /nonrelocatable absolute link/);
  assert.deepEqual(fs.readdirSync(root), ['VERSION']);
  assert.equal(fs.readFileSync(path.join(root, 'VERSION'), 'utf8'), 'v0.1.21');
});

test('legacy and Alpha upgrades show the actionable Tape adapter notice before consent', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-tape-upgrade-notice-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  // The guide comes from the candidate, while host actions are isolated behind stand-ins.
  const repository = path.resolve(import.meta.dirname, '../..');
  const guide = path.join(root, 'docs/operations/migration.md');
  fs.mkdirSync(path.dirname(guide), { recursive: true });
  fs.copyFileSync(path.join(repository, 'docs/operations/migration.md'), guide);
  for (const current of ['v0.1.21', 'v1.0.0-alpha.1', 'none']) {
    for (const checkOnly of ['0', '1']) {
      const result = spawnSync('bash', ['-c', `
source "$1"
CURRENT_VERSION="$3"
RELEASE_VERSION=v1.0.0-alpha.2
RELEASE_DIRECTORY="$2"
CHECK_ONLY="$4"
show_migration_guide
echo fixture-before-consent
`, 'installer-test', path.join(repository, 'install-release.sh'), root, current, checkOnly], { encoding: 'utf8' });
      assert.equal(result.status, 0, result.stdout + result.stderr);
      if (current === 'none') {
        assert.doesNotMatch(result.stdout, /Tape adapter check required/);
        continue;
      }
      assert.match(result.stdout, /completed final Index at TAPE_DIR\/<barcode>\.schema/);
      assert.match(result.stdout, /Custom scripts are retained/);
      assert.match(result.stdout, /Keep legacy captures through migration/);
      assert.match(result.stdout, /adapt mount output.*device release in unmount/);
      assert(result.stdout.indexOf('Tape adapter check required') < result.stdout.indexOf('fixture-before-consent'));
      if (current === 'v0.1.21' && checkOnly === '0') {
        assert.match(result.stdout, /Action required before the first Tape Job/);
      }
    }
  }
});
