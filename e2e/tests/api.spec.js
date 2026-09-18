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
  apiAuth,
  adoptSession,
  awaitPlan,
  ensureEditor,
  freshSession,
} = require('./helpers');

test.use({ baseURL: API_URL });

// Genuinely shared state: one database, one event, no per-test isolation to be
// had. Serial, or these are tests that pass alone and fail together.
test.describe.configure({ mode: 'serial' });

test.beforeEach(async ({ request }) => {
  // 404 is the no-database answer and the reason to skip. A 401 is the
  // opposite: the API is there and wants a session, which resetPlan gets.
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

/*
 * The one above proves a second browser reads the same ledger when it opens.
 * This one is the part that was still missing: neither of them had any way to
 * learn about the other until somebody reloaded, and two people editing the
 * same budget without seeing each other is how a venue gets booked twice.
 *
 * Nothing here reloads, and nothing here waits on a clock. Every assertion is
 * an ordinary Playwright poll on what the *other* browser is showing, which is
 * the only honest witness that the change crossed on its own.
 *
 * One deliberate detail: before asserting that the first browser has caught up,
 * the caret is moved out of the budget table. A rebuild of a table somebody is
 * typing in would take their cursor with it, so it is held back until they are
 * out — which means a test that leaves the caret inside the container it then
 * asserts on would be testing that policy rather than the live stream.
 */
test('an edit crosses to the other browser with nobody reloading', async ({ page, browser }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  const elsewhere = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block' });
  const other = await elsewhere.newPage();
  try {
    await openSharedPlanner(other);
    await gotoTab(other, 'budget');
    await expect(other.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(0);

    // One person books the venue.
    await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

    // The other sees it appear. Not a figure they had to ask for, not after a
    // reload: the row, the money on it, and the headline totals it moves.
    await expect(budgetRow(other, 0).item).toHaveValue('Venue deposit', { timeout: 15_000 });
    await expect(budgetRow(other, 0).paid).toHaveValue('500');
    await expectFigures(other, { committed: 2500, paid: 500, outstanding: 2000, forecast: 2500 });

    // And it travels the other way, on a row that already exists. The caret
    // goes somewhere harmless first, for the reason in the comment above.
    await page.locator('#ceilingInput').click();
    await budgetRow(other, 0).unit.fill('2600');
    await expect(budgetRow(page, 0).unit).toHaveValue('2600', { timeout: 15_000 });
    await expectFigures(page, { committed: 2600, paid: 500, outstanding: 2100, forecast: 2600 });

    // Including the removal. A line that is gone has to *go*: a delete
    // announces the revision the row already had, so a client that compared
    // revisions the way it does for an edit would throw this one away and keep
    // showing a cost nobody is paying.
    await other.locator('#ceilingInput').click();
    await budgetRow(page, 0).remove.click();
    await expect(other.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(0, { timeout: 15_000 });
    await expectFigures(other, { committed: 0, paid: 0, outstanding: 0, forecast: 0 });
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
    headers: await apiAuth(request),
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

/*
 * A session that ends while the page is open.
 *
 * Seven idle days, an account disabled by an admin, a sign-out in another tab:
 * the page is mid-use and the next request it makes is refused. Three things
 * used to go wrong at once. The refused write was parked like any other 4xx —
 * and a parked row stays parked until it is edited again, so signing back in
 * did not send it. Nothing told the person, because nothing told auth.js. And
 * the event stream went on asking into the same 401 every thirty seconds for
 * as long as the tab stayed open, which a proxy that counts refusals per
 * address reads as an attack.
 *
 * These two bring their own session and end that one. The run shares a single
 * login, replayed into every context; signing THAT out would leave every spec
 * after this anonymous, and an anonymous request is answered 401 whether or
 * not the thing it asked for works.
 */
async function openWithOwnSession(page, request) {
  const who = await ensureEditor(request);
  const cookie = await freshSession(request, API_URL, who);
  await adoptSession(page, cookie);
  await awaitPlan(page, () => page.goto('/'));
  return cookie;
}

/**
 * Makes an edit whose write meets the end of the session.
 *
 * The order matters and cannot be left to timing. Ending the session first and
 * editing second looks like the scenario, and is a race: the page has its own
 * reasons to ask the server something — a resync after its last write is the
 * usual one — and if that request is the one that finds the session gone, the
 * sign-in screen is up before the test reaches for the input underneath it.
 * That is the page being right and the test being wrong, about one run in
 * three with two workers.
 *
 * So the edit is made while still signed in, its PATCH is held in flight, the
 * session is ended, and only then is the PATCH let through. The edit always
 * exists, and its write can never have succeeded.
 */
async function editAsTheSessionEnds(page, request, cookie, paid) {
  const held = '**/api/v1/budget-items/**';
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route(held, async (route) => { await gate; await route.continue(); });

  const sent = page.waitForRequest((r) => r.method() === 'PATCH' && r.url().includes('/api/v1/budget-items/'));
  const refused = page.waitForResponse((r) => r.request().method() === 'PATCH' && r.status() === 401);
  await budgetRow(page, 0).paid.fill(String(paid));
  await budgetRow(page, 0).paid.blur();
  await sent;

  await endSession(request, cookie);
  release();
  await refused;
  await page.unroute(held);
}

async function signInThroughTheForm(page, request) {
  const who = await ensureEditor(request);
  await page.fill('#loginEmail', who.email);
  await page.fill('#loginPassword', who.password);
  await page.click('#loginSubmit');
  await expect(page.locator('#authScreen')).toBeHidden();
}

async function endSession(request, cookie) {
  const res = await request.post(`${API_URL}/api/v1/auth/logout`, { headers: { Cookie: cookie } });
  expect(res.status(), 'ending the session server-side').toBe(204);
}

test('a session that ends mid-edit asks for a sign-in, and the edit goes up afterwards', async ({ page, request }) => {
  const cookie = await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.paid))
    .toEqual(['500.00']);

  // Nothing about the edit is wrong; only who is asking.
  await editAsTheSessionEnds(page, request, cookie, 750);

  await expect(page.locator('#authScreen'), 'the sign-in screen should open by itself').toBeVisible();
  await expect(page.locator('#authLede')).toContainText('Your session has ended');
  await expect(page.locator('body')).toHaveClass(/signed-out/);

  // Refused, so not on the server — and that is the state the old behaviour
  // left it in for good.
  expect((await apiPlan(request)).budgetItems.map((i) => i.paid)).toEqual(['500.00']);

  await signInThroughTheForm(page, request);

  // The claim. Nobody touches the row again: signing in is what sends it.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.paid), {
      message: 'the edit made while signed out should go up on sign-in, without being edited again',
    })
    .toEqual(['750.00']);
  await expect(budgetRow(page, 0).paid).toHaveValue('750');
});

test('signing back in merges, and does not write a stale copy over everyone else', async ({ page, request }) => {
  const cookie = await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 2, paid: 0 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item).sort())
    .toEqual(['Flowers', 'Venue deposit']);

  await editAsTheSessionEnds(page, request, cookie, 750);
  await expect(page.locator('#authScreen')).toBeVisible();

  // While this browser is signed out, somebody else carries on: they change a
  // field this browser never touched, and delete a row it is still showing.
  const headers = await apiAuth(request);
  const before = await apiPlan(request);
  const venue = before.budgetItems.find((i) => i.item === 'Venue deposit');
  const flowers = before.budgetItems.find((i) => i.item === 'Flowers');
  const patched = await request.patch(`${API_URL}/api/v1/budget-items/${venue.id}`, {
    headers, data: { revision: venue.revision, vendor: 'The Orangery' },
  });
  expect(patched.status()).toBe(200);
  const removed = await request.delete(
    `${API_URL}/api/v1/budget-items/${flowers.id}?revision=${flowers.revision}`, { headers });
  expect(removed.status()).toBe(204);

  await signInThroughTheForm(page, request);

  // Ours where we edited, theirs where we did not, and their delete stands.
  // A browser that let its own copy win would put `vendor` back to empty —
  // at the current revision, so with no conflict raised — and POST the
  // flowers back into a budget somebody had just taken them out of.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.paid, i.vendor]), {
      message: 'sign-in should merge against the plan as it now stands',
    })
    .toEqual([['Venue deposit', '750.00', 'The Orangery']]);
});

