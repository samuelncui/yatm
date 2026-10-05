// Release builds consume a clean commit; distributed Preview rebuilds consume verified corresponding sources.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { validatePreviewSources, yatmSourceFiles, sourceChecksums } from './check-preview-sources.mjs';

export function directoryFiles(directory) {
  const files = new Map();
  function visit(relative) {
    for (const entry of fs.readdirSync(path.join(directory, relative), { withFileTypes: true })) {
      const name = relative ? `${relative}/${entry.name}` : entry.name;
      assert(entry.isFile() || entry.isDirectory(), `Source inputs must use ordinary files and directories: ${name}`);
      if (entry.isDirectory()) visit(name);
      else files.set(name, fs.readFileSync(path.join(directory, name)));
    }
  }
  visit('');
  return files;
}

export function releaseCommit(repository, suppliedCommit) {
  const git = (...args) => execFileSync('git', ['-C', repository, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  const root = git('rev-parse', '--show-toplevel');
  assert.equal(fs.realpathSync(root), fs.realpathSync(repository), 'Release inputs must be the repository root');
  const commit = git('rev-parse', 'HEAD');
  assert.match(commit, /^[a-f0-9]{40}$/, 'Invalid release commit');
  assert(!suppliedCommit || suppliedCommit === commit, 'RELEASE_COMMIT must match actual HEAD');
  assert.equal(git('status', '--porcelain=v1', '--untracked-files=all'), '', 'Release inputs must be clean: tracked changes or untracked nonignored source found');
  return commit;
}

export function committedInputs(repository, destination, suppliedCommit, previewSources) {
  // A distributed tree may sit below another checkout; only its own Git metadata identifies a release checkout.
  if (!fs.existsSync(path.join(repository, '.git'))) {
    assert(previewSources && suppliedCommit, 'Distributed Preview rebuild requires PREVIEW_SOURCE_DIRECTORY and explicit RELEASE_COMMIT');
    assert.equal(fs.realpathSync(path.join(previewSources, 'yatm')), fs.realpathSync(repository), 'Preview source directory must own the rebuild tree');
    const files = directoryFiles(previewSources);
    validatePreviewSources(files, suppliedCommit);
    fs.mkdirSync(destination, { recursive: true });
    for (const [name] of files) {
      if (!name.startsWith('yatm/')) continue;
      const relative = name.slice(5);
      fs.mkdirSync(path.dirname(path.join(destination, relative)), { recursive: true });
      fs.copyFileSync(path.join(previewSources, name), path.join(destination, relative));
      fs.chmodSync(path.join(destination, relative), fs.statSync(path.join(previewSources, name)).mode);
    }
    return suppliedCommit;
  }
  const commit = releaseCommit(repository, suppliedCommit);
  fs.mkdirSync(destination, { recursive: true });
  // Git archive excludes ignored/untracked inputs and repository metadata from every build input.
  const archive = execFileSync('git', ['-C', repository, 'archive', commit], { maxBuffer: 128 * 1024 * 1024 });
  execFileSync('tar', ['-xf', '-', '-C', destination], { input: archive });
  return commit;
}

export function copyPreviewSources(repository, destination) {
  for (const name of yatmSourceFiles) {
    const source = path.join(repository, name);
    assert(fs.lstatSync(source).isFile(), `Missing ordinary committed Preview source: ${name}`);
    const target = path.join(destination, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.copyFileSync(source, target);
    fs.chmodSync(target, fs.statSync(source).mode);
  }
  if (fs.existsSync(path.join(repository, 'previewworker/vendor'))) {
    fs.cpSync(path.join(repository, 'previewworker/vendor'), path.join(destination, 'previewworker/vendor'), { recursive: true });
  }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [operation, repository, destination, commit, sources] = process.argv.slice(2);
  if (operation === 'checkout') console.log(committedInputs(repository, destination, commit || undefined, sources || undefined));
  else if (operation === 'preview') copyPreviewSources(repository, destination);
  else if (operation === 'checksums') fs.writeFileSync(path.join(repository, 'SHA256SUMS'), sourceChecksums(directoryFiles(repository)));
  else if (operation === 'verify') validatePreviewSources(directoryFiles(repository), destination);
  else throw new Error('Usage: committed-inputs.mjs <checkout|preview|checksums|verify> <source> <destination|commit> [commit] [distributed-sources]');
}
