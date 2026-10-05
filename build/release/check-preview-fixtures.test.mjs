import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { checkPreviewFixtures } from './check-preview-fixtures.mjs';

test('missing fixture generator fails acceptance instead of skipping it', () => {
  assert.throws(() => checkPreviewFixtures('/nonexistent/yatm-test-ffmpeg'), /fixture preflight failed.*H.264/);
});

test('an installed fixture generator without the required encoder fails acceptance', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-ffmpeg-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const executable = path.join(root, 'ffmpeg');
  fs.writeFileSync(executable, '#!/bin/sh\necho "Unknown encoder libx264" >&2\nexit 1\n', { mode: 0o755 });
  assert.throws(() => checkPreviewFixtures(executable), /Unknown encoder libx264/);
});
