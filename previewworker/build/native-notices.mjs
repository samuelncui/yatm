import fs from 'node:fs';
import path from 'node:path';
import { sourceNotices } from '../../build/release/native-notices.mjs';

const [source, output] = process.argv.slice(2);
fs.mkdirSync(output, { recursive: true });
for (const [directory, subdirectories, filename] of [
  ['LibRaw-0.22.2', ['src', 'internal', 'libraw'], 'LibRaw-NOTICE'],
  ['ffmpeg-8.0', ['libavcodec', 'libavdevice', 'libavfilter', 'libavformat', 'libavutil', 'libswresample', 'libswscale'], 'FFmpeg-NOTICE'],
  ['libwebp-1.6.0', ['src', 'sharpyuv'], 'libwebp-NOTICE'],
  ['zlib-1.3.1', ['.'], 'zlib-NOTICE'],
  ['jpeg-9f', ['.'], 'libjpeg-NOTICE'],
]) {
  fs.writeFileSync(path.join(output, filename), sourceNotices(path.join(source, directory), subdirectories));
}
// Required by the IJG license reproduced in the exact libjpeg and FFmpeg inputs.
fs.writeFileSync(path.join(output, 'IJG-NOTICE'),
  "This software is based in part on the work of the Independent JPEG Group.\n" +
  "YATM uses the IJG-derived FFmpeg 8.0 files without further changes; their upstream notices and modification descriptions are in FFmpeg-NOTICE.\n");