test('once the session has ended the page stops asking', async ({ page, request }) => {
  const cookie = await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  await editAsTheSessionEnds(page, request, cookie, 750);
  await expect(page.locator('#authScreen')).toBeVisible();

  /*
   * The one place in this suite that waits on a clock, because the claim is
   * that something does NOT happen and there is no event for that. The window
   * is longer than the longest first retry the page used to make — the stream
   * reopened after 2.5 to 7.5 seconds, the write loop after one — so the old
   * behaviour cannot fit a quiet spell inside it.
   */
  const asked = [];
  const note = (r) => { if (r.url().includes('/api/v1/')) asked.push(`${r.method()} ${new URL(r.url()).pathname}`); };
  page.on('request', note);
  await page.waitForTimeout(9000);
  page.off('request', note);

  expect(asked, 'a signed-out page should not keep asking the API').toEqual([]);
});

/*
 * The same ending, met the ordinary way: not with the page open, but with the
 * tab closed for a week. The page comes back, paints the copy of the plan this
 * browser kept — that is what the cache is for — and is signed out, and
 * nothing stops somebody editing what they see before they notice. Then they
 * sign in.
 *
 * There is no shadow to merge against this time; it lived in the page that was
 * closed. What there is instead is the copy as it was loaded, before anybody
 * touched it, and that does the same job: a field that differs from it was
 * edited here, and everything else is the server's.
 */
