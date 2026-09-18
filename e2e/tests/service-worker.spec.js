// @ts-check
/*
 * The one place a real service worker runs. Every other spec blocks it (see
 * playwright.config.js), and for three releases that hid a worker no browser
 * could parse: the server had escaped it as HTML, `i < n` reached the browser
 * as `i &lt; n`, registration failed, and the page - which does not report a
 * failed registration - told people their browser could not take reminders.
 * Reading the file on the server was never going to find that. Running it does.
 */
const vm = require('node:vm');
const { test, expect } = require('@playwright/test');
const { BASE_URL } = require('../servers');

test('the service worker the server sends is a script a browser can run', async ({ request }) => {
  const res = await request.get('/sw.js');
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toContain('javascript');
  const script = await res.text();
  // Compiled, not run: a syntax error is thrown here, with its line number.
  expect(() => new vm.Script(script, { filename: 'sw.js' })).not.toThrow();
});

test('a browser that opens the page ends up with an active worker and the shell in its cache', async ({ browser }) => {
  const context = await browser.newContext({ baseURL: BASE_URL, serviceWorkers: 'allow' });
  try {
    const page = await context.newPage();
    await page.goto('/');
    // `ready` settles only once a worker is active, so a worker that does not
    // parse leaves this waiting; the bound turns that into a failure.
    const state = await page.evaluate(() => Promise.race([
      navigator.serviceWorker.ready.then((reg) => (reg.active ? reg.active.state : 'none')),
      new Promise((resolve) => { setTimeout(() => resolve('never ready'), 15000); }),
    ]));
    expect(['activating', 'activated']).toContain(state);

    // Install has finished by the time a worker is active, so the precache is
    // complete: the shell and the script it needs are there to be served offline.
    const cached = await page.evaluate(async () => {
      const names = await caches.keys();
      const urls = [];
      for (const name of names) {
        const keys = await (await caches.open(name)).keys();
        keys.forEach((k) => urls.push(new URL(k.url).pathname));
      }
      return urls;
    });
    expect(cached).toContain('/');
    expect(cached.some((u) => /^\/assets\/app\.[0-9a-f]+\.js$/.test(u))).toBe(true);
  } finally {
    await context.close();
  }
});
