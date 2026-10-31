import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import test from 'node:test';
import { publicBuildMetadata, publicBuildRoot, publicFFmpegConfiguration } from './sanitize-native.mjs';

test('public FFmpeg configuration preserves flags and license choices while mapping only the build root', () => {
  const buildRoot = path.join('/', 'private', 'var', 'folders', 'fixture', 'native-build');
  const external = path.join('/', 'home', 'fixture', 'external-sdk');
  const configuration = `--prefix=${buildRoot}/native --enable-shared --disable-static --disable-network --enable-libwebp --disable-debug --extra-cflags=-ffile-prefix-map=${buildRoot}=${publicBuildRoot}`;
  const header = `#define FFMPEG_CONFIGURATION "${configuration}"\n#define EXTERNAL_SDK "${external}"\n#define CONFIG_GPL 0\n#define CONFIG_NONFREE 0\n`;
  const output = publicFFmpegConfiguration(header, buildRoot);
  assert(!output.includes(buildRoot), 'Ephemeral root remains in public configure metadata');
  assert(output.includes(external), 'Unrelated configuration was replaced');
  for (const flag of ['--enable-shared', '--disable-static', '--disable-network', '--enable-libwebp', '--disable-debug',
    '#define CONFIG_GPL 0', '#define CONFIG_NONFREE 0']) assert(output.includes(flag), 'Configure semantics changed');
  assert(output.includes(`--prefix=${publicBuildRoot}/native`), 'Public configure prefix is missing');
  assert(publicBuildMetadata(output, buildRoot) === output, 'Public metadata is not stable');
  assert(publicBuildMetadata(`${buildRoot}-external`, buildRoot) === `${buildRoot}-external`, 'An unrelated path prefix was replaced');
});

test('distributed configure copy is sanitized after compilation without changing the build-used config', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-native-metadata-test-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const input = path.join(root, 'config.mak');
  const output = path.join(root, 'distributed.mak');
  const original = `prefix=${root}/native\nCFLAGS=-I${root}/native/include -g0\nEXTRALIBS-avcodec=-L${root}/native/lib -lwebp -lz\nLICENSE=LGPL version 2.1 or later\n`;
  fs.writeFileSync(input, original);
  execFileSync(process.execPath, [path.join(import.meta.dirname, 'sanitize-native.mjs'), 'copy', input, output, root]);
  assert(fs.readFileSync(input, 'utf8') === original, 'Build-used configuration was changed');
  const distributed = fs.readFileSync(output, 'utf8');
  assert(!distributed.includes(root), 'Distributed configuration retains the ephemeral root');
  assert(distributed.includes('LICENSE=LGPL version 2.1 or later'), 'License configuration changed');
  assert(distributed.includes('-lwebp -lz'), 'Dependency flags changed');
});
