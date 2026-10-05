// Preserve upstream notice text, including notices embedded in native source files.
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

export const zigVersion = '0.15.2';

export function sourceNotices(directory, subdirectories) {
  const notices = [];
  const visit = relative => {
    for (const entry of fs.readdirSync(path.join(directory, relative), { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const name = path.posix.join(relative, entry.name);
      if (entry.isDirectory()) { visit(name); continue; }
      if (!entry.isFile() || !/\.(?:c|cc|cpp|h|hpp|S|s|asm|zig)$/.test(name)) continue;
      const source = fs.readFileSync(path.join(directory, name), 'utf8');
      const comments = [...source.matchAll(/\/\*[\s\S]*?\*\/|(?:^[ \t]*(?:\/\/|;)[^\n]*(?:\n|$))+/gm)]
        .map(match => match[0]).filter(comment => /copyright|license|permission|redistribution|warranty/i.test(comment));
      if (comments.length) notices.push(`Source: ${name}\n\n${comments.join('\n\n')}`);
    }
  };
  for (const subdirectory of subdirectories) visit(subdirectory);
  assert(notices.length, 'No embedded native source notices found');
  return `Source tree: ${path.basename(directory)}\nVerbatim notices from the pinned upstream source tree, including optional implementations.\n\n${notices.join('\n\n')}\n`;
}

export function zigDirectories(zig = process.env.ZIG || 'zig') {
  assert.equal(execFileSync(zig, ['version'], { encoding: 'utf8' }).trim(), zigVersion, `Native notices require Zig ${zigVersion}`);
  const environment = execFileSync(zig, ['env'], { encoding: 'utf8' });
  const library = environment.match(/\.lib_dir = "([^"]+)"/)?.[1];
  const executable = environment.match(/\.zig_exe = "([^"]+)"/)?.[1];
  assert(library && executable, 'Cannot resolve pinned Zig source directories');
  const licenses = [path.join(path.dirname(executable), 'LICENSE'), path.join(path.dirname(executable), '../LICENSE')];
  const license = licenses.find(name => fs.existsSync(name));
  assert(license, 'Missing pinned Zig license');
  assert.equal(crypto.createHash('sha256').update(fs.readFileSync(license)).digest('hex'),
    '5c537d6853e005298a285d508cff9ac7192cea23576c840d485b2b586a7ff177', 'Zig license does not match the pinned release');
  return { library, license };
}

export function copyMuslNotices(output) {
  const { library, license } = zigDirectories();
  const musl = path.join(library, 'libc/musl');
  const copyright = fs.readFileSync(path.join(musl, 'COPYRIGHT'));
  assert.equal(crypto.createHash('sha256').update(copyright).digest('hex'),
    'f9bc4423732350eb0b3f7ed7e91d530298476f8fec0c6c427a1c04ade22655af', 'musl notice does not match Zig 0.15.2');
  const records = [
    { kind: 'native', name: 'musl', version: `zig-${zigVersion}`, notices: ['COPYRIGHT', 'NOTICE'] },
    { kind: 'native', name: 'zig', version: zigVersion, notices: ['LICENSE', 'NOTICE'] },
  ];
  for (const item of records) fs.mkdirSync(path.join(output, item.kind, `${item.name}@${item.version}`), { recursive: true });
  const muslOutput = path.join(output, `native/musl@zig-${zigVersion}`);
  fs.writeFileSync(path.join(muslOutput, 'COPYRIGHT'), copyright);
  fs.writeFileSync(path.join(muslOutput, 'NOTICE'), sourceNotices(musl, ['src']));
  const zigOutput = path.join(output, `native/zig@${zigVersion}`);
  fs.copyFileSync(license, path.join(zigOutput, 'LICENSE'));
  fs.writeFileSync(path.join(zigOutput, 'NOTICE'), sourceNotices(library, ['compiler_rt']));
  return records;
}
