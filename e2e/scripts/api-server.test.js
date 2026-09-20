// @ts-check
'use strict';
/*
 * What the launcher does when a backend did not come up.
 *
 * Two answers, and which one is right depends on who is asking. On a laptop
 * without Docker the run continues with no database and the API specs skip
 * themselves; in CI that same run is a green job that quietly stopped testing
 * the half of the application a database exists for, so there the launcher
 * fails instead. See scripts/api-server.js.
 *
 * Node's own runner, no dependency and no Docker: both cases put a `docker` on
 * PATH that fails, which leaves the launcher where a machine without one does.
 *
 *     cd e2e && node --test scripts/api-server.test.js
 */
const { test, before, after, beforeEach } = require('node:test');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const launcher = path.join(__dirname, 'api-server.js');

let work = '';
let marker = '';

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

before(() => {
  work = fs.mkdtempSync(path.join(os.tmpdir(), 'soiree-launcher-'));
  marker = path.join(work, 'started');

  // `docker info` fails, so the launcher sees a machine with no Docker, which
  // is also where a failed pull or a readiness timeout leaves it.
  const docker = path.join(work, 'docker');
  fs.writeFileSync(docker, '#!/bin/sh\nexit 1\n');
  fs.chmodSync(docker, 0o755);

  // Stands in for the soiree binary: records whether it was given a database
  // and then waits to be killed. `exec`, so the signal the launcher sends
  // reaches the process that is actually sleeping.
  const server = path.join(work, 'soiree');
  fs.writeFileSync(server, '#!/bin/sh\nprintf %s "${DATABASE_URL-unset}" > "$SOIREE_TEST_MARKER"\nexec sleep 600\n');
  fs.chmodSync(server, 0o755);
});

after(() => fs.rmSync(work, { recursive: true, force: true }));

beforeEach(() => fs.rmSync(marker, { force: true }));

/**
 * Starts the launcher with the stub `docker` first on PATH.
 *
 * The variables the launcher reads are cleared rather than inherited: a
 * developer's shell with a database in it would otherwise decide the test.
 */
function launch(extra) {
  const env = Object.assign({}, process.env, {
    PATH: `${work}:${process.env.PATH}`,
    SOIREE_TEST_MARKER: marker,
    SOIREE_E2E_DATABASE_URL: '',
    SOIREE_E2E_REQUIRE_BACKENDS: '',
  }, extra);
  const child = spawn(process.execPath, [launcher, path.join(work, 'soiree')], {
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stderr = '';
  let exited = false;
  child.stderr.on('data', (chunk) => { stderr += chunk; });
  child.stdout.resume();
  const ended = new Promise((resolve) => {
    child.on('exit', (code) => { exited = true; resolve(code); });
  });
  // Bounded, because the case this file is about is a launcher that keeps
  // running when it should not: waiting on `ended` alone would hang the run
  // rather than report it.
  const endedWithin = async (timeoutMs) => {
    const deadline = Date.now() + timeoutMs;
    while (!exited && Date.now() < deadline) await sleep(50);
    return exited ? ended : null;
  };
  return { child, ended, endedWithin, stderr: () => stderr };
}

/** Resolves once the stand-in server has written its marker, or gives up. */
async function waitForStart(timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (fs.existsSync(marker)) return true;
    await sleep(50);
  }
  return false;
}

test('with the backends required, no database fails the run', async () => {
  const run = launch({ SOIREE_E2E_REQUIRE_BACKENDS: '1' });
  const finished = await run.endedWithin(10_000);
  if (finished === null) {
    run.child.kill('SIGTERM');
    await run.ended;
    assert.fail(`the launcher kept running with no database:\n${run.stderr()}`);
  }
  const code = await finished;
  assert.equal(code, 1, `the launcher exited ${code}:\n${run.stderr()}`);
  assert.match(run.stderr(), /no database/);
  // Playwright has to see the webServer command fail, rather than a server
  // that answers /healthz while the specs needing a database skip themselves.
  assert.equal(fs.existsSync(marker), false, 'the server was started anyway');
});

test('without them, a machine with no Docker still gets a server', async () => {
  const run = launch({});
  const started = await waitForStart(10_000);
  run.child.kill('SIGTERM');
  await run.ended;
  assert.ok(started, `the launcher never started the server:\n${run.stderr()}`);
  assert.equal(fs.readFileSync(marker, 'utf8'), 'unset', 'it was given a database');
});
