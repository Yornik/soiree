// @ts-check
/*
 * The worker, run for real. Almost every spec blocks service workers (see
 * playwright.config.js; the reminders switch in api.spec.js is the other
 * exception), and for three releases that hid a worker no browser
 * could parse: the server had escaped it as HTML, `i < n` reached the browser
 * as `i &lt; n`, registration failed, and the page - which does not report a
 * failed registration - told people their browser could not take reminders.
 * Reading the file on the server was never going to find that. Running it does.
 */
const vm = require('node:vm');
const { test, expect, chromium } = require('@playwright/test');
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

    // Install has finished by the time a worker is active, and nothing here
    // stands in its way, so the precache is complete: the shell and the script
    // it needs are there to be served offline. A precache that loses a request
    // is the test below.
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

test('under a working worker, only the planner itself is answered from the cache', async ({ browser }) => {
  const context = await browser.newContext({ baseURL: BASE_URL, serviceWorkers: 'allow' });
  try {
    const page = await context.newPage();
    await page.goto('/');
    // The worker claims the page once it is active; from then on every
    // request the page makes, navigations included, goes through it.
    await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 15000 });

    // A link to anything else on the site is a navigation too - an attached
    // file is opened with one. It has to get the server's answer. Answered
    // with the cached shell, it would open the planner where the file should be.
    const res = await page.goto('/healthz');
    expect(res && res.status()).toBe(200);
    expect((await page.locator('body').innerText()).trim()).toBe('ok');
    await expect(page.locator('#daysNum')).toHaveCount(0);

    // And the planner still comes from the worker: it opens with the network gone.
    await page.goto('/');
    await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 15000 });
    await context.setOffline(true);
    await page.reload();
    await expect(page.locator('#daysNum')).toBeVisible();
  } finally {
    await context.setOffline(false).catch(() => {});
    await context.close();
  }
});

/*
 * A deploy that gets half way, which is the one the offline copy has to come
 * through.
 *
 * The worker names its cache after the version it was rendered with, and
 * activate deletes every cache that is not that one. Install fills the new
 * cache with a single addAll, and addAll is all or nothing: one lost request
 * and it stores nothing at all. An install that shrugs that off therefore ends
 * with the previous version's complete cache deleted and the new one empty,
 * and the next launch with no connection gets a browser error page where the
 * planner should be: the cache is gone in exactly the conditions it exists
 * for, and on the far links where a request goes missing in the first place.
 *
 * Two things make that deploy out of one running server. The arriving worker
 * is asked for at a URL of its own, because one server serves one version and
 * a second request for a script URL the browser already holds never reaches
 * it; the body it gets back is the worker the server just served with its
 * VERSION changed, which for the cache is the whole of what a new build is.
 * And the shell answers 503: "/" is the precache entry that cannot be dodged,
 * since it is no-cache and always asks the network, and a rollout is exactly
 * when the server it asks may be the one going away.
 */
test('a deploy whose precache cannot finish leaves the copy that works in place', async ({ browser, request }) => {
  const arriving = (await (await request.get('/sw.js')).text())
    .replace(/var VERSION = "[^"]*"/, 'var VERSION = "the next build"');
  const context = await browser.newContext({ baseURL: BASE_URL, serviceWorkers: 'allow' });
  try {
    const page = await context.newPage();
    await page.goto('/');
    // The cache rather than the worker: a worker is active a moment before the
    // install that fills it has finished.
    await expect.poll(() => page.evaluate(() => caches.match('/').then((hit) => !!hit)),
      { timeout: 15_000 }).toBe(true);

    await context.route(/\/sw\.js\?deploy=2$/, (route) => route.fulfill({ contentType: 'text/javascript', body: arriving }));
    await context.route(`${BASE_URL}/`, (route) => route.fulfill({ status: 503, contentType: 'text/plain', body: 'deploying' }));

    const fate = await page.evaluate(() => new Promise((resolve) => {
      navigator.serviceWorker.ready.then((reg) => {
        reg.addEventListener('updatefound', () => {
          const next = reg.installing;
          // Terminal either way, so nothing here waits on a clock: the arriving
          // worker is refused, or it is in charge. "activated" is reached only
          // once activate has finished deleting what it means to delete.
          const settle = () => {
            if (next.state === 'redundant') resolve('refused');
            if (next.state === 'activated') resolve('took over');
          };
          next.addEventListener('statechange', settle);
          settle();
        });
        // Rejected when the install is refused, which is the point of it.
        navigator.serviceWorker.register('/sw.js?deploy=2').catch(() => {});
      });
    }));
    expect(fate).toBe('refused');
    expect(await page.evaluate(() => caches.match('/').then((hit) => !!hit))).toBe(true);

    // And what that cache is for. The routes go first, so that offline is
    // offline rather than a fulfilled 503.
    await context.unrouteAll();
    await context.setOffline(true);
    await page.reload();
    await expect(page.locator('#daysNum')).toBeVisible();
  } finally {
    await context.setOffline(false).catch(() => {});
    await context.close();
  }
});

