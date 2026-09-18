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

test('with no database behind it, the planner asks once and then stays off the network', async ({ page }) => {
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

  // Exactly one request, ever: "is there a database here". The answer was a
  // 404, which is final — those routes are not registered in this deployment
  // and asking again would only be a second 404. A planner that spent a round
  // trip per keystroke rediscovering that would be worse than one that never
  // asked.
  await expect
    .poll(() => apiRequests.length, { message: 'the API is probed exactly once at startup' })
    .toBe(1);
  expect(apiRequests[0]).toContain('/api/v1/plan');

  // More edits, and a flush, and still nothing goes out.
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await flushToStorage(page);
  expect(apiRequests).toHaveLength(1);

  // Nothing in the console that the application put there. The 404 itself is
  // logged by the browser's own network stack against the API URL — that is
  // Chromium reporting the probe, not soiree reporting a fault — so it is
  // named here rather than swept up in a blanket filter.
  expect(errors.filter((e) => e.url.indexOf('/api/v1/plan') === -1)).toEqual([]);
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
