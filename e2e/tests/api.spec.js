// @ts-check
/*
 * The planner as a shared thing.
 *
 * Every other spec in this suite runs against a server with no database, where
 * the planner lives in one browser and stays there. This one runs against the
 * instance that has PostgreSQL behind it, which is the deployment the project
 * exists for: several people, one ledger, and edits that reach each other.
 *
 * Two rules, inherited from the rest of the suite and worth restating because
 * a networked test is where they are easiest to break:
 *
 *  - Never wait on a clock. Every assertion below either acts (which
 *    Playwright waits for) or polls a condition — usually the plan itself,
 *    read back over the API, which is the only witness that says whether an
 *    edit left the browser.
 *  - Never assert on an exact Intl string when the point is the arithmetic.
 *
 * These tests share one database, so they run serially and wipe the plan
 * between them. If Docker is unavailable the launcher starts this instance
 * without a database; /api/v1/plan is then a 404 and every test here skips,
 * which is why the suite still runs on a machine that has no containers.
 *
 * Two things follow from the state being shared, and both are easy to undo by
 * accident:
 *
 *  - Everything that touches the API belongs in this one file. The rest of
 *    the suite runs files in parallel, and serial mode only orders the tests
 *    inside a file — a second API spec would run against this database at the
 *    same time as this one, and the failures would look like application bugs.
 *  - Repeating it needs one worker (`--repeat-each=3 --workers=1`). Without
 *    that, Playwright runs the copies of this group concurrently, which is the
 *    same collision by another route.
 */
const fs = require('fs/promises');
const { test, expect } = require('@playwright/test');
const {
  API_URL,
  STORAGE_KEY,
  addBudgetLine,
  addSponsor,
  addTask,
  apiPlan,
  budgetRow,
  expectFigures,
  gotoTab,
  openSharedPlanner,
  reloadSharedPlanner,
  resetPlan,
  tagLine,
} = require('./helpers');

test.use({ baseURL: API_URL });

// Genuinely shared state: one database, one event, no per-test isolation to be
// had. Serial, or these are tests that pass alone and fail together.
test.describe.configure({ mode: 'serial' });

test.beforeEach(async ({ request }) => {
  const res = await request.get(`${API_URL}/api/v1/plan`);
  test.skip(res.status() === 404, 'this instance has no database — Docker was not available');
  await resetPlan(request);
});

test('an edit reaches the server, and from there the next person', async ({ page, request, browser }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

  // No flush provoked: the debounced writer reaching the API on its own is the
  // claim, exactly as it is for localStorage in persistence.spec.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.unit, i.qty, i.paid]), {
      message: 'the debounced write should reach the API without anyone leaving the page',
    })
    .toEqual([['Venue deposit', '2500.00', 1, '500.00']]);

  /*
   * The whole point of the change. A browser that has never seen this planner
   * opens it and reads the same ledger — which, until the API was wired up,
   * was the one thing this application could not do.
   */
  const elsewhere = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block' });
  const other = await elsewhere.newPage();
  try {
    await openSharedPlanner(other);
    await expect(other.locator('body')).not.toHaveClass(/is-empty/);
    await expectFigures(other, { committed: 2500, paid: 500, outstanding: 2000, forecast: 2500 });

    await gotoTab(other, 'budget');
    await expect(budgetRow(other, 0).item).toHaveValue('Venue deposit');
    await expect(budgetRow(other, 0).paid).toHaveValue('500');
  } finally {
    await elsewhere.close();
  }
});

test('money crosses the wire as a decimal string in major units', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  // Two traps in one line, both invisible against a zero-decimal currency and
  // both fatal against this one. String(45.33 * 1) is "45.330000000000005",
  // which the API refuses outright; 45.33 x 40 is 1813.1999999999998, which it
  // would accept and store a cent short of the truth.
  await addBudgetLine(page, { item: 'Catering', unit: 45.33, qty: 40, paid: 0.07 });

  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.unit, i.qty, i.paid]))
    .toEqual([['45.33', 40, '0.07']]);

  const plan = await apiPlan(request);
  // A string, not a number: every browser parses a JSON number as an IEEE-754
  // double, and most major-unit amounts have no exact binary form.
  expect(typeof plan.budgetItems[0].unit).toBe('string');
  expect(typeof plan.budgetItems[0].paid).toBe('string');

  // The ceiling is money too, and goes through the same rule.
  await page.locator('#ceilingInput').fill('10000');
  await expect.poll(async () => (await apiPlan(request)).settings.ceiling).toBe('10000.00');
});

