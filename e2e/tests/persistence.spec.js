// @ts-check
/*
 * State survives a reload — in the deployment that has no database.
 *
 * That is now one of two modes rather than the only one, which is why this
 * file says so. These tests run against the default instance, which is started
 * with no DATABASE_URL: the binary then registers no /api/v1 routes at all, so
 * the planner is localStorage and nothing else. It is a supported deployment,
 * not a transitional one — `docker run` with no arguments lands here, the CI
 * image smoke test checks it, and a self-hoster without PostgreSQL gets a
 * working planner out of it. Every claim below is still exactly true of it.
 *
 * The shared mode is api.spec.js, against the instance that has a database.
 *
 * Writes are debounced by 500 ms and flushed on `pagehide`, so a test that
 * edits and immediately reloads is racing the debounce. Nothing here sleeps
 * and hopes: either it waits for the write to land (`expectStored`) or it
 * provokes the flush the way leaving the page does (`flushToStorage`) and then
 * reads, which is safe because the flush is synchronous — localStorage is
 * written first and synchronously in both modes, precisely so that leaving the
 * page never has to wait on a promise.
 */
const { test, expect } = require('@playwright/test');
const {
  STORAGE_KEY,
  addBudgetLine,
  addSponsor,
  addTask,
  budgetRow,
  expectFigures,
  expectStored,
  flushToStorage,
  gotoTab,
  openPlanner,
  readStored,
} = require('./helpers');

test('an ordinary edit is persisted without anyone leaving the page', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

  // No flush provoked: this is the debounced writer doing its job on its own.
  await expectStored(
    page,
    (s) => !!s && s.budgetItems.length === 1 && s.budgetItems[0].item === 'Venue deposit',
    'the debounced write should land on its own within the expect timeout',
  );

  const stored = await readStored(page);
  expect(stored.budgetItems[0]).toMatchObject({ item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
});

test('leaving the page flushes an edit that is still inside the debounce window', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });

  await row.note.fill('Balance due one month before');
  // Straight to the flush, with no wait in between. The assertion is the
  // invariant that matters — after pagehide, storage is current — and it holds
  // whether or not the debounce happened to fire first, so there is no race in
  // either direction.
  await flushToStorage(page);

  const stored = await readStored(page);
  expect(stored.budgetItems[0].note).toBe('Balance due one month before');
});

test('a full planner comes back after a reload', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('10000');
  await page.locator('#inflationInput').fill('4');

  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await addBudgetLine(page, { item: 'Catering', unit: 45, qty: 40, paid: 0 });
  await page.locator('#splitEvenly').check();

  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada', due: '2030-01-15', status: 'in-progress' });

  await gotoTab(page, 'overview');
  await page.locator('#addWatch').click();
  await page.locator('#watchList textarea').first().fill('Venue balance is due a month out.');

  // Make the write happen before navigating, rather than trusting the browser
  // to fire pagehide early enough on a reload.
  await flushToStorage(page);
  await expectStored(page, (s) => !!s && s.budgetItems.length === 2 && s.notes.length === 1, 'state written');

  await page.reload();

  // Everything is back, and the start screen stays away.
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
  await expectFigures(page, { committed: 4300, paid: 500, outstanding: 3800, forecast: 4472 });
  await expect(page.locator('#upNextList li')).toHaveCount(1);
  await expect(page.locator('#watchList textarea').first()).toHaveValue('Venue balance is due a month out.');

  await gotoTab(page, 'budget');
  await expect(page.locator('#ceilingInput')).toHaveValue('10000');
  await expect(page.locator('#inflationInput')).toHaveValue('4');
  await expect(page.locator('#splitEvenly')).toBeChecked();
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(1);
  await expect(page.locator('#sponsorGrid .code-input').first()).toHaveValue('Rose');

  const first = budgetRow(page, 0);
  await expect(first.item).toHaveValue('Venue deposit');
  await expect(first.unit).toHaveValue('2500');
  await expect(first.paid).toHaveValue('500');
  await expect(first.committed).toHaveText('€2,500');

  await gotoTab(page, 'tasks');
  const task = page.locator('#tasksBody tr').first();
  await expect(task.locator('td').nth(0).locator('input')).toHaveValue('Confirm final guest count');
  await expect(task.locator('td').nth(1).locator('input')).toHaveValue('Ada');
  await expect(task.locator('td').nth(2).locator('input')).toHaveValue('2030-01-15');
  await expect(task.locator('select.status-select')).toHaveValue('in-progress');
});

