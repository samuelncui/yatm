import assert from 'node:assert/strict'
import { chmod, cp, mkdtemp, mkdir, readFile, realpath, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { basename, join } from 'node:path'
import { after, test } from 'node:test'
import { spawnSync } from 'node:child_process'

const temporaryDirectory = await mkdtemp(join(tmpdir(), 'yatm-build-test-'))
const fixtureRoot = join(temporaryDirectory, 'fixture')
const callerDirectory = join(temporaryDirectory, 'caller directory')
const goLog = join(temporaryDirectory, 'go.log')

await mkdir(join(fixtureRoot, 'build', 'backend'), { recursive: true })
await mkdir(callerDirectory)
await cp(new URL('./build.sh', import.meta.url), join(fixtureRoot, 'build', 'backend', 'build.sh'))
const physicalFixtureRoot = await realpath(fixtureRoot)
const physicalCallerDirectory = await realpath(callerDirectory)

const goDirectory = join(temporaryDirectory, 'bin')
const goStub = join(goDirectory, 'go')
await mkdir(goDirectory)
await writeFile(goStub, `#!/usr/bin/env node
import { appendFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'

const args = process.argv.slice(2)
if (args[0] === 'env' && args[1] === 'GOOS') {
  process.stdout.write('darwin\\n')
  process.exit(0)
}

appendFileSync(process.env.GO_LOG, JSON.stringify({ args, cwd: process.cwd() }) + '\\n')
const outputIndex = args.indexOf('-o')
if (outputIndex >= 0) {
  mkdirSync(dirname(args[outputIndex + 1]), { recursive: true })
  writeFileSync(args[outputIndex + 1], '')
}
`)
await chmod(goStub, 0o755)

after(async () => {
  await rm(temporaryDirectory, { force: true, recursive: true })
})

async function runBuild(outputDirectory) {
  await writeFile(goLog, '')
  const environment = {
    ...process.env,
    GO_LOG: goLog,
    GOOS: 'darwin',
    PATH: `${goDirectory}:${process.env.PATH}`,
    RELEASE_COMMIT: 'test-commit',
    RELEASE_VERSION: 'test-version',
  }
  delete environment.OUTPUT_DIRECTORY
  if (outputDirectory !== undefined) {
    environment.OUTPUT_DIRECTORY = outputDirectory
  }

  const result = spawnSync(join(fixtureRoot, 'build', 'backend', 'build.sh'), [], {
    cwd: physicalCallerDirectory,
    encoding: 'utf8',
    env: environment,
  })
  assert.equal(result.status, 0, result.stderr)
  return (await readFile(goLog, 'utf8')).trim().split('\n').map(JSON.parse)
}

function assertBuilds(commands, outputDirectory) {
  const programs = ['httpd', 'yatm-cli', 'export-library', 'lto-info', 'migrate']
  assert.equal(commands.length, programs.length)

  for (const [index, command] of commands.entries()) {
    const program = programs[index]
    const output = join(outputDirectory, program === 'yatm-cli' ? 'yatm-cli' : `yatm-${program}`)
    assert.equal(command.cwd, physicalFixtureRoot)
    assert.deepEqual(command.args.slice(0, 2), ['build', '-trimpath'])
    assert.equal(command.args.at(-1), `./cmd/${program}`)
    assert.equal(command.args[command.args.indexOf('-o') + 1], output)
    assert.match(command.args[command.args.indexOf('-ldflags') + 1], /test-version/)
    assert.match(command.args[command.args.indexOf('-ldflags') + 1], /test-commit/)
  }
}

test('builds default artifacts from the repository root', async () => {
  const outputDirectory = join(fixtureRoot, 'output')
  const commands = await runBuild()

  assertBuilds(commands, outputDirectory)
  for (const command of commands) {
    await readFile(command.args[command.args.indexOf('-o') + 1])
  }
})

test('resolves relative output directories from the caller and preserves spaces', async () => {
  const outputDirectory = join(physicalCallerDirectory, 'release artifacts')
  const commands = await runBuild('release artifacts')

  assertBuilds(commands, outputDirectory)
  assert.equal(basename(commands[0].args[commands[0].args.indexOf('-o') + 1]), 'yatm-httpd')
})