/*
 * The half of a deploy the page has to do.
 *
 * A worker taking over from another worker is the moment a reload would gain
 * something: the arriving worker precaches the new shell before it activates,
 * and activating deletes the cache the outgoing one was served from. Before
 * that moment a reload is served the same old shell out of the same old cache,
 * which is how a planner ends up needing two of them.
 */
test('a deploy that takes an open planner over says so, once there is something to reload into', async ({ browser, request }) => {
  const arriving = (await (await request.get('/sw.js')).text())
    .replace(/var VERSION = "[^"]*"/, 'var VERSION = "the next build"');
  const context = await browser.newContext({ baseURL: BASE_URL, serviceWorkers: 'allow' });
  try {
    const page = await context.newPage();
    await page.goto('/');
    await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 15000 });
    // Opened again, so that this page starts its life under a worker. The
    // first worker a page ever gets claims it exactly as a deploy does, and
    // that one is not news: there is nothing newer to reload into.
    await page.reload();
    await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 15000 });
    await expect(page.locator('#dataMsg')).toHaveText('');

    await context.route(/\/sw\.js\?deploy=2$/, (route) => route.fulfill({ contentType: 'text/javascript', body: arriving }));
    await page.evaluate(() => navigator.serviceWorker.register('/sw.js?deploy=2').then(() => {}));

    await expect(page.locator('#dataMsg')).toHaveText(/A newer version of the planner is ready/);
    // And there is: the arriving worker's own cache holds the shell already,
    // because a precache that has not finished is a worker that has not
    // activated and cannot have taken anything over.
    expect(await page.evaluate(() => caches.open('soiree-the next build')
      .then((c) => c.match('/')).then((hit) => !!hit))).toBe(true);
  } finally {
    await context.close();
  }
});

/*
 * The two handlers reminders end in: `push` and `notificationclick`.
 *
 * Neither had ever run. They arrived in the same change as the line that
 * stopped the worker parsing, so from the day they were written there was no
 * worker for a push to reach.
 *
 * In the full Chromium rather than the headless shell, because the shell denies
 * notifications whatever the context was granted, and a worker that may not
 * show a notification has its push dropped. There is no push service in a test
 * browser either, so the message is handed to the worker over the DevTools
 * protocol — past the network, at the same door a real one comes in by.
 */
const DIGEST = { title: 'Three things need attention', body: 'One of them is late.', url: `${BASE_URL}/`, tag: 'soiree-deadlines' };

