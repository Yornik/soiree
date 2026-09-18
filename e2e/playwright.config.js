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
 * The tests build and start the real binary — no stub, no mock server. The
 * server is read-only, so all tests share one instance; state lives in
 * localStorage, which Playwright isolates per test.
 */
const path = require('path');
const { defineConfig, devices } = require('@playwright/test');

// Overridable, because a fixed port on a shared machine is a collision waiting
// to happen.
const PORT = Number(process.env.SOIREE_E2E_PORT || 8099);
const BASE_URL = `http://127.0.0.1:${PORT}`;

// A second instance, configured differently: no event date, and a Dutch
// locale. Two of the three things added here are branches on configuration
// rather than on anything a test can do from inside the page — "with no event
// date there is no after" and "the interface language follows the configured
// locale" — and the only honest way to test a configuration branch is to
// configure it.
const ALT_PORT = PORT + 1;
// Metrics listeners, one per server. Derived from the site ports rather than
// fixed, so overriding SOIREE_E2E_PORT moves all four together.
const METRICS_PORT = PORT + 1000;
const ALT_METRICS_PORT = ALT_PORT + 1000;
const ALT_URL = `http://127.0.0.1:${ALT_PORT}`;

const repoRoot = path.resolve(__dirname, '..');
const binary = path.join(__dirname, '.tmp', 'soiree');
// Its own output path: Playwright starts the two servers in parallel, and two
// `go build -o` racing for the same file is a truncated binary waiting to
// happen.
const altBinary = path.join(__dirname, '.tmp', 'soiree-alt');

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

  // Reachable from a test as `altBaseURL` without hardcoding a port twice.
  metadata: { altBaseURL: ALT_URL },

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
    },
  }],
});