test('a row created here and the row the server made are one row', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  /*
   * The browser needs an id the moment a row appears, long before any round
   * trip could answer; the server issues a uuid on POST. All of this happens
   * inside one debounce window, so the line is written carrying a sponsor id
   * that only became real a moment earlier — and if the two ids were not
   * reconciled the attribution would be a foreign key violation, or the line
   * would be created twice.
   */
  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  const line = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });
  await tagLine(line, ['Rose']);

  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      if (plan.sponsors.length !== 1 || plan.budgetItems.length !== 1) {
        return { sponsors: plan.sponsors.length, items: plan.budgetItems.length, tagged: false };
      }
      return {
        sponsors: 1,
        items: 1,
        tagged: plan.budgetItems[0].sponsors.join() === plan.sponsors[0].id,
      };
    })
    .toEqual({ sponsors: 1, items: 1, tagged: true });

  // And it stays one row. A browser still using its own id would create a
  // second copy on the next write rather than updating the first.
  await budgetRow(page, 0).unit.fill('2100');
  await expect.poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.unit)).toEqual(['2100.00']);

  await reloadSharedPlanner(page);
  await gotoTab(page, 'budget');
  await expect(page.locator('#budgetBody tr')).toHaveCount(1);
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(1);
  await expect(budgetRow(page, 0).by).toHaveText('Rose');
});

test('rows come back in the order they were typed', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });
  await addBudgetLine(page, { item: 'Catering', unit: 50, qty: 40 });
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1 });

  // A POST does not allocate a position and an omitted one is 0, which would
  // put every new line at the top — so the ledger would read backwards to the
  // next person to open it.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item))
    .toEqual(['Venue deposit', 'Catering', 'Flowers']);

  await reloadSharedPlanner(page);
  await gotoTab(page, 'budget');
  for (const [i, item] of ['Venue deposit', 'Catering', 'Flowers'].entries()) {
    await expect(budgetRow(page, i).item).toHaveValue(item);
  }
});

test("someone else's edit to the same line is merged, not overwritten", async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  const row = await addBudgetLine(page, {
    item: 'Venue deposit',
    unit: 2000,
    qty: 1,
    note: 'Balance due one month before',
  });

  let stored;
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      stored = plan.budgetItems[0];
      return !!stored && stored.note === 'Balance due one month before';
    })
    .toBe(true);

  // Somebody else, editing a different column of the same line. This browser's
  // revision is now one behind, which is what the next write will discover.
  const theirs = await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    data: { revision: stored.revision, note: 'Deposit already wired' },
  });
  expect(theirs.status()).toBe(200);

  await row.unit.fill('2500');

  // Neither edit is discarded: the unit is this person's, the note is theirs.
  // A client that took the server's row wholesale would lose the unit; one
  // that resent its whole row would lose the note.
  await expect
    .poll(async () => {
      const item = (await apiPlan(request)).budgetItems[0];
      return [item.unit, item.note];
    })
    .toEqual(['2500.00', 'Deposit already wired']);

  await expect(page.locator('#dataMsg')).toHaveText(/Both sets of changes have been kept/);

  // The merged note reaches the screen once the caret is out of the table.
  // Rebuilding it while somebody is mid-word would take their cursor with it,
  // so the rebuild waits rather than interrupting.
  await page.locator('#ceilingInput').click();
  await expect(budgetRow(page, 0).note).toHaveValue('Deposit already wired');
  await expect(budgetRow(page, 0).unit).toHaveValue('2500');
});