test('with no database behind it, the page asks its two questions once and then stays off the network', async ({ page }) => {
  /** @type {string[]} */
  const apiRequests = [];
  page.on('request', (r) => {
    if (new URL(r.url()).pathname.indexOf('/api/') === 0) apiRequests.push(r.url());
  });

  /** @type {{text: string, url: string}[]} */
  const errors = [];
  page.on('console', (m) => {
    if (m.type() === 'error') errors.push({ text: m.text(), url: (m.location() || {}).url || '' });
  });
  page.on('pageerror', (e) => errors.push({ text: String(e), url: 'pageerror' }));

  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expectStored(page, (s) => !!s && s.budgetItems.length === 1, 'the write lands in localStorage');

  // Two questions at startup, each asked exactly once: "is there a database
  // here" from the planner, and "does this deployment have accounts" from the
  // accounts surface. Both were answered 404, and a 404 here is final — those
  // routes are not registered in this deployment, so asking again would only
  // be a second 404. A page that spent a round trip per keystroke
  // rediscovering that would be worse than one that never asked.
  //
  // Named rather than counted: a third probe, or either of these repeating,
  // has to fail this — which counting a total would not catch on its own.
  const probed = () => apiRequests.map((u) => new URL(u).pathname).sort();
  await expect
    .poll(probed, { message: 'each startup probe is made exactly once' })
    .toEqual(['/api/v1/auth/session', '/api/v1/plan']);

  // More edits, and a flush, and still nothing goes out.
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await flushToStorage(page);
  expect(probed()).toEqual(['/api/v1/auth/session', '/api/v1/plan']);

  // Nothing in the console that the application put there. The two 404s are
  // logged by the browser's own network stack against the URLs above — that is
  // Chromium reporting the probes, not soiree reporting a fault — so they are
  // named here rather than swept up in a blanket filter.
  const probes = ['/api/v1/plan', '/api/v1/auth/session'];
  expect(errors.filter((e) => !probes.some((p) => e.url.indexOf(p) !== -1))).toEqual([]);
});

/*
 * The same deployment, opened while it cannot be reached. The page cannot know
 * yet that there is no database, so it keeps asking, as it must for the
 * deployment that has one (api.spec.js). Two things keep that from costing
 * this one anything: nothing is said about a server to a planner that has
 * never met one, and the 404 is as final when it arrives late as when it
 * arrives first.
 */
test('opened while the origin is away, a planner that never met a server is told nothing about one, and a late 404 is still final', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await flushToStorage(page);
  await page.goto('about:blank');

  // Installed, not paused: time passes as it would, and the test may also move
  // it on, so that the page's backoff is not something to sit through.
  await page.clock.install();

  // A switch rather than route-then-unroute: see api.spec.js.
  let away = true;
  let asked = 0;
  let total = 0;
  await page.route('**/api/v1/**', (route) => {
    total += 1;
    if (route.request().url().endsWith('/api/v1/plan')) asked += 1;
    return away ? route.abort('internetdisconnected') : route.continue();
  });
  await page.goto('/');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).committed).toHaveText('€2,500');

  // Past the second failure, which is where a cached copy of a shared plan
  // gets its notice.
  for (let n = 1; n <= 3; n += 1) {
    await expect.poll(() => asked, { message: `request ${n} for the plan` }).toBeGreaterThanOrEqual(n);
    await page.clock.fastForward(30_000);
  }
  await expect.poll(() => asked).toBeGreaterThanOrEqual(4);
  await expect(page.locator('#dataMsg')).toHaveText('');

  // Back, and the answer is the one this deployment always gives.
  away = false;
  const answered = page.waitForResponse((r) => r.url().endsWith('/api/v1/plan') && r.status() === 404, { timeout: 5_000 });
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await answered;

  // The accounts surface asked its own question into the same silence and
  // drew a sign-in door meanwhile. With no database there is nothing behind
  // one, so it goes again.
  await expect(page.locator('body')).toHaveClass(/accounts-none/);
  await expect(page.locator('#accountBar')).toBeHidden();

  // Nothing is booked after that, by either script. Moving the clock fires
  // whatever is, and the round trip through the page lets a request made that
  // way be counted.
  const settled = total;
  await page.clock.fastForward(60_000);
  await page.evaluate(() => 0);
  expect(total).toBe(settled);
  await expect(page.locator('#dataMsg')).toHaveText('');
});

