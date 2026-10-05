// One immutable frontend and its production notices are reused by every release target.
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { directoryFiles } from './committed-inputs.mjs';
import { dependencyFiles, frontendFiles } from './package-assets.mjs';

const digest = bytes => crypto.createHash('sha256').update(bytes).digest('hex');

function artifactFiles(directory, version, commit) {
  const files = directoryFiles(directory);
  files.delete('manifest.json');
  const allowed = new Set([...frontendFiles(files, version, commit), ...dependencyFiles(files, ['node']), 'licenses/react-dnd.LICENSE']);
  assert(files.has('licenses/react-dnd.LICENSE'), 'Missing copied frontend attribution');
  for (const name of files.keys()) assert(allowed.has(name), `Unexpected frontend artifact file: ${name}`);
  return files;
}

export function createFrontendArtifact(directory, version, commit, lockfile) {
  assert.match(version, /^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/);
  assert.match(commit, /^[a-f0-9]{40}$/);
  const files = artifactFiles(directory, version, commit);
  const manifest = { version, commit, lock_sha256: digest(fs.readFileSync(lockfile)), files: {} };
  for (const [name, bytes] of [...files].sort(([a], [b]) => a.localeCompare(b))) manifest.files[name] = digest(bytes);
  fs.writeFileSync(path.join(directory, 'manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
}

export function validateFrontendArtifact(directory, version, commit, lockfile) {
  const manifest = JSON.parse(fs.readFileSync(path.join(directory, 'manifest.json'), 'utf8'));
  assert.equal(manifest.version, version, 'Frontend artifact version mismatch');
  assert.equal(manifest.commit, commit, 'Frontend artifact commit mismatch');
  assert.equal(manifest.lock_sha256, digest(fs.readFileSync(lockfile)), 'Frontend artifact lockfile mismatch');
  const files = artifactFiles(directory, version, commit);
  assert(manifest.files && typeof manifest.files === 'object' && !Array.isArray(manifest.files), 'Invalid frontend file manifest');
  assert.deepEqual([...files.keys()].sort(), Object.keys(manifest.files).sort(), 'Frontend artifact file list mismatch');
  for (const [name, bytes] of files) assert.equal(digest(bytes), manifest.files[name], `Frontend artifact checksum mismatch: ${name}`);
  return files;
}

export function installFrontendArtifact(directory, destination, version, commit, lockfile) {
  const files = validateFrontendArtifact(directory, version, commit, lockfile);
  for (const [name, bytes] of files) {
    const target = path.join(destination, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, bytes);
  }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [operation, directory, destination] = process.argv.slice(2);
  const { RELEASE_VERSION: version, RELEASE_COMMIT: commit } = process.env;
  const lockfile = path.resolve(import.meta.dirname, '../../frontend/pnpm-lock.yaml');
  if (operation === 'create') createFrontendArtifact(directory, version, commit, lockfile);
  else if (operation === 'validate') validateFrontendArtifact(directory, version, commit, lockfile);
  else if (operation === 'install' && destination) installFrontendArtifact(directory, destination, version, commit, lockfile);
  else throw new Error('Usage: frontend-artifact.mjs <create|validate|install> <artifact-directory> [package-directory]');
}
