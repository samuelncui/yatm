import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { checkInputs, inspectCandidate, checkCandidateFiles, publishCandidate } from './publish-candidate.mjs';
import { expectedArchives } from './check-candidate-set.mjs';

const options = { repository: 'samuelncui/yatm', version: 'v1.0.0-alpha.2', runId: '123' };
const commit = 'a'.repeat(40);
const now = Date.parse('2026-09-26T00:00:00Z');

function metadata() {
  return {
    workflow: { id: 7, name: 'Release candidate', path: '.github/workflows/build.yml' },
    run: {
      id: 123, repository: { id: 8, full_name: options.repository }, head_repository: { id: 8, full_name: options.repository },
      workflow_id: 7, path: '.github/workflows/build.yml@v1', name: 'Release candidate', event: 'workflow_dispatch',
      status: 'completed', conclusion: 'success', display_title: `Build candidate ${options.version}`, head_sha: commit,
    },
    artifacts: [{ artifacts: [{
      id: 456, name: 'accepted-candidates', expired: false, expires_at: '2099-01-01T00:00:00Z',
      workflow_run: { id: 123, head_sha: commit, repository_id: 8, head_repository_id: 8 },
    }] }],
    release: { tag_name: options.version, prerelease: true },
    ref: { object: { type: 'commit', sha: commit } },
    tags: {},
  };
}

function mockAPI(data, calls = []) {
  const base = `repos/${options.repository}`;
  return (route, paginate = false) => {
    calls.push({ route, paginate });
    if (route === `${base}/actions/workflows/build.yml`) return data.workflow;
    if (route === `${base}/actions/runs/${options.runId}`) return data.run;
    if (route === `${base}/actions/runs/${options.runId}/artifacts?per_page=100`) {
      assert(paginate, 'Artifact lookup must include every page');
      return data.artifacts;
    }
    if (route === `${base}/releases/tags/${options.version}`) {
      assert(data.release, 'Release does not exist');
      return data.release;
    }
    if (route === `${base}/git/ref/tags/${options.version}`) {
      assert(data.ref, 'Tag does not exist');
      return data.ref;
    }
    const prefix = `${base}/git/tags/`;
    if (route.startsWith(prefix)) return data.tags[route.slice(prefix.length)];
    assert.fail(`Unexpected GitHub request: ${route}`);
  };
}

test('build inputs need no release or candidate run; publish requires an explicit numeric run', () => {
  checkInputs({ ...options, publish: 'false', runId: '' });
  checkInputs({ ...options, publish: 'true' });
  for (const runId of ['', '0', '-1', '1; touch /tmp/injected', '$(id)', '123\nartifact_id=1', '9007199254740992']) {
    assert.throws(() => checkInputs({ ...options, publish: 'true', runId }), /candidate_run_id/);
  }
  assert.throws(() => checkInputs({ ...options, publish: 'false' }), /only used with publish=true/);
  assert.throws(() => checkInputs({ ...options, publish: 'yes' }), /publish flag/);
});

test('dispatch strings cannot inject shell commands, GitHub routes, or output lines', () => {
  for (const version of ['$(id)', 'v1.0.0;echo hacked', 'v1.0.0\nartifact_id=1', '--clobber', 'v1.0.0/../other']) {
    assert.throws(() => checkInputs({ ...options, version, publish: 'true' }), /version/);
  }
  for (const repository of ['other/repo/../yatm', 'owner/repo\n', 'owner/repo;id']) {
    assert.throws(() => checkInputs({ ...options, repository, publish: 'true' }), /repository/);
  }
});

test('identity comes from the selected run, with paginated artifact metadata', () => {
  const data = metadata();
  data.artifacts.unshift({ artifacts: [{ id: 3, name: 'candidate-linux-amd64' }] });
  assert.deepEqual(inspectCandidate(options, mockAPI(data), now), { commit, artifactId: '456' });
});

test('annotated tags resolve to the exact selected run commit', () => {
  const data = metadata();
  const tag = 'b'.repeat(40);
  data.ref.object = { type: 'tag', sha: tag };
  data.tags[tag] = { object: { type: 'commit', sha: commit } };
  assert.deepEqual(inspectCandidate(options, mockAPI(data), now), { commit, artifactId: '456' });
});

