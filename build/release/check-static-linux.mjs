// Reject a dynamically linked release before it enters a package, on any host.
import fs from 'node:fs';
import { execFileSync } from 'node:child_process';

for (const filename of process.argv.slice(2)) {
  const data = fs.readFileSync(filename);
  if (!data.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) throw new Error(`${filename}: not ELF`);
  const bits = data[4];
  const little = data[5] === 1;
  if (![1, 2].includes(bits) || ![1, 2].includes(data[5])) throw new Error(`${filename}: unsupported ELF encoding`);
  const u16 = offset => little ? data.readUInt16LE(offset) : data.readUInt16BE(offset);
  const u32 = offset => little ? data.readUInt32LE(offset) : data.readUInt32BE(offset);
  const u64 = offset => Number(little ? data.readBigUInt64LE(offset) : data.readBigUInt64BE(offset));
  const offset = bits === 1 ? u32(28) : u64(32);
  const size = u16(bits === 1 ? 42 : 54);
  const count = u16(bits === 1 ? 44 : 56);
  if (!count || !size || offset + size * count > data.length) throw new Error(`${filename}: invalid program headers`);
  for (let index = 0; index < count; index++) {
    const type = u32(offset + size * index);
    if (type === 2 || type === 3) throw new Error(`${filename}: contains a dynamic segment or ELF interpreter`);
  }
  const metadata = execFileSync('go', ['version', '-m', filename], { encoding: 'utf8' });
  if (!metadata.includes('CGO_ENABLED=1')) throw new Error(`${filename}: CGO is not enabled`);
  if (/go-astiav|go-astits|ffmpeg/i.test(metadata)) throw new Error(`${filename}: unexpected media dependency in main program`);
  console.log(`${filename}: static ELF, CGO enabled, no dynamic dependencies`);
}
