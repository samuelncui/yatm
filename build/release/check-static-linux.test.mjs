import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

for (const bits of [1, 2]) {
  for (const endian of ['LE', 'BE']) {
    for (const type of [2, 3]) {
      test(`reject ${bits === 1 ? 32 : 64}-bit ${endian} ELF program type ${type}`, () => {
        const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-elf-test-'));
        try {
          const bytes = Buffer.alloc(128);
          bytes.set([0x7f, 0x45, 0x4c, 0x46, bits, endian === 'LE' ? 1 : 2]);
          if (bits === 1) bytes[`writeUInt32${endian}`](64, 28);
          else bytes[`writeBigUInt64${endian}`](64n, 32);
          bytes[`writeUInt16${endian}`](32, bits === 1 ? 42 : 54);
          bytes[`writeUInt16${endian}`](1, bits === 1 ? 44 : 56);
          bytes[`writeUInt32${endian}`](type, 64);
          const filename = path.join(temporary, 'binary');
          fs.writeFileSync(filename, bytes);
          const result = spawnSync(process.execPath, [new URL('./check-static-linux.mjs', import.meta.url).pathname, filename], { encoding: 'utf8' });
          assert.notEqual(result.status, 0);
          assert.match(result.stderr, /dynamic segment or ELF interpreter/);
        } finally {
          fs.rmSync(temporary, { recursive: true });
        }
      });
    }
  }
}
