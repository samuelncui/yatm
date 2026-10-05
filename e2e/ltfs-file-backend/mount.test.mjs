import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

const repository = path.resolve(import.meta.dirname, '../..');
const adapters = ['scripts/mount', 'scripts/mount.openltfs', 'e2e/ltfs-file-backend/mount'];

for (const adapter of adapters) {
  for (const relative of [true, false]) {
    test(`${adapter} captures the final Index with ${relative ? 'relative' : 'absolute'} Tape work paths`, t => {
      const root = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-ltfs-mount-test-')));
      t.after(() => fs.rmSync(root, { recursive: true, force: true }));
      const caller = path.join(root, 'caller directory');
      const scripts = path.join(root, 'scripts');
      const binaries = path.join(root, 'bin');
      const mountPoint = path.join(root, 'mount');
      const tapeDirectory = path.join(caller, 'work files/jobs/7/tapes/TEST01');
      for (const directory of [caller, scripts, binaries, mountPoint]) fs.mkdirSync(directory, { recursive: true });
      fs.copyFileSync(path.join(repository, adapter), path.join(scripts, 'mount'));
      const executable = (name, contents) => fs.writeFileSync(name, contents, { mode: 0o755 });
      executable(path.join(scripts, 'get_device'), '#!/usr/bin/env bash\nprintf "%s\\n" "$YATM_TEST_DEVICE"\n');
      executable(path.join(binaries, 'sleep'), '#!/usr/bin/env bash\nexit 0\n');
      executable(path.join(binaries, 'mountpoint'), '#!/usr/bin/env bash\n[[ "$1" == -q && "$2" == "$MOUNT_POINT" ]]\n');
      executable(path.join(binaries, 'df'), '#!/usr/bin/env bash\nprintf "Mounted on\\n%s\\n" "$MOUNT_POINT"\n');
      executable(path.join(binaries, 'ltfs'), `#!/usr/bin/env node
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
const options = process.argv.slice(2);
const work = options.find(option => option.startsWith('work_directory=')).slice('work_directory='.length);
assert.equal(work, process.env.YATM_TEST_TAPE_DIRECTORY);
assert(path.isAbsolute(work));
const capture = options.find(option => option === 'capture_index' || option.startsWith('capture_index='));
assert(capture);
if (capture !== 'capture_index') assert.equal(capture, 'capture_index=' + work);
// Background LTFS no longer shares the service working directory.
process.chdir('/');
fs.writeFileSync(path.join(work, 'TEST01.schema'), '<ltfsindex/>');
`);
      const result = spawnSync('bash', [path.join(scripts, 'mount')], {
        cwd: caller,
        env: {
          ...process.env,
          PATH: `${binaries}:${process.env.PATH}`,
          MOUNT_POINT: mountPoint,
          TAPE_DIR: relative ? path.relative(caller, tapeDirectory) : tapeDirectory,
          YATM_TEST_TAPE_DIRECTORY: tapeDirectory,
          YATM_TEST_DEVICE: path.join(root, 'TEST01'),
        },
        encoding: 'utf8',
        timeout: 10000,
      });
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.equal(fs.readFileSync(path.join(tapeDirectory, 'TEST01.schema'), 'utf8'), '<ltfsindex/>');
    });
  }
}