const negativeMetadata = [
  ['wrong workflow identity', data => { data.run.workflow_id = 99; }, /workflow/],
  ['wrong workflow path', data => { data.run.path = '.github/workflows/other.yml@v1'; }, /workflow path/],
  ['wrong workflow name', data => { data.run.name = 'Other'; }, /workflow name/],
  ['wrong workflow owner path', data => { data.workflow.path = '.github/workflows/other.yml'; }, /workflow path/],
  ['wrong workflow owner name', data => { data.workflow.name = 'Other'; }, /workflow name/],
  ['wrong run ID', data => { data.run.id = 999; }, /candidate run/],
  ['different repository', data => { data.run.repository.full_name = 'other/yatm'; }, /same repository/],
  ['fork source', data => { data.run.head_repository.full_name = 'other/yatm'; }, /same repository/],
  ['automatic release build', data => { data.run.event = 'release'; }, /manual build/],
  ['incomplete run', data => { data.run.status = 'in_progress'; }, /completed/],
  ['failed run', data => { data.run.conclusion = 'failure'; }, /successful/],
  ['cancelled run', data => { data.run.conclusion = 'cancelled'; }, /successful/],
  ['publish run selected as build', data => { data.run.display_title = `Publish ${options.version} from candidate 122`; }, /build kind or version/],
  ['wrong candidate version', data => { data.run.display_title = 'Build candidate v1.0.0-alpha.1'; }, /build kind or version/],
  ['invalid commit', data => { data.run.head_sha = 'main'; }, /candidate commit/],
  ['missing accepted artifact', data => { data.artifacts[0].artifacts = []; }, /exactly one/],
  ['duplicate accepted artifact', data => { data.artifacts.push(data.artifacts[0]); }, /exactly one/],
  ['invalid artifact ID', data => { data.artifacts[0].artifacts[0].id = -1; }, /artifact ID/],
  ['expired artifact flag', data => { data.artifacts[0].artifacts[0].expired = true; }, /expired/],
  ['expired artifact timestamp', data => { data.artifacts[0].artifacts[0].expires_at = '2026-01-01T00:00:00Z'; }, /expired/],
  ['invalid artifact expiry', data => { data.artifacts[0].artifacts[0].expires_at = 'unknown'; }, /expired/],
  ['artifact from another run', data => { data.artifacts[0].artifacts[0].workflow_run.id = 999; }, /Artifact run/],
  ['artifact from another commit', data => { data.artifacts[0].artifacts[0].workflow_run.head_sha = 'b'.repeat(40); }, /Artifact commit/],
  ['artifact from another repository', data => { data.artifacts[0].artifacts[0].workflow_run.repository_id = 999; }, /Artifact repository/],
  ['artifact from another source repository', data => { data.artifacts[0].artifacts[0].workflow_run.head_repository_id = 999; }, /Artifact source repository/],
  ['absent Release', data => { data.release = null; }, /does not exist/],
  ['wrong Release tag', data => { data.release.tag_name = 'v1.0.0-alpha.1'; }, /Release tag/],
  ['alpha Release marked stable', data => { data.release.prerelease = false; }, /prerelease status/],
  ['absent tag', data => { data.ref = null; }, /does not exist/],
  ['tag points to another commit', data => { data.ref.object.sha = 'b'.repeat(40); }, /exact candidate commit/],
  ['tag points to a tree', data => { data.ref.object.type = 'tree'; }, /point to a commit/],
  ['tag cycle', data => {
    const tag = 'b'.repeat(40);
    data.ref.object = { type: 'tag', sha: tag };
    data.tags[tag] = { object: { type: 'tag', sha: tag } };
  }, /tag cycle/],
];
for (const [name, change, error] of negativeMetadata) {
  test(`reject ${name} before any upload`, () => {
    const data = metadata();
    change(data);
    let uploads = 0;
    assert.throws(() => publishCandidate(options, '/unused', '456', {
      api: mockAPI(data), upload: () => { uploads++; }, validate: () => assert.fail('Must reject metadata first'),
    }), error);
    assert.equal(uploads, 0);
  });
}

test('stable version requires a stable Release', () => {
  const stableOptions = { ...options, version: 'v1.0.0' };
  const data = metadata();
  data.run.display_title = 'Build candidate v1.0.0';
  data.release = { tag_name: 'v1.0.0', prerelease: false };
  const api = mockAPI(data);
  const stableAPI = (route, paginate) => api(route.replaceAll('v1.0.0', options.version), paginate);
  inspectCandidate(stableOptions, stableAPI, now);
  data.release.prerelease = true;
  assert.throws(() => inspectCandidate(stableOptions, stableAPI, now), /prerelease status/);
});