async function plannerWithWorker(path) {
  const full = await chromium.launch({ channel: 'chromium' });
  const context = await full.newContext({ baseURL: BASE_URL, serviceWorkers: 'allow', permissions: ['notifications'] });
  const page = await context.newPage();
  await page.goto(path);
  await page.waitForFunction(() => !!navigator.serviceWorker.controller, null, { timeout: 15000 });

  const cdp = await context.newCDPSession(page);
  const registrations = [];
  cdp.on('ServiceWorker.workerRegistrationUpdated', (e) => registrations.push(...e.registrations));
  await cdp.send('ServiceWorker.enable');
  await expect.poll(() => registrations.some((r) => r.scopeURL === `${BASE_URL}/`)).toBe(true);
  const { registrationId } = registrations.find((r) => r.scopeURL === `${BASE_URL}/`);

  const shown = () => page.evaluate(async () => {
    const reg = await navigator.serviceWorker.ready;
    return (await reg.getNotifications()).map((n) => ({ title: n.title, body: n.body, tag: n.tag, renotify: n.renotify, url: n.data && n.data.url }));
  });

  return {
    full,
    context,
    page,
    /*
     * A digest delivered until it is on screen, rather than delivered once and
     * then waited for.
     *
     * Nothing acknowledges this delivery. Sent with a registration id nothing
     * owns it is still answered as a success, in three milliseconds, and no
     * notification ever follows: a message the browser dropped looks exactly
     * like one still on its way, and unlike a real push service there is
     * nothing behind this one to send it again. On a loaded machine it does
     * get dropped, five times in ninety runs of the tap test below, and the
     * wait then spent its whole budget on a notification nobody was going to
     * send. So the wait sends it again. Every digest carries the same tag,
     * which is what makes that safe: a second copy replaces the notification
     * on screen instead of joining it. It goes out only after a second of
     * quiet, an order of magnitude longer than the round trip takes when the
     * first one arrived, so a delivery merely on its way is never doubled.
     */
    push: async (data, expected) => {
      let sent = 0;
      await expect.poll(async () => {
        if (Date.now() - sent > 1000) {
          await cdp.send('ServiceWorker.deliverPushMessage', { origin: BASE_URL, registrationId, data });
          sent = Date.now();
        }
        return shown();
      }).toEqual(expected);
    },
    shown,
  };
}

test('a push that reaches the worker is shown as the server wrote it, and the next one replaces it', async () => {
  const { full, push } = await plannerWithWorker('/');
  try {
    await push(JSON.stringify(DIGEST), [{ title: DIGEST.title, body: DIGEST.body, tag: DIGEST.tag, renotify: true, url: DIGEST.url }]);

    // Same tag, so tomorrow's digest takes today's place instead of joining it.
    await push(JSON.stringify({ ...DIGEST, title: 'Four things need attention' }),
      [{ title: 'Four things need attention', body: DIGEST.body, tag: DIGEST.tag, renotify: true, url: DIGEST.url }]);

    // A payload the worker cannot read is still shown as something. The
    // subscription is userVisibleOnly: a push handled in silence is one the
    // browser counts against the site, and eventually ends the subscription for.
    await push('not json', [{ title: 'soiree', body: '', tag: DIGEST.tag, renotify: true, url: '/' }]);
  } finally {
    await full.close();
  }
});

test('tapping a reminder leaves an open planner on the screen it was on', async () => {
  // Opened at a fragment, which is what every screen of the planner is; the
  // digest opens "/".
  const { full, context, page, push, shown } = await plannerWithWorker('/#tasks');
  try {
    await page.evaluate(() => { window.__neverReloaded = true; });
    await push(JSON.stringify(DIGEST), [{ title: DIGEST.title, body: DIGEST.body, tag: DIGEST.tag, renotify: true, url: DIGEST.url }]);

    // The tap itself cannot be made from here, so the event is: the real
    // handler, in the real worker, given the real notification. What a made
    // event lacks is the user activation focus() wants, so whether the tab
    // comes forward is the part this cannot see — only that it was asked to,
    // and that nothing was asked to navigate first.
    const [worker] = context.serviceWorkers();
    await worker.evaluate(async () => {
      self.__asked = [];
      for (const name of ['navigate', 'focus']) {
        const real = WindowClient.prototype[name];
        WindowClient.prototype[name] = function watched(...args) {
          self.__asked.push(name);
          return real.apply(this, args);
        };
      }
      const [notification] = await self.registration.getNotifications();
      self.dispatchEvent(new NotificationEvent('notificationclick', { notification }));
    });
    await expect.poll(() => worker.evaluate(() => self.__asked)).toEqual(['focus']);

    // Closed, and the planner where it was. Navigated to "/" from a fragment
    // it would have been reloaded, and what was being typed gone with it.
    expect(await shown()).toEqual([]);
    await expect(page).toHaveURL(`${BASE_URL}/#tasks`);
    expect(await page.evaluate(() => window.__neverReloaded)).toBe(true);
  } finally {
    await full.close();
  }
});
