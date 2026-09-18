// @ts-check
/*
 * Where the test servers live.
 *
 * Shared by playwright.config.js and by the specs that address a server other
 * than the default one, so a port is written down once. Everything derives
 * from SOIREE_E2E_PORT: a fixed port on a shared machine is a collision
 * waiting to happen, and moving one without moving the rest is worse than not
 * moving any.
 */
const PORT = Number(process.env.SOIREE_E2E_PORT || 8099);

// Three site listeners: the default instance, an instance configured
// differently (no event date, Dutch locale), and one with a database behind
// it. All three run the same binary from the same source.
const ALT_PORT = PORT + 1;
const API_PORT = PORT + 2;

// A metrics listener each. The exposition has its own port and defaults to
// :9090, so two servers left on the default fight over it and the second
// exits before a single test runs.
const METRICS_PORT = PORT + 1000;
const ALT_METRICS_PORT = ALT_PORT + 1000;
const API_METRICS_PORT = API_PORT + 1000;

// The throwaway PostgreSQL the API instance talks to, published on loopback.
const PG_PORT = Number(process.env.SOIREE_E2E_PG_PORT || PORT + 2000);

// The first admin, seeded into the API instance by SOIREE_BOOTSTRAP_*. The
// plan API is behind a session, so anything reaching it directly — a raw
// request context rather than a page — has to sign in like a browser would.
const ADMIN_EMAIL = 'ada@example.test';
const ADMIN_PASSWORD = 'rehearsal-dinner-e2e';

module.exports = {
  ADMIN_EMAIL,
  ADMIN_PASSWORD,
  PORT,
  ALT_PORT,
  API_PORT,
  METRICS_PORT,
  ALT_METRICS_PORT,
  API_METRICS_PORT,
  PG_PORT,
  BASE_URL: `http://127.0.0.1:${PORT}`,
  ALT_URL: `http://127.0.0.1:${ALT_PORT}`,
  API_URL: `http://127.0.0.1:${API_PORT}`,
  // The same instance as API_URL, reached by name rather than by address.
  //
  // It is a second URL rather than a second server because of one rule in the
  // WebAuthn specification: a relying party id is a domain, and an IP address
  // is not one. A deployment reached at 127.0.0.1 gets password login and
  // nothing else — see config.loadPasskeys — so the accounts spec drives this
  // origin, which the API instance is also configured with as its base URL.
  // Both names resolve to the same loopback listener and the same database.
  AUTH_URL: `http://localhost:${API_PORT}`,
};
