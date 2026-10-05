import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { releaseCommit } from './committed-inputs.mjs';

const rootFiles = new Set([
  '.gitattributes', '.gitignore', '.gitleaksignore', 'AGENTS.md', 'CONTEXT.md', 'LICENSE', 'Makefile',
  'README.md', 'THIRD_PARTY_NOTICES', 'config.example.yaml', 'go.mod', 'go.sum', 'install-release.sh',
]);
const owners = /^(?:\.agents\/skills\/yatm\/|\.github\/|cmd\/|internal\/|entity\/|frontend\/|previewworker\/|scripts\/|build\/(?:backend|release)\/|dev\/|e2e\/|docs\/|licenses\/)/;
const scratch = /(?:\.(?:db|sqlite|sqlite3|log|tmp|temp|bak|orig|rej|swp|swo|tsbuildinfo|out|test|exe|dll|o|a|so|dylib|zip|tgz|tar|tar\.gz|tar\.xz)|~)$/i;
const privateComponent = /(?:^|\/)(?:\.local|\.pnpm-store|\.drafts|node_modules|vendor|\.DS_Store)(?:\/|$)/;
const executableMagic = new Set(['7f454c46', 'feedface', 'cefaedfe', 'feedfacf', 'cffaedfe', 'cafebabe', 'bebafeca', 'cafebabf', 'bfbafeca']);

// Repository source excludes runtime data and build output. Corresponding-source archives
// have a different owner and are checked by check-preview-sources, including required vendor trees.
export function checkSourceInventory(repository) {
  const files = new Set(execFileSync('git', ['-C', repository, 'ls-files', '--cached', '--others', '--exclude-standard', '-z'],
    { encoding: 'utf8' }).split('\0').filter(Boolean));
  let count = 0;
  for (const name of files) {
    const filename = path.join(repository, name);
    const entry = fs.lstatSync(filename, { throwIfNoEntry: false });
    if (!entry) continue;
    assert(entry.isFile(), `Public source must be an ordinary file: ${name}`);
    assert(rootFiles.has(name) || name === '.vscode/settings.json' || owners.test(name), `Source has no repository owner: ${name}`);
    assert(!privateComponent.test(name) && !/^(?:output\/|frontend\/dist(?:-ssr)?\/)/.test(name), `Private or generated output in source: ${name}`);
    assert(!scratch.test(name), `Runtime data, archive or temporary artifact in source: ${name}`);
    const base = path.posix.basename(name);
    assert(name === 'frontend/.env.example' || !/^\.env(?:\.|$)/.test(base), `Local environment file in source: ${name}`);
    assert(!/^config\.ya?ml$/i.test(base), `Local configuration in source: ${name}`);
    const descriptor = fs.openSync(filename, 'r');
    const header = Buffer.alloc(16);
    try { fs.readSync(descriptor, header, 0, header.length, 0); }
    finally { fs.closeSync(descriptor); }
    assert(header.toString('latin1') !== 'SQLite format 3\0', `Runtime database in source: ${name}`);
    assert(!executableMagic.has(header.subarray(0, 4).toString('hex')) && header.subarray(0, 2).toString() !== 'MZ', `Compiled executable in source: ${name}`);
    count++;
  }
  return count;
}

export function checkReleaseSource(repository, suppliedCommit) {
  const commit = releaseCommit(repository, suppliedCommit);
  const shallow = execFileSync('git', ['-C', repository, 'rev-parse', '--is-shallow-repository'], { encoding: 'utf8' }).trim();
  assert.equal(shallow, 'false', 'Release history is incomplete; fetch the full history before checking public content');
  const count = checkSourceInventory(repository);
  console.log(`Validated ${count} source files at ${commit}; full reachable history is available.`);
  return commit;
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    assert.match(process.env.RELEASE_VERSION || '', /^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$/, 'Set RELEASE_VERSION to the candidate tag');
    checkReleaseSource(path.resolve(process.argv[2] || '.'), process.env.RELEASE_COMMIT);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
