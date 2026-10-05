// FFmpeg creates test inputs only; it is not an installed YATM runtime dependency.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export function checkPreviewFixtures(ffmpeg = 'ffmpeg') {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-preview-fixtures-'));
  const run = (name, args, output) => {
    const result = spawnSync(ffmpeg, ['-nostdin', '-hide_banner', '-loglevel', 'error', ...args, path.join(directory, output)],
      { encoding: 'utf8', timeout: 30_000 });
    assert(!result.error && result.status === 0,
      `Preview test fixture preflight failed (${name}). Install FFmpeg with libx264, 10-bit libx265, PNG, lavfi and display_rotation support. ${result.error?.message || result.stderr}`);
    assert(fs.statSync(path.join(directory, output)).size > 0, `Empty Preview test fixture: ${name}`);
  };
  try {
    const video = ['-f', 'lavfi', '-i', 'testsrc2=size=64x48:rate=3', '-frames:v', '2'];
    run('H.264', [...video, '-c:v', 'libx264', '-threads', '1', '-g', '3'], 'h264.mp4');
    run('10-bit HEVC', [...video, '-c:v', 'libx265', '-pix_fmt', 'yuv420p10le', '-preset', 'ultrafast',
      '-x265-params', 'pools=1:frame-threads=1:keyint=3:min-keyint=3:scenecut=0'], 'hevc.mkv');
    run('PNG', ['-f', 'lavfi', '-i', 'color=red:s=64x48', '-frames:v', '1', '-c:v', 'png', '-threads', '1'], 'image.png');
    run('rotation and timestamp offset', ['-itsoffset', '5', '-display_rotation', '90', '-i', path.join(directory, 'h264.mp4'),
      '-c', 'copy', '-aspect', '8:3'], 'rotated.mov');
    run('Matroska timestamp offset', ['-itsoffset', '5', '-i', path.join(directory, 'h264.mp4'), '-c', 'copy'], 'offset.mkv');
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  checkPreviewFixtures();
  console.log('Preview test fixture generator, encoders and container transforms passed.');
}
