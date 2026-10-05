import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { sourceNotices } from './native-notices.mjs';
import { nativeSources } from './check-preview-sources.mjs';

function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-native-notices-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

test('embedded notices retain exact copyright, terms, Unicode and line endings', t => {
  const root = temporary(t);
  const block = '/* Copyright © Fixture\r\n * Redistribution and use permitted.\r\n */';
  const second = '/* Permission is hereby granted.\nNo warranty. */';
  const line = '// Copyright second author\n// BSD license\n';
  const assembly = '; Copyright assembly author\n; Permission granted\n';
  fs.mkdirSync(path.join(root, 'src'));
  fs.writeFileSync(path.join(root, 'src/one.c'), `${block}\nint code;\n${second}\n`);
  fs.writeFileSync(path.join(root, 'src/two.cpp'), `${line}int code;\n`);
  fs.writeFileSync(path.join(root, 'src/three.asm'), `${assembly}ret\n`);
  fs.writeFileSync(path.join(root, 'src/private.log'), 'not source');
  const notices = sourceNotices(root, ['src']);
  for (const text of [block, second, line, assembly]) assert(notices.includes(text));
  assert(!notices.includes('int code') && !notices.includes('not source'));
});

const nativeDirectory = process.env.YATM_TEST_PREVIEW_SOURCE_DIRECTORY;
test('pinned native source notices include LibRaw BSD dependencies and FFmpeg IJG terms', { skip: !nativeDirectory }, t => {
  const root = temporary(t);
  for (const name of nativeSources.keys()) {
    const archive = path.join(nativeDirectory, name);
    assert.equal(crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex'), nativeSources.get(name));
    execFileSync('tar', ['-xf', archive, '-C', root]);
  }
  const output = path.join(root, 'licenses');
  execFileSync(process.execPath, [path.resolve(import.meta.dirname, '../../previewworker/build/native-notices.mjs'), root, output]);
  const libraw = fs.readFileSync(path.join(output, 'LibRaw-NOTICE'), 'utf8');
  for (const text of ['Jacek Gozdz', 'Roland Karlsson', 'Redistribution and use', 'THIS SOFTWARE IS PROVIDED']) assert(libraw.includes(text));
  const ffmpeg = fs.readFileSync(path.join(output, 'FFmpeg-NOTICE'), 'utf8');
  for (const text of ['Independent JPEG Group', 'Thomas G. Lane', 'Source: libavcodec/jrevdct.c']) assert(ffmpeg.includes(text));
  assert(fs.readFileSync(path.join(output, 'libwebp-NOTICE'), 'utf8').includes('Source: sharpyuv/sharpyuv.c'));
  assert(fs.readFileSync(path.join(output, 'zlib-NOTICE'), 'utf8').includes('Mark Adler'));
  assert(fs.readFileSync(path.join(output, 'libjpeg-NOTICE'), 'utf8').includes('Thomas G. Lane'));
  // Compare an exact, complete transitive license block with the checksum-verified source.
  const original = fs.readFileSync(path.join(root, 'LibRaw-0.22.2/src/demosaic/dcb_demosaic.cpp'), 'utf8');
  assert(libraw.includes(original.slice(0, original.indexOf('*/') + 2)));
});
