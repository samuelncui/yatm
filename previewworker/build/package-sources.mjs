import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { sourceNotices, zigDirectories } from '../../build/release/native-notices.mjs';

const output = path.resolve(process.argv[2]);
const workerDirectory = path.resolve(import.meta.dirname, '..');
// Zig embeds its C++ runtime into the Linux LibRaw shared library rather than depending on host libstdc++.
if (process.env.GOOS === 'linux' && process.env.CXX?.startsWith('zig ')) {
  const { library: libraryDirectory, license } = zigDirectories();
  const licenseDirectory = path.join(output, 'licenses/zig-runtime');
  fs.mkdirSync(licenseDirectory, { recursive: true });
  for (const runtime of ['libcxx', 'libcxxabi', 'libunwind']) {
    fs.copyFileSync(path.join(libraryDirectory, runtime, 'LICENSE.TXT'), path.join(licenseDirectory, `${runtime}-LICENSE.TXT`));
  }
  fs.copyFileSync(license, path.join(licenseDirectory, 'Zig-LICENSE'));
  fs.writeFileSync(path.join(licenseDirectory, 'compiler-rt-NOTICE'), sourceNotices(libraryDirectory, ['compiler_rt']));
}
const modules = new Map();
const lines = execFileSync('go', ['list', '-deps', '-f', '{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}', '.'], { encoding: 'utf8', cwd: workerDirectory });
for (const line of lines.split('\n')) {
  const [name, version, moduleDirectory] = line.split('\t');
  if (name && version && !name.startsWith('github.com/samuelncui/yatm')) {
    const directory = moduleDirectory || path.join(workerDirectory, 'vendor', name);
    modules.set(name, { name, version, directory });
  }
}
const records = [];
if (!fs.existsSync(path.join(workerDirectory, 'vendor'))) {
  // Corresponding sources must match the downloaded modules, including files outside the compiled packages.
  execFileSync('go', ['mod', 'verify'], { cwd: workerDirectory, stdio: ['ignore', 'pipe', 'pipe'] });
}
for (const item of modules.values()) {
  const relative = `sources/modules/${item.name}@${item.version}`;
  fs.cpSync(item.directory, path.join(output, relative), { recursive: true });
  const notices = fs.readdirSync(item.directory).filter(name => /^(licen[cs]e|notice|copying|copyright)([.-].*)?$/i.test(name));
  if (!notices.length) throw new Error(`Missing license for ${item.name}`);
  const licenseDirectory = path.join(output, 'licenses/go', `${item.name}@${item.version}`);
  fs.mkdirSync(licenseDirectory, { recursive: true });
  for (const notice of notices) fs.copyFileSync(path.join(item.directory, notice), path.join(licenseDirectory, notice));
  records.push({ name: item.name, version: item.version, source: `modules/${item.name}@${item.version}`, notices });
}
fs.writeFileSync(path.join(output, 'licenses/go-dependencies.json'), JSON.stringify(records, null, 2) + '\n');
fs.copyFileSync(import.meta.filename, path.join(output, 'sources/yatm/previewworker/build/package-sources.mjs'));
const commit = process.argv[3] || fs.readFileSync(path.join(output, 'COMMIT'), 'utf8').trim();
const guide = fs.readFileSync(path.resolve(import.meta.dirname, 'README.md'), 'utf8');
const readme = directory => guide.replace(/\]\((?!https?:|#)([^)]+)\)/g, (match, target) => {
  if (directory && fs.existsSync(path.resolve(directory, decodeURIComponent(target.split('#')[0])))) return match;
  return `](https://github.com/samuelncui/yatm/blob/${commit}/${path.posix.normalize(`previewworker/build/${target}`)})`;
});
fs.writeFileSync(path.join(output, 'README.md'), readme());
const sourceGuide = path.join(output, 'sources/yatm/previewworker/build/README.md');
fs.writeFileSync(sourceGuide, readme(path.dirname(sourceGuide)));
