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

const repoRoot = path.resolve(__dirname, '..');
const binary = path.join(__dirname, '.tmp', 'soiree');

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

  webServer: {
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
      // Synthetic throughout. No real event, no real people.
      SOIREE_EVENT_NAME: 'Rehearsal Dinner (e2e)',
      SOIREE_EVENT_TAGLINE: 'Synthetic fixture data',
      SOIREE_EVENT_DATE: '2030-06-12T00:00:00Z',
      SOIREE_CURRENCY: 'EUR',
      SOIREE_LOCALE: 'en-US',
      SOIREE_BUDGET_CEILING: '0',
      // Off: the first-run screen only exists when there is nothing to show,
      // and every other test builds the data it needs, so no test depends on
      // seed values it did not write.
      SOIREE_DEMO_DATA: 'false',
    },
  },
});
