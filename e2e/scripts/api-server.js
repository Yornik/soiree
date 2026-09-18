#!/usr/bin/env node
// @ts-check
'use strict';
/*
 * Starts the soiree binary with a database behind it, so the browser tests can
 * exercise the half of the application that only exists when there is one.
 *
 * The database is a throwaway PostgreSQL container. It is started here, inside
 * the Playwright `webServer` command, rather than in a globalSetup — that way
 * there is no question about which of the two runs first, and the container's
 * lifetime is exactly the lifetime of the server that needs it.
 *
 * The important property is what happens when Docker is not available. This
 * script then starts the binary with **no** DATABASE_URL, which is a perfectly
 * good soiree: /healthz answers, Playwright is satisfied, and the API specs
 * see a 404 from /api/v1/plan and skip themselves. A machine without Docker
 * runs a smaller suite rather than a failing one.
 *
 * Set SOIREE_E2E_DATABASE_URL to point at a PostgreSQL you already have and no
 * container is started at all.
 *
 * Usage: node api-server.js <path-to-soiree-binary>
 */
const { spawn, spawnSync } = require('child_process');
const http = require('http');
const net = require('net');

const { PG_PORT, S3_PORT, S3_URL } = require('../servers');

const binary = process.argv[2];
if (!binary) {
  console.error('api-server: usage: node api-server.js <path-to-soiree-binary>');
  process.exit(2);
}

const IMAGE = process.env.SOIREE_E2E_PG_IMAGE || 'postgres:18-alpine';
// Named after the port, so overriding SOIREE_E2E_PORT gives a second run on
// the same machine its own container instead of tearing down the first one's.
const CONTAINER = process.env.SOIREE_E2E_PG_CONTAINER || `soiree-e2e-pg-${PG_PORT}`;
const PG_USER = 'soiree';
const PG_PASSWORD = 'soiree';
const PG_DB = 'soiree';

// The bucket. The same image, pinned the same way, as internal/s3test uses for
// the Go tests; quay.io because the project no longer publishes to Docker Hub.
const S3_IMAGE = process.env.SOIREE_E2E_S3_IMAGE ||
  'quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e';
const S3_CONTAINER = process.env.SOIREE_E2E_S3_CONTAINER || `soiree-e2e-s3-${S3_PORT}`;
const S3_KEY = 'soiree-e2e';
const S3_SECRET = 'soiree-e2e-secret';
const S3_BUCKET = 'soiree-e2e';

// Generous: the first run on a machine may be pulling the image. The Playwright
// webServer timeout for this entry has to be at least this much again.
const READY_TIMEOUT_MS = 180_000;

const note = (msg) => console.error(`api-server: ${msg}`);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function sh(args, opts) {
  return spawnSync('docker', args, Object.assign({ encoding: 'utf8' }, opts || {}));
}

function dockerAvailable() {
  const probe = sh(['info'], { stdio: 'ignore' });
  return !probe.error && probe.status === 0;
}

function removeContainer() {
  sh(['rm', '-f', CONTAINER], { stdio: 'ignore' });
}

function removeBucket() {
  sh(['rm', '-f', S3_CONTAINER], { stdio: 'ignore' });
}

/**
 * Resolves true for a 2xx, false for anything else including no answer.
 *
 * `http` and not the global `fetch`, deliberately. Under Node 20 a fetch to a
 * port nothing is listening on yet ended this process — no rejection, no
 * message, exit status 0 — which Playwright reports only as "exited early".
 */
function httpOK(url) {
  return new Promise((resolve) => {
    const req = http.get(url, (res) => {
      res.resume();
      resolve(res.statusCode >= 200 && res.statusCode < 300);
    });
    req.setTimeout(1000, () => { req.destroy(); resolve(false); });
    req.once('error', () => resolve(false));
  });
}

/**
 * Brings up a bucket, or returns false and leaves attachments off.
 *
 * Off is a supported deployment, so a machine that cannot pull the image runs
 * every other spec and the attachment specs skip themselves, exactly as the API
 * specs do with no database. A directory under MinIO's data root is a bucket,
 * which saves these tests a CreateBucket call soiree has no reason to be able
 * to make. MinIO answers CORS preflights from any origin by default; a real
 * bucket needs the rule in docs/operating.md.
 */
async function startBucket() {
  removeBucket();
  const started = sh([
    'run', '-d', '--name', S3_CONTAINER,
    '-e', `MINIO_ROOT_USER=${S3_KEY}`,
    '-e', `MINIO_ROOT_PASSWORD=${S3_SECRET}`,
    '-p', `127.0.0.1:${S3_PORT}:9000`,
    '--entrypoint', 'sh',
    S3_IMAGE,
    '-c', `mkdir -p /data/${S3_BUCKET} && exec minio server /data`,
  ]);
  if (started.status !== 0) {
    note(`could not start the bucket (${(started.stderr || '').trim()}) — attachments are off for this run`);
    return false;
  }
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    if (await httpOK(`${S3_URL}/minio/health/ready`)) return true;
    await sleep(300);
  }
  note('the bucket did not become ready in time — attachments are off for this run');
  removeBucket();
  return false;
}

