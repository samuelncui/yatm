// Only the ephemeral build root is replaced; dependency flags and license configuration remain public.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

export const publicBuildRoot = '/build/yatm-preview';

export function publicBuildMetadata(text, buildRoot) {
  assert(buildRoot?.startsWith('/') && buildRoot.length > 1, 'Expected absolute native build root');
  const escaped = buildRoot.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return text.replace(new RegExp(`${escaped}(?=/|[^A-Za-z0-9._-]|$)`, 'g'), publicBuildRoot);
}

export function publicFFmpegConfiguration(header, buildRoot) {
  assert(/^#define FFMPEG_CONFIGURATION /m.test(header), 'Missing FFmpeg configuration metadata');
  return header.replace(/^#define FFMPEG_CONFIGURATION .*$/m, line => publicBuildMetadata(line, buildRoot));
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [operation, input, output, buildRoot] = process.argv.slice(2);
  if (operation === 'header') fs.writeFileSync(input, publicFFmpegConfiguration(fs.readFileSync(input, 'utf8'), output));
  else if (operation === 'copy') fs.writeFileSync(output, publicBuildMetadata(fs.readFileSync(input, 'utf8'), buildRoot));
  else throw new Error('Usage: sanitize-native.mjs header <config.h> <build-root> | copy <config.mak> <output> <build-root>');
}
