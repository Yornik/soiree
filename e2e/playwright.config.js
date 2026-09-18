// @ts-check
/*
 * Browser-level tests for soiree.
 *
 * Everything else in this repository is tested at the Go layer, which verifies
 * that the right bytes are served. It cannot verify that the page works — and
 * soiree is mostly frontend, so that is the larger half.
 *
 * This suite is deliberately outside the Go module: it adds nothing to
 * go.mod/go.sum, so `go mod download` in the Dockerfile is untouched and the
 * README's claim that the Go compiler is the only build dependency stays true.
 * `go test ./...` and `go test -short ./...` do not know this directory exists.
 *
 * Running it locally:
 *
 *     cd e2e
 *     npm ci
 *     npx playwright install chromium   # one-off browser download
 *     npm test                          # or: npm run test:ui
 *
 * `npx playwright install --with-deps chromium` also installs the system
 * libraries Chromium needs, but wants root. On a machine where you do not have
 * it, install libnss3, libnspr4 and libasound2 by hand (that is the whole list
 * on Ubuntu 22.04 with a desktop already present).
 *
 * The tests build and start the real binary — no stub, no mock server.
 *
 * Three instances, because there are three deployments worth testing and two
 * of them are configuration branches no test can reach from inside the page:
 *
 *   BASE_URL — the default: an event date, EUR, en-US, and no database. Most
 *              tests live here, share the one instance, and stay independent
 *              because their state is in localStorage, which Playwright
 *              isolates per test.
 *   ALT_URL  — no event date, Dutch locale, no database.
 *   API_URL  — the same binary with PostgreSQL behind it, which is the only
 *              place /api/v1 exists at all. Its tests run serially and reset
 *              the plan between them, because that state is genuinely shared.
 *              If Docker is unavailable the launcher starts this instance
 *              without a database and those tests skip themselves.
 */
const path = require('path');
const { defineConfig, devices } = require('@playwright/test');

const {
  PORT, ALT_PORT, API_PORT,
  METRICS_PORT, ALT_METRICS_PORT, API_METRICS_PORT,
  BASE_URL, ALT_URL, API_URL, AUTH_URL,
} = require('./servers');

// The bootstrap admin the accounts spec signs in as. Synthetic, and only ever
// reachable on a throwaway container that lives for the length of one run —
// the same shape as the fixture passwords in the Go tests. Long enough to
// clear the server's twelve-character minimum, which refuses anything shorter
// at startup rather than creating an account nobody can use.
const E2E_ADMIN = 'ada@example.test';
const E2E_ADMIN_PASSWORD = 'rehearsal-dinner-e2e';

const repoRoot = path.resolve(__dirname, '..');
const binary = path.join(__dirname, '.tmp', 'soiree');
// Its own output path per server: Playwright starts them in parallel, and two
// `go build -o` racing for the same file is a truncated binary waiting to
// happen.
const altBinary = path.join(__dirname, '.tmp', 'soiree-alt');
const apiBinary = path.join(__dirname, '.tmp', 'soiree-api');
const apiLauncher = path.join(__dirname, 'scripts', 'api-server.js');