test('a reopened tab that was edited while signed out does not overwrite a week of other people', async ({ page, request }) => {
  const cookie = await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 2, paid: 0 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item).sort())
    .toEqual(['Flowers', 'Venue deposit']);

  // The tab is closed. Literally: a page left open would hear about the edits
  // below over its event stream, find its session gone and take itself to the
  // sign-in screen — which is the other spec, and whether it got there before
  // the reload would be a race. Leaving the origin is what closing a tab is;
  // the cookie jar and localStorage stay, as they would.
  await page.goto('about:blank');

  // The week goes by: the session ends, and other people carry on.
  await endSession(request, cookie);
  const headers = await apiAuth(request);
  const before = await apiPlan(request);
  const venue = before.budgetItems.find((i) => i.item === 'Venue deposit');
  const flowers = before.budgetItems.find((i) => i.item === 'Flowers');
  // `unit` is a field the page draws and `vendor` is one it does not, and a
  // stale copy damages them differently: the first is PATCHed back to what it
  // was a week ago, the second is left alone. Both have to survive.
  expect((await request.patch(`${API_URL}/api/v1/budget-items/${venue.id}`, {
    headers, data: { revision: venue.revision, vendor: 'The Orangery', unit: '2600.00' },
  })).status()).toBe(200);
  expect((await request.delete(
    `${API_URL}/api/v1/budget-items/${flowers.id}?revision=${flowers.revision}`, { headers })).status()).toBe(204);

  // The tab is reopened. Signed out, and showing the copy it kept.
  await page.goto('/');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).item).toHaveValue('Venue deposit');
  await expect(page.locator('body')).toHaveClass(/signed-out/);

  await budgetRow(page, 0).paid.fill('750');
  await budgetRow(page, 0).paid.blur();

  await page.goto('/#/login');
  await signInThroughTheForm(page, request);

  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.paid, i.vendor, i.unit]), {
      message: 'only what was edited here should go up; the rest is a week out of date',
    })
    .toEqual([['Venue deposit', '750.00', 'The Orangery', '2600.00']]);
  await expect(budgetRow(page, 0).unit).toHaveValue('2600');
});

/*
 * Signing out takes this browser's copy of the plan with it.
 *
 * That copy is what lets the page paint at once and work offline, and it is
 * also a ledger of names against money in localStorage on whatever computer
 * somebody used. An ended session leaves it alone — the specs above depend on
 * that, because the unsent edit is in it. A sign-out somebody asked for does
 * not.
 */
const signOutButton = (page) => page.locator('#accountActs button', { hasText: 'Sign out' });

test('signing out removes this browser\'s copy of the plan, and signing in brings it back from the server', async ({ page, request }) => {
  await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toContain('Venue deposit');

  await signOutButton(page).click();
  await expect(page.locator('#authScreen')).toBeVisible();
  await expect(page.locator('body')).toHaveClass(/signed-out/);

  // Gone from storage, and gone from the page behind the sign-in screen.
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toBeNull();
  // Closing the tab must not put it back: pagehide is when the planner saves.
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toBeNull();

  await page.goto('/');
  await expect(page.locator('body')).toHaveClass(/signed-out/);
  await expect(page.locator('body')).toHaveClass(/is-empty/);
  expect(await page.content()).not.toContain('Venue deposit');

  // Nothing was lost: it was only ever a copy.
  await page.goto('/#/login');
  await signInThroughTheForm(page, request);
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).item).toHaveValue('Venue deposit');
  await expect(budgetRow(page, 0).paid).toHaveValue('500');
});

test('signing out with changes that never reached the server asks first', async ({ page, request }) => {
  await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  // The origin stops answering writes, as it does on a train.
  await page.route('**/api/v1/budget-items/**', (route) => route.abort());
  await budgetRow(page, 0).paid.fill('750');
  await budgetRow(page, 0).paid.blur();

  // Declined: still signed in, and the edit is still here.
  let asked = '';
  page.once('dialog', (d) => { asked = d.message(); d.dismiss(); });
  await signOutButton(page).click();
  await expect.poll(() => asked).toContain('have not reached the server');
  await expect(page.locator('body')).toHaveClass(/signed-in/);
  await expect(budgetRow(page, 0).paid).toHaveValue('750');
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toContain('750');

  // Accepted: it is their planner and their decision.
  page.once('dialog', (d) => d.accept());
  await signOutButton(page).click();
  await expect(page.locator('#authScreen')).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toBeNull();
  expect((await apiPlan(request)).budgetItems.map((i) => i.paid)).toEqual(['500.00']);
});