test('the planner is stored under one known key, and nothing else', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await flushToStorage(page);

  // The key is a compatibility surface: change it and every saved planner is
  // silently orphaned, with no error anyone would see.
  //
  // Still an equality, and still exactly one key, because a theme nobody has
  // chosen is stored by storing nothing — see the test below, which is where
  // the second key is pinned down.
  const keys = await page.evaluate(() => Object.keys(localStorage));
  expect(keys).toEqual([STORAGE_KEY]);
});

/*
 * The theme is the one preference on this page that deliberately does not
 * travel. Everything else a person changes is a fact about the event and is
 * shared; this is a fact about the screen they are looking at, and one person
 * picking dark must not darken the ledger for everybody else.
 *
 * So it is not in the planner document, not in the export and not on the wire
 * — which leaves a second localStorage key, and leaves the assertion above
 * needing a companion rather than a loosening. The two together still pin the
 * whole storage surface: exactly one key until somebody overrides their
 * device, exactly two afterwards, and back to one when they stop.
 */
test('the theme is a per-device choice, kept out of the planner', async ({ page }) => {
  await openPlanner(page);
  const themed = () => page.evaluate(() => document.documentElement.getAttribute('data-theme'));
  const stored = () => page.evaluate(() => Object.keys(localStorage).sort());
  // Addressed by its accessible name rather than by position in the footer, so
  // this says which control it means and keeps meaning it when something else
  // lands beside it.
  const themes = page.getByRole('group', { name: 'Theme' }).getByRole('button');
  const pill = (i) => themes.nth(i);

  // Three states, not two: "follow the system" is the one most people want and
  // the one there is no way back to from a two-position switch.
  await expect(themes).toHaveCount(3);
  await expect(pill(0)).toHaveText('System');
  await expect(pill(2)).toHaveText('Dark');

  // Nothing chosen yet. The page follows the device, and says so by writing
  // no attribute and storing no key.
  expect(await themed()).toBeNull();
  expect(await page.evaluate(() => localStorage.getItem('soiree.theme'))).toBeNull();

  await pill(2).click();
  expect(await themed()).toBe('dark');

  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await flushToStorage(page);

  expect(await stored()).toEqual(['soiree.theme', STORAGE_KEY]);
  const planner = await readStored(page);
  expect(planner).not.toHaveProperty('theme');

  // It is remembered, and the control says which one is in force.
  await page.reload();
  expect(await themed()).toBe('dark');
  await expect(pill(2)).toHaveAttribute('aria-pressed', 'true');
  await expect(pill(0)).toHaveAttribute('aria-pressed', 'false');

  // Back to following the device clears the key rather than storing a third
  // value that then has to be kept in step with what "system" means.
  await pill(0).click();
  expect(await themed()).toBeNull();
  expect(await stored()).toEqual([STORAGE_KEY]);
});

/*
 * The half of a chosen theme that is not the page's to paint.
 *
 * Scrollbars, the date picker on a due date and the popup a native select
 * opens are drawn by the browser, from the colour scheme it thinks is in
 * force. Repainting the tokens does not tell it anything, so a person on a
 * light device who picks Dark used to get a near-white popup list carrying
 * the dark theme's near-white text. That disagreement is the case the
 * three-way control exists for, which is why the test forces it.
 */
test('a chosen theme is told to the browser too, not only to the page', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'light' });
  await openPlanner(page);
  const scheme = () => page.evaluate(
    () => window.getComputedStyle(document.documentElement).colorScheme,
  );
  const themes = page.getByRole('group', { name: 'Theme' }).getByRole('button');

  // The device says light throughout. Each pill has to win over it.
  await themes.nth(2).click();
  expect(await scheme()).toBe('dark');

  await themes.nth(1).click();
  expect(await scheme()).toBe('light');

  // Following the device means claiming neither, and leaving the meta in the
  // head to answer for both.
  await themes.nth(0).click();
  expect(await scheme()).toBe('normal');
});

test('a corrupt saved planner is repaired rather than fatal', async ({ page }) => {
  // What a hand-edited export, or an older version, might leave behind:
  // missing arrays, wrong types, a line tagged to a sponsor that no longer
  // exists. The page must still come up.
  //
  // Planted before the first navigation rather than written into a live page
  // and reloaded: the running page flushes its own state on pagehide, so a
  // reload would overwrite this with whatever that page was holding.
  await page.addInitScript(
    ([key, payload]) => {
      try {
        localStorage.setItem(key, payload);
      } catch (e) {
        /* not on the origin yet */
      }
    },
    [
      STORAGE_KEY,
      JSON.stringify({
        ceiling: 'not a number',
        budgetItems: [{ item: 'Venue deposit', unit: 2500, qty: 1, paid: 0, sponsors: ['ghost'] }],
        tasks: null,
        colWidths: [1, 2],
      }),
    ],
  );

  await page.goto('/');

  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
  await expect(page.locator('#budgetBody tr')).toHaveCount(1);
  await expectFigures(page, { committed: 2500, paid: 0, outstanding: 2500, forecast: 2500 });

  await gotoTab(page, 'budget');
  // The dangling sponsor reference was dropped, not rendered as a ghost tag.
  await expect(budgetRow(page, 0).by).toHaveText('Unassigned');
  await expect(page.locator('#ceilingInput')).toHaveValue('');

  await gotoTab(page, 'tasks');
  await expect(page.locator('#tasksBody .empty-cell')).toBeVisible();
});