test('removing a line removes it for everyone', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(2);

  await budgetRow(page, 1).remove.click();

  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item))
    .toEqual(['Venue deposit']);
});

test('the plan-wide settings are shared too', async ({ page, request, browser }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('10000');
  await page.locator('#inflationInput').fill('4');
  await page.locator('#splitEvenly').check();

  await expect
    .poll(async () => {
      const s = (await apiPlan(request)).settings;
      return [s.ceiling, s.inflationPct, s.splitEvenly];
    })
    .toEqual(['10000.00', 4, true]);

  const elsewhere = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block' });
  const other = await elsewhere.newPage();
  try {
    await openSharedPlanner(other);
    await gotoTab(other, 'budget');
    await expect(other.locator('#ceilingInput')).toHaveValue('10000');
    await expect(other.locator('#inflationInput')).toHaveValue('4');
    await expect(other.locator('#splitEvenly')).toBeChecked();
  } finally {
    await elsewhere.close();
  }
});

test('a write that cannot get out is retried and said out loud, not dropped', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  // The origin goes away mid-session. Nothing about the interaction changes:
  // edits apply locally and paint immediately, which is the whole reason the
  // write is deferred in the first place.
  await page.route('**/api/v1/**', (route) => route.abort('internetdisconnected'));
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });
  await expect(budgetRow(page, 0).committed).toHaveText('€2,500');

  // One failure is a blip. Two in a row means the edit exists in one browser
  // only, which is worth saying — in the status line a person can see, not in
  // a console nobody has open.
  await expect(page.locator('#dataMsg')).toHaveText(/not reaching the server/, { timeout: 15_000 });
  expect((await apiPlan(request)).budgetItems).toHaveLength(0);

  // Back in touch. Nothing was queued and nothing was lost: the write is the
  // difference between the page and the last version the server confirmed, and
  // that difference is still there to be sent.
  await page.unroute('**/api/v1/**');
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item), { timeout: 20_000 })
    .toEqual(['Venue deposit']);
  await expect(page.locator('#dataMsg')).toHaveText(/Back in touch with the server/);
});

test('the shared planner is still cached under one known key, and nothing else', async ({ page }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));

  // The server is the planner here; localStorage is the copy that paints
  // before the plan arrives. Same single key either way — change it and a
  // browser's cached copy is orphaned with no error anyone would see.
  expect(await page.evaluate(() => Object.keys(localStorage))).toEqual(['soiree.v1']);
});

test('export and import still work, and an import replaces the shared planner', async ({ page, request }, testInfo) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Something to be replaced', unit: 999, qty: 1 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  // Export is the same document in both modes: it is written from the page's
  // own state, which the API fills rather than replaces.
  const downloadPromise = page.waitForEvent('download');
  await page.locator('#exportData').click();
  const download = await downloadPromise;
  const exported = testInfo.outputPath('shared-export.json');
  await download.saveAs(exported);
  const data = JSON.parse(await fs.readFile(exported, 'utf8'));
  expect(data.budgetItems.map((i) => [i.item, i.unit])).toEqual([['Something to be replaced', 999]]);

  // A planner from somewhere else, whose ids mean nothing here.
  const incoming = testInfo.outputPath('incoming.json');
  await fs.writeFile(
    incoming,
    JSON.stringify({
      ceiling: 5000,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      sponsors: [{ id: 'local-1', code: 'Rose', name: 'Ada' }],
      budgetItems: [{ id: 'local-2', item: 'Venue deposit', unit: 2000, qty: 1, paid: 0, sponsors: ['local-1'], note: '' }],
      tasks: [{ id: 'local-3', name: 'Confirm final guest count', owner: 'Ada', due: '', status: 'not-started' }],
      notes: [],
    }),
  );

  page.on('dialog', (d) => d.accept().catch(() => {}));
  const chooser = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooser).setFiles(incoming);
  await expect(page.locator('#dataMsg')).toHaveText(/^Imported /);

  // Replacing the planner replaces it for everyone: the old line is gone from
  // the server, the new ones are there under ids the server issued, and the
  // attribution still resolves.
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      return {
        items: plan.budgetItems.map((i) => i.item),
        sponsors: plan.sponsors.map((s) => s.code),
        tasks: plan.tasks.map((t) => t.name),
        ceiling: plan.settings.ceiling,
      };
    })
    .toEqual({
      items: ['Venue deposit'],
      sponsors: ['Rose'],
      tasks: ['Confirm final guest count'],
      ceiling: '5000.00',
    });

  const plan = await apiPlan(request);
  expect(plan.budgetItems[0].sponsors).toEqual([plan.sponsors[0].id]);
  // The ids in the file were this browser's, not the server's; they are not
  // what came back.
  expect(plan.budgetItems[0].id).not.toBe('local-2');
});