function candidateFiles(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'yatm-publish-candidate-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const lines = expectedArchives(options.version).map(name => {
    const contents = Buffer.from(`Accepted candidate bytes for ${name}`);
    fs.writeFileSync(path.join(directory, name), contents);
    const line = `${crypto.createHash('sha256').update(contents).digest('hex')}  ${name}`;
    fs.writeFileSync(path.join(directory, `${name}.sha256`), `${line}\n`);
    return line;
  }).sort();
  fs.writeFileSync(path.join(directory, 'SHA256SUMS'), `${lines.join('\n')}\n`);
  return directory;
}

test('uploads the identical complete downloaded file set after existing validator acceptance', t => {
  const directory = candidateFiles(t);
  const before = new Map(fs.readdirSync(directory).map(name => [name, fs.readFileSync(path.join(directory, name))]));
  let validated = false;
  let uploads = 0;
  publishCandidate(options, directory, '456', {
    api: mockAPI(metadata()),
    validate: (actualDirectory, version, actualCommit) => {
      assert.equal(actualDirectory, directory);
      assert.equal(version, options.version);
      assert.equal(actualCommit, commit);
      validated = true;
    },
    upload: (actualOptions, files) => {
      assert(validated);
      assert.equal(actualOptions, options);
      assert.deepEqual(files.map(file => path.basename(file)).sort(), [...before.keys()].sort());
      for (const file of files) assert.deepEqual(fs.readFileSync(file), before.get(path.basename(file)));
      uploads++;
    },
  });
  assert.equal(uploads, 1);
  for (const [name, bytes] of before) assert.deepEqual(fs.readFileSync(path.join(directory, name)), bytes);
});

test('reject downloaded artifact ID mismatch before file validation', () => {
  assert.throws(() => publishCandidate(options, '/unused', '999', {
    api: mockAPI(metadata()), validate: () => assert.fail('Must reject artifact ID first'),
    upload: () => assert.fail('Must not upload'),
  }), /Downloaded candidate artifact mismatch/);
});

for (const [name, change, error] of [
  ['missing archive', directory => fs.unlinkSync(path.join(directory, expectedArchives(options.version)[0])), /missing accepted candidate files/],
  ['missing checksum', directory => fs.unlinkSync(path.join(directory, `${expectedArchives(options.version)[0]}.sha256`)), /missing accepted candidate files/],
  ['missing aggregate checksum', directory => fs.unlinkSync(path.join(directory, 'SHA256SUMS')), /missing accepted candidate files/],
  ['extra file', directory => fs.writeFileSync(path.join(directory, 'unexpected.txt'), 'extra'), /Unexpected/],
  ['extra ARMv5 candidate', directory => fs.writeFileSync(path.join(directory, `yatm-linux-arm5-experimental-${options.version}.tar.gz`), 'extra'), /Unexpected/],
  ['directory replacing archive', directory => {
    const file = path.join(directory, expectedArchives(options.version)[0]);
    fs.unlinkSync(file);
    fs.mkdirSync(file);
  }, /ordinary file/],
  ['symlink replacing archive', directory => {
    const file = path.join(directory, expectedArchives(options.version)[0]);
    fs.unlinkSync(file);
    fs.symlinkSync('SHA256SUMS', file);
  }, /ordinary file/],
  ['modified archive', directory => fs.appendFileSync(path.join(directory, expectedArchives(options.version)[0]), 'tampered'), /Checksum mismatch/],
  ['modified companion checksum', directory => fs.appendFileSync(path.join(directory, `${expectedArchives(options.version)[0]}.sha256`), 'tampered'), /Checksum mismatch/],
  ['modified aggregate checksum', directory => fs.appendFileSync(path.join(directory, 'SHA256SUMS'), 'tampered'), /SHA256SUMS mismatch/],
]) {
  test(`reject ${name} before any upload`, t => {
    const directory = candidateFiles(t);
    change(directory);
    assert.throws(() => publishCandidate(options, directory, '456', {
      api: mockAPI(metadata()), validate: () => assert.fail('Must reject files first'), upload: () => assert.fail('Must not upload'),
    }), error);
  });
}

test('candidate validator rejection prevents upload', t => {
  const directory = candidateFiles(t);
  assert.throws(() => publishCandidate(options, directory, '456', {
    api: mockAPI(metadata()), validate: () => { throw new Error('Package identity rejected'); },
    upload: () => assert.fail('Must not upload'),
  }), /Package identity rejected/);
});

test('the real existing validator rejects checksum-consistent non-packages', t => {
  const directory = candidateFiles(t);
  assert.throws(() => checkCandidateFiles(directory, options.version, commit));
});