/**
 * Waits until the origin's 404 has landed. The notices and the adoption below
 * are held until then, because until then this could be a deployment whose
 * plan lives on a server, and a page that has not made up its mind yet reads
 * as a failure of either.
 */
async function localOnly(page) {
  await page.waitForFunction(() => !!window.soiree && window.soiree.apiAvailable === false);
}

/*
 * A browser that refuses site data throws on every localStorage write. With no
 * database that write is the planner rather than a cache in front of one, so a
 * page carrying on as though it had saved is an evening's work lost to closing
 * a tab, with nothing said at any point.
 */
test('a browser that keeps nothing says so rather than looking saved', async ({ page }) => {
  await page.addInitScript(() => {
    const setItem = Storage.prototype.setItem;
    // Only the planner's key: the theme is stored separately, and a browser
    // that refused both would be testing two things at once.
    Storage.prototype.setItem = function (key, value) {
      if (key === 'soiree.v1') throw new DOMException('site data is blocked', 'SecurityError');
      return setItem.call(this, key, value);
    };
  });

  await openPlanner(page);
  await localOnly(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

  await expect(page.locator('#dataMsg')).toHaveText(
    'This browser is not keeping your changes. Export the planner before you close this tab.',
  );
  expect(await readStored(page)).toBeNull();
});

/*
 * Two tabs of one planner, in the deployment where the key is the planner.
 *
 * Each tab holds the whole state in memory and each save writes the whole of
 * it, so there is nothing in a write that says which parts of it are new. With
 * a database that does not matter — the server settles it row by row — but
 * here the last write is simply what is there now, and a tab that had not
 * heard about the other one put its own copy back over the work.
 *
 * An installed planner beside a browser tab is the ordinary way to arrive at
 * two of them.
 */
test('a planner saved in one tab is read by the other, not overwritten by it', async ({ page, context }) => {
  await openPlanner(page);
  // Opened before anything is typed, so this is the tab holding the older
  // copy: an empty planner, where the other is about to hold a line.
  const other = await context.newPage();
  await openPlanner(other);
  await localOnly(page);
  await localOnly(other);

  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expectStored(page, (s) => !!s && s.budgetItems.length === 1, 'the first tab saves its line');

  // The second tab reads what was saved rather than going on drawing a planner
  // that no longer exists.
  await expect(other.locator('body')).not.toHaveClass(/is-empty/);
  await expect(other.locator('#budgetBody tr')).toHaveCount(1);
  await gotoTab(other, 'budget');
  await expect(budgetRow(other, 0).item).toHaveValue('Venue deposit');

  // And leaving it does not put the empty planner back.
  await flushToStorage(other);
  const stored = await readStored(page);
  expect(stored.budgetItems.map((i) => i.item)).toEqual(['Venue deposit']);
});

test('a tab that has typed nothing writes nothing when it is left', async ({ page }) => {
  await openPlanner(page);
  await localOnly(page);

  // The key changes under a tab that never hears about it — one the browser
  // had frozen in the background, say. Written from this page on purpose: a
  // document is never told about its own write, so this is the blind spot
  // itself rather than a simulation of it.
  const elsewhere = {
    ceiling: 0,
    inflationPct: 0,
    fxRate: 0,
    splitEvenly: false,
    sponsors: [],
    budgetItems: [{ id: 'b1', item: 'Venue deposit', unit: 2500, qty: 1, paid: 500, sponsors: [], note: '' }],
    tasks: [],
    notes: [],
  };
  await page.evaluate(
    ([key, payload]) => localStorage.setItem(key, payload),
    [STORAGE_KEY, JSON.stringify(elsewhere)],
  );

  // Switching away from it, or closing it, is the moment the whole state went
  // back over whatever was there.
  await flushToStorage(page);
  const stored = await readStored(page);
  expect(stored.budgetItems.map((i) => i.item)).toEqual(['Venue deposit']);
});