test('a task with no due date is written without one', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'tasks');
  // The field is empty until somebody fills it, and "" is not a date: sent as
  // one it is a 400 and the task never leaves the browser.
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada' });

  await expect
    .poll(async () => (await apiPlan(request)).tasks.map((t) => [t.name, t.owner, t.due, t.status]))
    .toEqual([['Confirm final guest count', 'Ada', null, 'not-started']]);

  await page.locator('#tasksBody tr').first().locator('td').nth(2).locator('input').fill('2030-01-15');
  await expect.poll(async () => (await apiPlan(request)).tasks[0].due).toBe('2030-01-15');
});

test('a planner built before the database existed is carried up, not wiped', async ({ page, request }) => {
  /*
   * The upgrade path: somebody ran this without PostgreSQL, built a real
   * planner in their browser, and then a database appeared. On that first load
   * the server's plan is empty, and taking it would replace the only copy of
   * their work with nothing — silently, and permanently as soon as the tab
   * closes and the cache is overwritten.
   *
   * Narrow on purpose. It applies only to a planner that was actually saved,
   * and only against a plan with nothing at all in it; anything else and the
   * server is simply right. The risk it accepts is two browsers seeding the
   * same empty database and producing every row twice, which somebody can see
   * and fix. The risk it removes cannot be seen or fixed.
   */
  await page.addInitScript(
    ([key, payload]) => {
      try {
        if (!localStorage.getItem(key)) localStorage.setItem(key, payload);
      } catch (e) {
        /* not on the origin yet */
      }
    },
    [
      STORAGE_KEY,
      JSON.stringify({
        ceiling: 10000,
        inflationPct: 4,
        fxRate: 0,
        splitEvenly: false,
        sponsors: [{ id: 'local-s1', code: 'Rose', name: 'Ada' }],
        budgetItems: [
          { id: 'local-b1', item: 'Venue deposit', unit: 2500, qty: 1, paid: 500, sponsors: ['local-s1'], note: '' },
        ],
        tasks: [{ id: 'local-t1', name: 'Confirm final guest count', owner: 'Ada', due: '', status: 'in-progress' }],
        notes: [{ id: 'local-n1', text: 'Venue balance is due a month out.' }],
      }),
    ],
  );

  await openSharedPlanner(page);

  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      return {
        items: plan.budgetItems.map((i) => [i.item, i.unit, i.paid]),
        sponsors: plan.sponsors.map((s) => s.code),
        tasks: plan.tasks.map((t) => t.name),
        notes: plan.notes.map((n) => n.text),
        ceiling: plan.settings.ceiling,
        inflationPct: plan.settings.inflationPct,
      };
    })
    .toEqual({
      items: [['Venue deposit', '2500.00', '500.00']],
      sponsors: ['Rose'],
      tasks: ['Confirm final guest count'],
      notes: ['Venue balance is due a month out.'],
      ceiling: '10000.00',
      inflationPct: 4,
    });

  // Attribution survived the change of identity: the line points at the
  // sponsor under the id the server issued, not the one this browser invented.
  const plan = await apiPlan(request);
  expect(plan.budgetItems[0].sponsors).toEqual([plan.sponsors[0].id]);

  // And nothing was carried up twice.
  await expect(page.locator('#budgetBody tr')).toHaveCount(1);
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);
});
