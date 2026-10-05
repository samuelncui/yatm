// Shared ownership checks for distributed frontend files and dependency notices.
import assert from 'node:assert/strict';
import { checkMemberPath } from './archive-members.mjs';

export function frontendFiles(files, version, commit) {
  const html = files.get('frontend/index.html')?.toString();
  assert(html?.includes(`name="yatm-version" content="${version}"`), 'Frontend version does not match package');
  assert(html.includes(`name="yatm-commit" content="${commit}"`), 'Frontend commit does not match package');
  const allowed = new Set(['frontend/index.html']);
  for (const name of files.keys()) {
    if (/^frontend\/assets\/[A-Za-z0-9_-]+-[A-Za-z0-9_-]{8}\.(?:js|css|svg)$/.test(name)) allowed.add(name);
  }
  return allowed;
}

export function dependencyFiles(files, kinds = ['go', 'node', 'native']) {
  const dependencies = JSON.parse(files.get('licenses/dependencies.json')?.toString() || 'null');
  assert(Array.isArray(dependencies) && dependencies.length, 'Invalid dependency notices manifest');
  const allowed = new Set(['licenses/dependencies.json']);
  for (const { kind, name, version, notices } of dependencies) {
    assert(kinds.includes(kind) && typeof name === 'string' && typeof version === 'string'
      && Array.isArray(notices) && notices.length, 'Invalid dependency notice ownership');
    if (kind === 'native') assert((name === 'musl' && version === 'zig-0.15.2') || (name === 'zig' && version === '0.15.2'), 'Unowned native runtime notice');
    const directory = `licenses/${kind}/${name}@${version}`;
    checkMemberPath(directory);
    for (const notice of notices) {
      assert(typeof notice === 'string' && /^(licen[cs]e|notice|copying|copyright|patents)([.-].*)?$/i.test(notice)
        && !notice.includes('/'), 'Invalid dependency notice filename');
      const entry = `${directory}/${notice}`;
      assert(files.has(entry), `Required dependency notice missing: ${entry}`);
      allowed.add(entry);
    }
    if (kind === 'node') {
      const entry = `${directory}/package.json`;
      assert(files.has(entry), `Required dependency metadata missing: ${directory}`);
      allowed.add(entry);
    }
  }
  return allowed;
}