module.exports = defineConfig({
  testDir: './tests',
  fullyParallel: true,

  // A test that fails randomly gets ignored and then deleted, so there is no
  // retry budget to hide behind: every wait in this suite is on a condition,
  // never on a clock. If something here is flaky it is a bug in the test (or
  // in the app) and it should be visible as one.
  retries: 0,
  forbidOnly: !!process.env.CI,
  workers: process.env.CI ? 2 : undefined,

  timeout: 30_000,
  expect: { timeout: 5_000 },

  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never' }], ['list']]
    : [['list']],

  // Reachable from a test without hardcoding a port twice. `./servers` is the
  // same values for the specs that need one at module scope.
  metadata: {
    altBaseURL: ALT_URL,
    apiBaseURL: API_URL,
    authBaseURL: AUTH_URL,
    adminEmail: E2E_ADMIN,
    adminPassword: E2E_ADMIN_PASSWORD,
  },

  use: {
    baseURL: BASE_URL,
    // The service worker is a cache in front of the app. Its contents are
    // checked in the Go tests (TestServiceWorkerPrecachesRealURLs); letting it
    // install here would put a race between registration and the reload in the
    // persistence test, for no extra coverage.
    serviceWorkers: 'block',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
  },

  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],

  webServer: [{
    // The real binary, built from source. Doing it inside the webServer
    // command rather than in a globalSetup keeps the ordering unambiguous:
    // Playwright will not start the tests until /healthz answers.
    command: `go build -o ${JSON.stringify(binary)} ./cmd/soiree && exec ${JSON.stringify(binary)}`,
    cwd: repoRoot,
    url: `${BASE_URL}/healthz`,
    // Never reuse: if something else is already on this port the run must fail
    // loudly rather than quietly testing whatever that is.
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
    env: {
      ...process.env,
      SOIREE_LISTEN_ADDR: `127.0.0.1:${PORT}`,
      // Each server needs its own metrics port. The exposition moved to a
      // listener of its own, and it defaults to :9090 — so two servers left on
      // the default fight over it and the second exits before a single test
      // runs. Bound to loopback because nothing scrapes these.
      SOIREE_METRICS_ADDR: `127.0.0.1:${METRICS_PORT}`,
      // Synthetic throughout. No real event, no real people.
      SOIREE_EVENT_NAME: 'Rehearsal Dinner (e2e)',
      SOIREE_EVENT_TAGLINE: 'Synthetic fixture data',
      // Carries a time of day, which is how a real evening is configured and
      // the case that catches a countdown rounding hours instead of comparing
      // calendar dates. Formatted with timeZone: 'UTC' in the page, so the
      // date shown beside it is still June 12.
      SOIREE_EVENT_DATE: '2030-06-12T19:00:00Z',
      SOIREE_CURRENCY: 'EUR',
      SOIREE_LOCALE: 'en-US',
      SOIREE_BUDGET_CEILING: '0',
      // Off: the first-run screen only exists when there is nothing to show,
      // and every other test builds the data it needs, so no test depends on
      // seed values it did not write.
      SOIREE_DEMO_DATA: 'false',
      // Explicitly none. This is the no-database deployment — `docker run`
      // with no arguments, a self-hoster without PostgreSQL — and an inherited
      // DATABASE_URL from the developer's shell would quietly turn it into a
      // different one, with the localStorage tests testing nothing.
      DATABASE_URL: '',
    },
  }, {
    // Same source, different environment. Built to its own path so the two
    // `go build`s above and here cannot collide.
    command: `go build -o ${JSON.stringify(altBinary)} ./cmd/soiree && exec ${JSON.stringify(altBinary)}`,
    cwd: repoRoot,
    url: `${ALT_URL}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
    env: {
      ...process.env,
      SOIREE_LISTEN_ADDR: `127.0.0.1:${ALT_PORT}`,
      SOIREE_METRICS_ADDR: `127.0.0.1:${ALT_METRICS_PORT}`,
      SOIREE_EVENT_NAME: 'Ongedateerd Feest (e2e)',
      SOIREE_EVENT_TAGLINE: '',
      // Deliberately unset: this is the "no date configured" deployment.
      SOIREE_EVENT_DATE: '',
      SOIREE_CURRENCY: 'EUR',
      SOIREE_LOCALE: 'nl-NL',
      SOIREE_BUDGET_CEILING: '0',
      SOIREE_DEMO_DATA: 'false',
      DATABASE_URL: '',
    },
  }, {
    // The same binary again, with PostgreSQL behind it. The launcher starts a
    // throwaway container, waits for it, and execs the server; with no Docker
    // it starts the server without a database instead, so this entry always
    // answers /healthz and the API specs decide for themselves whether there
    // is anything to test. See scripts/api-server.js.
    command: `go build -o ${JSON.stringify(apiBinary)} ./cmd/soiree && exec node ${JSON.stringify(apiLauncher)} ${JSON.stringify(apiBinary)}`,
    cwd: repoRoot,
    url: `${API_URL}/healthz`,
    reuseExistingServer: false,
    // Longer than the other two: a machine that has never run this may be
    // pulling the postgres image, and the launcher waits up to three minutes
    // for the database before giving up and starting without one.
    timeout: 300_000,
    // Without this Playwright force-kills the whole process group, and a
    // SIGKILL cannot be caught — so the launcher never gets to take its
    // container down and every run leaves a PostgreSQL behind. The other two
    // servers own nothing but themselves and are fine being killed outright.
    gracefulShutdown: { signal: 'SIGTERM', timeout: 15_000 },
    stdout: 'pipe',
    stderr: 'pipe',
    env: {
      ...process.env,
      SOIREE_LISTEN_ADDR: `127.0.0.1:${API_PORT}`,
      SOIREE_METRICS_ADDR: `127.0.0.1:${API_METRICS_PORT}`,
      SOIREE_EVENT_NAME: 'Shared Rehearsal Dinner (e2e)',
      SOIREE_EVENT_TAGLINE: 'Synthetic fixture data',
      SOIREE_EVENT_DATE: '2030-06-12T19:00:00Z',
      // EUR rather than the deployment's IDR on purpose. A two-decimal
      // currency is the one where a wrong wire format is visible: against a
      // zero-decimal currency every money bug in this file looks like a pass.
      SOIREE_CURRENCY: 'EUR',
      SOIREE_LOCALE: 'en-US',
      SOIREE_BUDGET_CEILING: '0',
      SOIREE_DEMO_DATA: 'false',

      // Accounts exist wherever a database does, so this is also the only
      // instance with a sign-in to test. Three settings turn it into one:
      //
      //   BASE_URL     the origin set-password links are built against, and
      //                the relying party passkeys are scoped to. `localhost`
      //                rather than 127.0.0.1 because a relying party id is a
      //                domain and an address is not one; the server refuses to
      //                derive one from an address and leaves passkeys off.
      //   BOOTSTRAP_*  the first admin, since every other account is created
      //                by one and an empty database has nobody to start from.
      //                With a password set the account starts active and can
      //                sign in immediately, which is what the spec needs.
      //
      // No SMTP: that is deliberate, and it is the branch worth testing. With
      // no relay the server hands the set-password link back to the admin who
      // asked for it, and the interface has to surface it — otherwise such a
      // deployment can never onboard anybody.
      SOIREE_BASE_URL: AUTH_URL,
      SOIREE_BOOTSTRAP_ADMIN: E2E_ADMIN,
      SOIREE_BOOTSTRAP_PASSWORD: E2E_ADMIN_PASSWORD,
    },
  }],
});
