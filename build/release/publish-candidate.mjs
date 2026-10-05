import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { checkCandidateSet, expectedArchives } from './check-candidate-set.mjs';

export function checkInputs({ repository, version, publish, runId }) {
  assert.match(repository, /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/, 'Invalid repository');
  assert.match(version, /^v\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$/, 'Invalid release version');
  assert(['true', 'false'].includes(publish), 'Invalid publish flag');
  if (publish === 'true') {
    assert.match(runId, /^[1-9]\d*$/, 'Publishing requires candidate_run_id');
    assert(Number.isSafeInteger(Number(runId)), 'Invalid candidate_run_id');
  } else {
    assert(!runId, 'candidate_run_id is only used with publish=true');
  }
}

function githubAPI(route, paginate = false) {
  const args = ['api', route];
  if (paginate) args.push('--paginate', '--slurp');
  return JSON.parse(execFileSync('gh', args, { encoding: 'utf8' }));
}

export function inspectCandidate(options, api = githubAPI, now = Date.now()) {
  checkInputs({ ...options, publish: 'true' });
  const { repository, version, runId } = options;
  const base = `repos/${repository}`;
  const workflow = api(`${base}/actions/workflows/build.yml`);
  assert.equal(workflow.path, '.github/workflows/build.yml', 'Unexpected workflow path');
  assert.equal(workflow.name, 'Release candidate', 'Unexpected workflow name');
  const run = api(`${base}/actions/runs/${runId}`);
  assert.equal(run.id, Number(runId), 'Unexpected candidate run');
  for (const source of [run.repository, run.head_repository]) {
    assert.equal(source?.full_name, repository, 'Candidate must come from the same repository');
  }
  assert.equal(run.workflow_id, workflow.id, 'Candidate must use the Release candidate workflow');
  assert.equal(run.path.split('@')[0], workflow.path, 'Unexpected candidate workflow path');
  assert.equal(run.name, workflow.name, 'Unexpected candidate workflow name');
  assert.equal(run.event, 'workflow_dispatch', 'Candidate must be a manual build');
  assert.equal(run.status, 'completed', 'Candidate run must be completed');
  assert.equal(run.conclusion, 'success', 'Candidate run must be successful');
  // run-name is set by the workflow's build branch, not by candidate file contents.
  assert.equal(run.display_title, `Build candidate ${version}`, 'Candidate build kind or version mismatch');
  assert.match(run.head_sha, /^[a-f0-9]{40}$/, 'Invalid candidate commit');

  const artifacts = api(`${base}/actions/runs/${runId}/artifacts?per_page=100`, true)
    .flatMap(page => page.artifacts).filter(artifact => artifact.name === 'accepted-candidates');
  assert.equal(artifacts.length, 1, 'Expected exactly one accepted-candidates artifact');
  const artifact = artifacts[0];
  assert(Number.isSafeInteger(artifact.id) && artifact.id > 0, 'Invalid candidate artifact ID');
  assert.equal(artifact.expired, false, 'Candidate artifact has expired');
  assert(Date.parse(artifact.expires_at) > now, 'Candidate artifact has expired');
  assert.equal(artifact.workflow_run?.id, run.id, 'Artifact run mismatch');
  assert.equal(artifact.workflow_run?.head_sha, run.head_sha, 'Artifact commit mismatch');
  assert.equal(artifact.workflow_run?.repository_id, run.repository.id, 'Artifact repository mismatch');
  assert.equal(artifact.workflow_run?.head_repository_id, run.head_repository.id, 'Artifact source repository mismatch');

  const release = api(`${base}/releases/tags/${version}`);
  assert.equal(release.tag_name, version, 'Existing Release tag mismatch');
  assert.equal(release.prerelease, version.includes('-'), 'Existing Release prerelease status mismatch');
  let object = api(`${base}/git/ref/tags/${version}`).object;
  const visited = new Set();
  while (object.type === 'tag') {
    assert.match(object.sha, /^[a-f0-9]{40}$/, 'Invalid tag object');
    assert(!visited.has(object.sha), 'Invalid tag cycle');
    visited.add(object.sha);
    object = api(`${base}/git/tags/${object.sha}`).object;
  }
  assert.equal(object.type, 'commit', 'Release tag must point to a commit');
  assert.equal(object.sha, run.head_sha, 'Release tag must match the exact candidate commit');
  return { commit: run.head_sha, artifactId: String(artifact.id) };
}

export function checkCandidateFiles(directory, version, commit, validate = checkCandidateSet) {
  const archives = expectedArchives(version);
  const names = [...archives, ...archives.map(name => `${name}.sha256`), 'SHA256SUMS'].sort();
  assert.deepEqual(fs.readdirSync(directory).sort(), names, 'Unexpected or missing accepted candidate files');
  const files = names.map(name => path.resolve(directory, name));
  for (const file of files) assert(fs.lstatSync(file).isFile(), `Candidate must be an ordinary file: ${file}`);
  const checksums = archives.map(name => {
    const digest = crypto.createHash('sha256').update(fs.readFileSync(path.join(directory, name))).digest('hex');
    const line = `${digest}  ${name}`;
    assert.equal(fs.readFileSync(path.join(directory, `${name}.sha256`), 'utf8').trim(), line, `Checksum mismatch: ${name}`);
    return line;
  }).sort();
  assert.equal(fs.readFileSync(path.join(directory, 'SHA256SUMS'), 'utf8'), `${checksums.join('\n')}\n`, 'SHA256SUMS mismatch');
  validate(directory, version, commit);
  return files;
}

function uploadRelease({ repository, version }, files) {
  // gh upload requires an existing Release; omit --clobber to preserve existing assets.
  execFileSync('gh', ['release', 'upload', version, '--repo', repository, ...files], { stdio: 'inherit' });
}

export function publishCandidate(options, directory, artifactId, { api = githubAPI, validate = checkCandidateSet, upload = uploadRelease } = {}) {
  const identity = inspectCandidate(options, api);
  assert.equal(identity.artifactId, artifactId, 'Downloaded candidate artifact mismatch');
  const files = checkCandidateFiles(directory, options.version, identity.commit, validate);
  upload(options, files);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [mode, directory] = process.argv.slice(2);
  const options = { repository: process.env.GITHUB_REPOSITORY, version: process.env.RELEASE_VERSION, runId: process.env.CANDIDATE_RUN_ID };
  if (mode === 'inputs') {
    checkInputs({ ...options, publish: process.env.PUBLISH });
  } else if (mode === 'inspect') {
    const identity = inspectCandidate(options);
    assert(process.env.GITHUB_OUTPUT, 'GITHUB_OUTPUT is required');
    fs.appendFileSync(process.env.GITHUB_OUTPUT, `artifact_id=${identity.artifactId}\n`);
    console.log(`Accepted candidate run ${options.runId}: ${options.version}, ${identity.commit}, artifact ${identity.artifactId}`);
  } else {
    assert.equal(mode, 'upload', 'Usage: publish-candidate.mjs inputs|inspect|upload [directory]');
    assert(directory, 'Candidate directory is required');
    publishCandidate(options, directory, process.env.CANDIDATE_ARTIFACT_ID);
  }
}