/** Resolves once something is listening on the published port. */
function portOpen(port) {
  return new Promise((resolve) => {
    const socket = net.connect({ host: '127.0.0.1', port });
    const done = (ok) => {
      socket.destroy();
      resolve(ok);
    };
    socket.setTimeout(1000);
    socket.once('connect', () => done(true));
    socket.once('error', () => done(false));
    socket.once('timeout', () => done(false));
  });
}

/**
 * Brings up the container and waits until it will actually answer a query.
 *
 * `pg_isready` alone is not enough: the official image starts a private server
 * to run its initialisation scripts, and that one reports ready on the unix
 * socket while nothing is listening on the published port yet. So both are
 * checked — a real SELECT inside, and a TCP connection from out here.
 */
async function startPostgres() {
  // A crashed previous run leaves the container holding the port, and the
  // error it produces ("port is already allocated") reads like a machine
  // problem rather than a leftover.
  removeContainer();

  const started = sh([
    'run', '-d', '--name', CONTAINER,
    '-e', `POSTGRES_USER=${PG_USER}`,
    '-e', `POSTGRES_PASSWORD=${PG_PASSWORD}`,
    '-e', `POSTGRES_DB=${PG_DB}`,
    '-p', `127.0.0.1:${PG_PORT}:5432`,
    IMAGE,
  ]);
  if (started.status !== 0) {
    note(`could not start ${IMAGE}: ${(started.stderr || '').trim()}`);
    return null;
  }

  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    const query = sh(['exec', CONTAINER, 'psql', '-U', PG_USER, '-d', PG_DB, '-tAc', 'select 1'], {
      stdio: 'ignore',
    });
    if (query.status === 0 && (await portOpen(PG_PORT))) {
      return `postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable`;
    }
    await sleep(500);
  }

  note('postgres did not become ready in time');
  removeContainer();
  return null;
}

async function resolveDatabase() {
  const provided = (process.env.SOIREE_E2E_DATABASE_URL || '').trim();
  if (provided) {
    note('using SOIREE_E2E_DATABASE_URL; no container started');
    return { dsn: provided, container: false };
  }
  if (!dockerAvailable()) {
    note('Docker is not available — starting soiree with no database, so the API specs will skip');
    return { dsn: '', container: false };
  }
  const dsn = await startPostgres();
  return { dsn: dsn || '', container: !!dsn };
}

async function main() {
  const { dsn, container } = await resolveDatabase();

  const env = Object.assign({}, process.env);
  // Explicit either way: an inherited DATABASE_URL must not turn the
  // no-database fallback into a half-configured server.
  if (dsn) env.DATABASE_URL = dsn;
  else delete env.DATABASE_URL;

  // Attachments need the database too, so there is no bucket without one.
  // All five or none: soiree refuses to start on a half-configured bucket.
  for (const name of Object.keys(env)) if (name.startsWith('SOIREE_S3_')) delete env[name];
  const bucket = !!dsn && container && (await startBucket());
  if (bucket) {
    env.SOIREE_S3_ENDPOINT = S3_URL;
    env.SOIREE_S3_REGION = 'us-east-1';
    env.SOIREE_S3_BUCKET = S3_BUCKET;
    env.SOIREE_S3_ACCESS_KEY_ID = S3_KEY;
    env.SOIREE_S3_SECRET_ACCESS_KEY = S3_SECRET;
    // Small, so a test can go over the limit without making a large file.
    env.SOIREE_ATTACHMENT_MAX_MB = '1';
    env.SOIREE_ATTACHMENTS_TOTAL_MB = '8';
  }

  // The binary runs its own migrations before it starts listening, so by the
  // time /healthz answers the schema is in place and /api/v1/plan is real.
  const child = spawn(binary, [], { stdio: 'inherit', env });

  let cleaning = false;
  const cleanUp = (signal) => {
    if (cleaning) return;
    cleaning = true;
    if (!child.killed) child.kill(signal === 'SIGINT' ? 'SIGINT' : 'SIGTERM');
    if (container) removeContainer();
    if (bucket) removeBucket();
  };

  ['SIGINT', 'SIGTERM', 'SIGHUP'].forEach((sig) => {
    process.on(sig, () => {
      cleanUp(sig);
      process.exit(0);
    });
  });
  // Covers the paths a signal handler does not: Playwright closing stdio, or
  // this process throwing. A container outliving the run is a port nobody can
  // reuse and a database nobody remembers starting.
  process.on('exit', () => cleanUp('SIGTERM'));

  child.on('exit', (code, signal) => {
    cleanUp('SIGTERM');
    process.exit(code === null ? (signal ? 1 : 0) : code);
  });
}

main().catch((err) => {
  note(String(err));
  removeContainer();
  removeBucket();
  process.exit(1);
});
