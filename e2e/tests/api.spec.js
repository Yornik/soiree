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
 *
 * And one thing follows from the page listening. With the event stream open a
 * browser reads the plan again by itself: 400 ms after it hears of a change,
 * and every 400 ms after that until it is idle. A spec that stages a conflict
 * is racing that read, and both results are correct. Either this browser's
 * write goes first and meets a 409, which it merges and mentions, or the read
 * goes first and there is no conflict left to meet. A spec that means one of
 * them has to arrange it, because asserting the 409 and leaving the order to
 * luck failed one run in five on a slow machine:
 *
 *  - for the 409, take the stream away before the page loads (delete
 *    window.EventSource in an init script), so nothing can arm the read;
 *  - for the silent merge, wait until their edit is on this screen.
 *
 * The two merge specs below are the pattern.
 */
const fs = require('fs/promises');
const { test, expect, chromium } = require('@playwright/test');
const {
  ADMIN_EMAIL,
  API_URL,
  STORAGE_KEY,
  addBudgetLine,
  addSponsor,
  addTask,
  apiPlan,
  budgetRow,
  expectFigures,
  flushToStorage,
  gotoTab,
  readStored,
  openSharedPlanner,
  reloadSharedPlanner,
  resetPlan,
  tagLine,
  signInPage,
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
  // No event stream, so the 409 is the only way this page can learn of their
  // edit. With it on, the re-read described at the top of this file lands
  // between their write and the keystroke below about one run in five, the
  // conflict is settled before it is met, and nothing is said. app.js treats
  // a browser without EventSource as one with no live sync and arms no timer.
  await page.addInitScript(() => { delete window.EventSource; });

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

/*
 * The other order, which is every bit as correct and looks nothing like it.
 * With the stream on, this browser normally hears of their edit before anybody
 * here has typed: it reads the plan again and takes their note, and the write
 * that follows goes up at the revision that now stands. No 409, no merge, and
 * no message, because nobody's work was ever in question.
 *
 * What puts the two edits in that order is the screen. A GET /plan seen from
 * out here has only arrived; the page may yet throw it away, which it does if
 * a keystroke lands first, and then this is the 409 path after all. Their note
 * in the row says the read has been merged, and nothing short of it does.
 */
test("someone else's edit that arrives before ours is taken in without a word", async ({ page, request }) => {
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

  // Out of the table, or the rebuild that shows their note waits for the
  // caret to leave and there is nothing on the screen to wait for.
  await page.locator('#ceilingInput').click();

  const theirs = await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    headers: await apiAuth(request),
    data: { revision: stored.revision, note: 'Deposit already wired' },
  });
  expect(theirs.status()).toBe(200);
  await expect(budgetRow(page, 0).note).toHaveValue('Deposit already wired', { timeout: 15_000 });

  // Registered before the keystroke, so it is this edit's write and not one
  // that happened to be passing.
  const written = page.waitForResponse(
    (r) => r.request().method() === 'PATCH' && r.url().includes(`/api/v1/budget-items/${stored.id}`),
  );
  await row.unit.fill('2500');
  expect((await written).status(), 'the write should go up at their revision and meet no conflict').toBe(200);

  await expect
    .poll(async () => {
      const item = (await apiPlan(request)).budgetItems[0];
      return [item.unit, item.note];
    })
    .toEqual(['2500.00', 'Deposit already wired']);

  // Both moments a message could have come from are behind us: the merge is
  // on the screen and the write has its answer. Asserting the silence any
  // earlier would be asserting nothing.
  await expect(page.locator('#dataMsg')).not.toHaveText(/Both sets of changes have been kept/);
  await expect(budgetRow(page, 0).note).toHaveValue('Deposit already wired');
  await expect(budgetRow(page, 0).unit).toHaveValue('2500');
});

/*
 * The page, left alone with the conflict it is about to meet.
 *
 * Taking the event stream away is not enough on its own. Signing in arms a
 * re-read of the plan too, and that read holds off while a write of this
 * browser's is pending, which is exactly the moment a spec stages a conflict:
 * it then lands between their edit and this one, settles the conflict
 * silently, and there is no 409 left to meet. So the plan fetch the page
 * opens with goes through and nothing after it does.
 */
async function noReReads(page) {
  await page.addInitScript(() => { delete window.EventSource; });
  let opened = false;
  await page.route('**/api/v1/plan', (route) => {
    if (!opened && route.request().method() === 'GET') {
      opened = true;
      return route.continue();
    }
    return route.abort('internetdisconnected');
  });
}

/*
 * The same moment as the spec above, on the column both people touched.
 *
 * A merge that is per field has one case it cannot settle by leaving each side
 * alone: both sides changed the same one. Who pays for a shared line is not a
 * value but a set, kept in a join table, and the whole list travelling as one
 * string is an artefact of how it is compared rather than a decision. Two
 * people each ticking a name has an answer that keeps both.
 */
test('two people tagging the same line at once keep both names', async ({ page, request }) => {
  await noReReads(page);

  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  await addSponsor(page, { code: 'Iris', name: 'Bram' });
  const line = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  // Everything typed so far is on the server, so the revision read here is the
  // one the next write will carry and nothing of this browser's is pending.
  let stored;
  let iris;
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      stored = plan.budgetItems[0];
      iris = (plan.sponsors || []).find((s) => s.code === 'Iris');
      return plan.sponsors.length === 2 && !!stored && stored.unit === '2000.00';
    })
    .toBe(true);

  // Somebody else puts one name on the line. This browser is now a revision
  // behind, on the very column it is about to tick.
  const theirs = await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    headers: await apiAuth(request),
    data: { revision: stored.revision, sponsors: [iris.id] },
  });
  expect(theirs.status()).toBe(200);

  await tagLine(line, ['Rose']);

  // Both names, because neither person untagged the other's. Taking one side
  // whole would drop a payer from a shared cost and say nothing.
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      const byId = new Map(plan.sponsors.map((s) => [s.id, s.code]));
      return plan.budgetItems[0].sponsors.map((id) => byId.get(id)).sort();
    })
    .toEqual(['Iris', 'Rose']);

  await expect(page.locator('#dataMsg')).toHaveText(/Both sets of changes have been kept/);
});

/*
 * And the case that has no such answer. Two people typed into one text column,
 * so one of the two values is gone. This browser's wins, which is the rule
 * everywhere else and the one the README describes. What must not happen is
 * the page claiming both were kept.
 */
test('a column two people changed at once says that yours replaced theirs', async ({ page, request }) => {
  await noReReads(page);

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

  const theirs = await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    headers: await apiAuth(request),
    data: { revision: stored.revision, note: 'Deposit already wired' },
  });
  expect(theirs.status()).toBe(200);

  await row.note.fill('Ask about the deposit');

  await expect.poll(async () => (await apiPlan(request)).budgetItems[0].note).toBe('Ask about the deposit');
  await expect(page.locator('#dataMsg')).toHaveText(/Yours replaced theirs/);
  await expect(page.locator('#dataMsg')).not.toHaveText(/Both sets of changes have been kept/);
});

/*
 * The three answers a write can meet that are not a conflict, one spec each.
 * None of them had a test at any level, and two of them decide whether
 * somebody's typing survives.
 *
 * First: the line this person removed had just been changed by somebody else.
 * The removal is still what they asked for, so it goes again at the revision
 * that now stands, and they are told, because the edit it overtook was not
 * theirs.
 */
test('removing a line somebody had just changed still removes it, and says so', async ({ page, request }) => {
  await noReReads(page);

  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  let stored;
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      stored = plan.budgetItems[0];
      return !!stored && stored.unit === '2000.00';
    })
    .toBe(true);

  const theirs = await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    headers: await apiAuth(request),
    data: { revision: stored.revision, note: 'Deposit already wired' },
  });
  expect(theirs.status()).toBe(200);

  await budgetRow(page, 0).remove.click();

  await expect(page.locator('#dataMsg')).toHaveText(/It is gone/);
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(0);
});

/*
 * Second: the server understood the write and said no. The row is parked,
 * because sending the identical body again would only earn the identical
 * refusal, and the edit stays on the screen under a line saying it has not
 * left the browser.
 *
 * A refusal has to be staged rather than provoked: every 4xx this API really
 * answers comes from a body the page has no way to type. What is under test is
 * what the page does with one, not which one it was.
 */
test('a change the server will not take is parked, said out loud, and not counted once the line goes', async ({ page, request }) => {
  await noReReads(page);

  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  let refuse = true;
  await page.route('**/api/v1/budget-items/*', async (route) => {
    if (refuse && route.request().method() === 'PATCH') {
      refuse = false;
      await route.fulfill({ status: 422, contentType: 'application/json', body: '{"error":"refused"}' });
      return;
    }
    await route.continue();
  });

  await budgetRow(page, 0).note.fill('Balance due one month before');
  await expect(page.locator('#dataMsg')).toHaveText(/would not accept/);
  await expect(budgetRow(page, 0).note).toHaveValue('Balance due one month before');

  // And then the line goes. The parked write can never be made again, so the
  // question at the door of a shared computer must not go on counting it: it
  // would be asking about a change that exists nowhere.
  await budgetRow(page, 0).remove.click();
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(0);
  expect(await page.evaluate(() => window.soiree.beforeSignOut())).toBe(0);
});

/*
 * Third, and the one where two rules contradicted each other. A PATCH that
 * meets a 404 means somebody removed the line this person is editing. The page
 * said their copy was still here, and the re-read that the same removal
 * announces dropped the row a few hundred milliseconds later: a promise with a
 * life of half a second. The re-read is the rule, because there is no row left
 * to write the edit to and putting the line back would undo a removal somebody
 * meant, so the message is what had to change.
 *
 * The order is arranged rather than raced. The page will not re-read the plan
 * while a write of its own is in flight, so holding the PATCH at the door is
 * what puts the removal before the 404 every time.
 */
test('the line somebody else removed while you were editing it goes from your copy too', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  let stored;
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      stored = plan.budgetItems[0];
      return !!stored && stored.unit === '2000.00';
    })
    .toBe(true);

  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  let held = false;
  await page.route(`**/api/v1/budget-items/${stored.id}`, async (route) => {
    if (route.request().method() === 'PATCH' && !held) {
      held = true;
      await gate;
    }
    await route.continue();
  });

  await budgetRow(page, 0).note.fill('my own remark');
  await expect.poll(() => held).toBe(true);

  const removed = await request.delete(
    `${API_URL}/api/v1/budget-items/${stored.id}?revision=${stored.revision}`,
    { headers: await apiAuth(request) },
  );
  expect(removed.status(), 'the removal has to land, or there is no 404 to meet').toBe(204);

  // Registered before the write is let go: the 404 and the re-read the removal
  // announces can both be over before a listener added afterwards exists.
  const reread = page.waitForResponse(
    (r) => r.request().method() === 'GET' && r.url().includes('/api/v1/plan'),
  );
  release();

  await expect(page.locator('#dataMsg')).toHaveText(/gone with it/);
  await reread;

  // Out of the table, but only once the caret leaves it: a rebuild is held
  // back while somebody is typing, which is why the line is still under their
  // hands when the message arrives.
  await expect(budgetRow(page, 0).note).toHaveValue('my own remark');
  await page.locator('#ceilingInput').click();
  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(0);

  // The other person's removal stands, and stands through a reload. A browser
  // still holding the row would read it on the next load as a create nobody
  // had sent yet and post the line back under a new id, with no action from
  // this person at all.
  expect((await apiPlan(request)).budgetItems).toHaveLength(0);
  await reloadSharedPlanner(page);
  await gotoTab(page, 'budget');
  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(0);

  // A write made afterwards is the witness that nothing went before it: the
  // load queues its work in one pass, and a create it queued would be on the
  // server by the time this one is.
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 2 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item))
    .toEqual(['Flowers']);
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
  // A switch rather than route-then-unroute. Taking a route down while the
  // page has a request in flight can leave that request paused inside
  // Playwright's interception for good — never sent, never failed — and this
  // page is retrying on a timer, so one is in flight more often than not. A
  // wedged request here keeps the write loop "running" for ever and the spec
  // times out twenty seconds later, a long way from the cause.
  let offline = true;
  await page.route('**/api/v1/**', (route) => (offline ? route.abort('internetdisconnected') : route.continue()));
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
  offline = false;
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item), { timeout: 20_000 })
    .toEqual(['Venue deposit']);
  await expect(page.locator('#dataMsg')).toHaveText(/Back in touch with the server/);
});

/*
 * The outage that is not one: the request arrives, the row commits, and only
 * the answer is lost on the way back. Every abort above stages the opposite,
 * where the server never hears the request at all, and the difference between
 * the two is the whole of this bug. Nothing in the browser can tell an answer
 * that never came from a request that never went, so the create goes again,
 * and a create the server has already done used to become a second budget
 * line: the same cost in every total twice, with nothing anywhere saying so.
 *
 * Staged by playing the create upstream by hand and then cutting the browser's
 * own attempt off, because route.abort() alone kills the request before the
 * server sees it.
 */
test('a create whose answer is lost does not become a second line', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  let swallowed = 0;
  let committed = 0;
  await page.route('**/api/v1/budget-items', async (route) => {
    if (swallowed > 0 || route.request().method() !== 'POST') return route.continue();
    swallowed += 1;
    const upstream = await request.post(`${API_URL}/api/v1/budget-items`, {
      headers: await apiAuth(request),
      data: JSON.parse(route.request().postData() || '{}'),
    });
    committed = upstream.status();
    return route.abort('internetdisconnected');
  });

  // Registered before the line is added, and it is the *retry* it catches: the
  // first attempt is cut off and never has a response at all.
  const answered = page.waitForResponse(
    (r) => r.request().method() === 'POST' && r.url().endsWith('/api/v1/budget-items'),
    { timeout: 30_000 },
  );
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });
  await expect.poll(() => committed, { message: 'the first create should have committed' }).toBe(201);
  expect(
    (await answered).status(),
    'a create this server has already done is the row it stored, not a second one',
  ).toBe(200);

  // One line on the server, and the money that was typed on it: the second
  // answer is the row as stored, and whatever was typed while the first answer
  // was not arriving goes up as the ordinary patch that follows.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.unit]), { timeout: 20_000 })
    .toEqual([['Venue deposit', '2500.00']]);

  // And one line on the screen, at the cost of one line. That is where the
  // duplicate did its damage: the next plan read pulls the first copy in as
  // somebody else's new row, and the evening's committed figure reads 5,000
  // for a venue that costs 2,500.
  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(1);
  await expectFigures(page, { committed: 2500, paid: 0, outstanding: 2500, forecast: 2500 });
});

/*
 * The same lost answer, met after a reload, which is what somebody does when
 * the page has been saying for a while that it cannot reach the server. Two
 * things are true by then that the test above does not stage: they kept
 * typing, so the copy in this browser and the row that committed no longer
 * agree, and the plan arrives before the retry does.
 *
 * That plan carries the committed row under the very id the create named, and
 * nothing in the merge can tell it from a row somebody else added, so it is
 * put on the page beside the local one. Adopting the id when the retry is
 * finally answered then renames the local row onto it, and the two are a
 * single id held by two rows, in a list every later pass reads by id. The
 * difference is computed from both and sent from whichever comes first, so
 * the other never comes to match: the line is double counted for good, and
 * its revision, its timestamp and the name against it keep moving for as long
 * as the page is open, with nothing on the plan changing.
 */
test('a create whose answer is lost does not leave two rows under one id', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  // The origin hears the create and the browser hears nothing back, for as
  // long as `offline` says so. Played upstream once: every attempt after the
  // first is the same create going again, which is the whole staging.
  let plays = 0;
  let committed = 0;
  let offline = true;
  await page.route('**/api/v1/budget-items', async (route) => {
    if (!offline || route.request().method() !== 'POST') return route.continue();
    if (plays === 0) {
      plays = 1;
      const upstream = await request.post(`${API_URL}/api/v1/budget-items`, {
        headers: await apiAuth(request),
        data: JSON.parse(route.request().postData() || '{}'),
      });
      committed = upstream.status();
    }
    return route.abort('internetdisconnected');
  });

  const line = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });
  await expect.poll(() => committed, { message: 'the first create should have committed' }).toBe(201);

  // The deposit went up while the answer was not arriving, and then they
  // reloaded the page that had been telling them it was out of touch.
  await line.unit.fill('2600');
  await flushToStorage(page);
  await reloadSharedPlanner(page);

  // Both copies are on the page now: the one this browser has been holding
  // under a name of its own, and the committed one the plan has just brought
  // in under the name the create gave it.
  const rows = page.locator('#budgetBody tr:not(:has(td.empty-cell))');
  await expect(rows).toHaveCount(2);

  // The retry gets through at last, and is answered with the row as stored.
  const answered = page.waitForResponse(
    (r) => r.request().method() === 'POST' && r.url().endsWith('/api/v1/budget-items'),
    { timeout: 30_000 },
  );
  offline = false;
  expect((await answered).status(), 'a create this server has already done').toBe(200);

  // One line on the server, holding the figure that was typed while the
  // answer was not arriving.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.unit]), { timeout: 20_000 })
    .toEqual([['Venue deposit', '2600.00']]);

  // One line on the screen, at the cost of one line, and one row in the copy
  // this browser keeps: a second row under the same id is the state nothing
  // downstream can recover from.
  await expect(rows).toHaveCount(1);
  await expectFigures(page, { committed: 2600, paid: 0, outstanding: 2600, forecast: 2600 });
  await flushToStorage(page);
  const ids = (await readStored(page)).state.budgetItems.map((r) => r.id);
  expect(new Set(ids).size, 'the state holds two rows under one id').toBe(ids.length);

  // And the line is written once after the create rather than for ever: the
  // create, then the one patch that carries what was typed since.
  const stored = (await apiPlan(request)).budgetItems[0];
  expect(stored.revision, 'the write loop settles').toBe(2);
});

/*
 * The same lost answer, with somebody else at the other end of it.
 *
 * The create commits, the answer does not arrive, and before the retry goes a
 * second participant corrects the figure on the line it made: 2,500 was typed
 * from memory and the invoice says 250. The retry is then answered with the
 * row as stored, at the revision their correction gave it, so this browser's
 * copy of the line, untouched since it was typed, becomes a difference
 * against a revision that is current, and the patch that follows lands with
 * no conflict to stop it. The correction is gone, nobody is asked and nobody
 * is told: the committed figure everybody reads goes back to the wrong number.
 */
test('a create answered a second time keeps the correction somebody else made meanwhile', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');

  // The origin hears the create and the browser hears nothing back, for as
  // long as `offline` says so, which is the staging the two specs above use.
  let plays = 0;
  let committed = 0;
  let offline = true;
  await page.route('**/api/v1/budget-items', async (route) => {
    if (!offline || route.request().method() !== 'POST') return route.continue();
    if (plays === 0) {
      plays = 1;
      const upstream = await request.post(`${API_URL}/api/v1/budget-items`, {
        headers: await apiAuth(request),
        data: JSON.parse(route.request().postData() || '{}'),
      });
      committed = upstream.status();
    }
    return route.abort('internetdisconnected');
  });

  const line = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });
  await expect.poll(() => committed, { message: 'the first create should have committed' }).toBe(201);

  // Somebody else has the invoice in front of them and corrects the deposit on
  // the row this browser is still trying to create.
  const stored = (await apiPlan(request)).budgetItems[0];
  expect((await request.patch(`${API_URL}/api/v1/budget-items/${stored.id}`, {
    headers: await apiAuth(request),
    data: { revision: stored.revision, unit: '250.00' },
  })).status(), 'the correction lands').toBe(200);

  // Nothing is typed here meanwhile, and the caret leaves the table: a table
  // somebody is inside is redrawn when they are not.
  await line.paid.blur();

  const answered = page.waitForResponse(
    (r) => r.request().method() === 'POST' && r.url().endsWith('/api/v1/budget-items'),
    { timeout: 30_000 },
  );
  offline = false;
  expect((await answered).status(), 'a create this server has already done').toBe(200);

  // The correction stands, here as well as there, and the person whose create
  // was answered late is told their line was written by somebody else rather
  // than left to notice it in the total.
  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(1);
  await expect(budgetRow(page, 0).unit).toHaveValue('250');
  await expect(page.locator('#dataMsg')).toHaveText(/Both sets of changes have been kept/);
  await expectFigures(page, { committed: 250, paid: 0, outstanding: 250, forecast: 250 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.unit]), { timeout: 20_000 })
    .toEqual([['Venue deposit', '250.00']]);
});

/*
 * The same outage, met from the other end: the origin is already away when the
 * page opens. That is the ordinary start for an installed planner, because the
 * service worker paints the shell with no network at all, and it is also a pod
 * restarting at the wrong moment. The cached copy is on screen and editable.
 *
 * The page used to ask five times over fifteen seconds and then stop, for the
 * rest of its life and without a word: every edit after that went to
 * localStorage only, under a status line that said nothing.
 */
test('a page opened while the origin is away says so, keeps asking, and sends what was typed once it is back', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  // The copy this browser keeps has to be the server's, under the server's id:
  // that is what tells the next page it is looking at a shared plan and not at
  // a planner that only ever lived here. With a database behind it the value
  // is the state and the base it was last agreed against, written together.
  const { id } = (await apiPlan(request)).budgetItems[0];
  await expect
    .poll(() => page.evaluate((key) => {
      window.dispatchEvent(new Event('pagehide'));
      const saved = JSON.parse(localStorage.getItem(key) || '{}');
      return (saved.state || saved).budgetItems.map((i) => i.id);
    }, STORAGE_KEY))
    .toEqual([id]);
  await page.goto('about:blank');

  // Installed, not paused: time passes as it would, and the test may also move
  // it on. The waits being skipped are the page's own backoff, up to thirty
  // seconds a time, and sitting through those would make this a clock.
  await page.clock.install();

  // A switch, for the reason the spec above gives.
  let away = true;
  let asked = 0;
  await page.route('**/api/v1/**', (route) => {
    if (route.request().method() === 'GET' && route.request().url().endsWith('/api/v1/plan')) asked += 1;
    return away ? route.abort('internetdisconnected') : route.continue();
  });
  await page.goto('/');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).committed).toHaveText('€2,500');

  // Six, because the fifth was where it used to stop. Each wait is on the
  // request having gone out, and the clock only supplies the pause before it.
  for (let n = 1; n <= 6; n += 1) {
    await expect.poll(() => asked, { message: `request ${n} for the plan` }).toBeGreaterThanOrEqual(n);
    await page.clock.fastForward(30_000);
  }

  // Said where a person can see it, and before they have typed for an hour.
  await expect(page.locator('#dataMsg')).toHaveText(/not reaching the server/);

  await budgetRow(page, 0).paid.fill('750');
  await budgetRow(page, 0).paid.blur();
  expect((await apiPlan(request)).budgetItems[0].paid).toBe('500.00');

  // Back. The browser says so itself, and that is taken as a reason to ask now
  // rather than when the backoff next comes round.
  away = false;
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.paid]))
    .toEqual([['Venue deposit', '750.00']]);
  await expect(page.locator('#dataMsg')).toHaveText(/Back in touch with the server/);

  // The other question asked at load went unanswered too, and is asked again
  // on the same terms: the door was drawn meanwhile, and the person who was
  // signed in all along is not left looking at it.
  await expect(page.locator('.account-email')).toHaveText(ADMIN_EMAIL);
});

/*
 * The same outage, and this time the page does not live through it.
 *
 * "Still trying — they are safe in this browser meanwhile" is what the status
 * line says while an edit is waiting, and somebody who reads that has every
 * reason to close the tab, or to reload in the hope of mending the connection.
 * On a phone they need not do either: the operating system discards a
 * background tab by itself. So the copy this browser keeps has to include the
 * version the server last confirmed. Without it the next page cannot tell an
 * unsent edit from a copy that is merely old, and the plan that arrives takes
 * the edit with it — silently, and after a promise that it would not.
 */
test('an edit made while the origin is away is still there after a reload, and goes up once it is back', async ({ page, request }) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  // A switch rather than route-then-unroute, for the reason the spec two above
  // gives: a request wedged inside the interception never fails and never
  // lands, and this page always has one in flight.
  let away = true;
  await page.route('**/api/v1/**', (route) => (away ? route.abort('internetdisconnected') : route.continue()));

  // One figure changed and one line added: an update and a create, which are
  // the two things the next page has to read differently from each other.
  await budgetRow(page, 0).paid.fill('750');
  await budgetRow(page, 0).paid.blur();
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1 });
  await expect(page.locator('#dataMsg')).toHaveText(/not reaching the server/, { timeout: 15_000 });
  expect((await apiPlan(request)).budgetItems.map((i) => [i.item, i.paid])).toEqual([['Venue deposit', '500.00']]);

  // The tab goes while the origin is still away, and comes back to the same
  // outage — the order a person meets it in, because the reload is what they
  // try when the notice will not clear.
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
  await page.reload();
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).paid).toHaveValue('750');
  await expect(budgetRow(page, 1).item).toHaveValue('Flowers');
  await expect(page.locator('#dataMsg')).toHaveText(/not reaching the server/, { timeout: 15_000 });

  // Back. Both changes are still the difference between this browser and the
  // version the server last confirmed, and each goes up exactly once: the line
  // added before the reload is created, not created twice.
  away = false;
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => [i.item, i.paid]).sort(), { timeout: 20_000 })
    .toEqual([['Flowers', '0.00'], ['Venue deposit', '750.00']]);
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

test('an import says what it takes from everybody, and saves a copy first', async ({ page, request }, testInfo) => {
  await openSharedPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1, paid: 0 });
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm the guest count', owner: 'Ada', due: '', status: 'not-started' });
  await expect
    .poll(async () => {
      const plan = await apiPlan(request);
      return [plan.budgetItems.length, plan.tasks.length];
    })
    .toEqual([1, 1]);

  // A planner from somewhere else: its ids are not the server's, so every row
  // here is removed to make room for it.
  const incoming = testInfo.outputPath('from-elsewhere.json');
  await fs.writeFile(
    incoming,
    JSON.stringify({
      ceiling: 0,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      sponsors: [],
      budgetItems: [{ id: 'b1', item: 'Imported venue', unit: 1200, qty: 1, paid: 0, sponsors: [], note: '' }],
      tasks: [],
      notes: [],
    }),
  );

  const asked = [];
  page.once('dialog', (dialog) => { asked.push(dialog.message()); dialog.accept().catch(() => {}); });
  const copyPromise = page.waitForEvent('download');
  const chooser = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooser).setFiles(incoming);

  // The generic question is still the last sentence. What is new is everything
  // in front of it: whose planner this is and what disappears from it.
  await expect.poll(() => asked, { message: 'an import must say what it removes' }).toEqual([
    'This changes the planner for everyone, not only in this browser. '
    + 'Lines removed: 1. Tasks removed: 1. '
    + 'A copy of the planner as it stands now is downloaded first. '
    + 'Replace everything currently in this planner with the imported data?',
  ]);

  // The copy is the way back, because the server keeps none: it holds the
  // planner as it was a moment before the replacement.
  const copy = await copyPromise;
  const saved = testInfo.outputPath('copy-before-import.json');
  await copy.saveAs(saved);
  const before = JSON.parse(await fs.readFile(saved, 'utf8'));
  expect(before.budgetItems.map((i) => i.item)).toEqual(['Venue deposit']);
  expect(before.tasks.map((k) => k.name)).toEqual(['Confirm the guest count']);

  await expect(page.locator('#dataMsg')).toHaveText(/^Imported /);
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item))
    .toEqual(['Imported venue']);
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
async function openWithOwnSession(page, request, name = 'grace') {
  const who = await ensureEditor(request, API_URL, name);
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
  // The route is left in place, deliberately. The gate is open, so anything it
  // matches from here on passes straight through — and taking it down at this
  // exact moment is what broke this spec in CI: the page answers the 401 by
  // asking GET /auth/session at once, that request was paused in Playwright's
  // interception as the route was being removed, and it was never sent
  // (`send: -1` in the trace, no failure, no response). The sign-in screen
  // waits on that answer, so it never opened. Nothing was wrong with the page.
}

async function signInThroughTheForm(page, request, name = 'grace') {
  const who = await ensureEditor(request, API_URL, name);
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

test('a refused write opens the sign-in screen by itself, without asking the server a second time', async ({ page, request }) => {
  // This failed once in CI, and the trace said exactly how: the write came
  // back 401, the status line said "your session has ended", the page then
  // asked GET /auth/session to confirm what it had just been told — and that
  // request was never sent, for reasons two hundred attempts could not
  // reproduce. The sign-in screen waited on its answer and never opened.
  //
  // Whatever held that request, a bad connection will do the same to a real
  // person. The 401 was already the answer, so nothing should wait on a second
  // one. Here the second one is made to hang on purpose.
  const cookie = await openWithOwnSession(page, request, 'linus');
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  let asked = 0;
  await page.route('**/api/v1/auth/session', () => { asked += 1; /* and never answered */ });

  await editAsTheSessionEnds(page, request, cookie, 750);

  await expect(page.locator('#authScreen')).toBeVisible();
  await expect(page.locator('#authLede')).toContainText('Your session has ended');
  expect(asked, 'the 401 was the answer; asking again is a round trip and a second thing to lose').toBe(0);
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

/*
 * The same removal met by the other road. A session that ends mid-edit leaves
 * the edit in this browser and nowhere else, and signing back in is a merge
 * rather than an adopt, so the row carrying that edit reaches the same
 * question the 404 above reaches: somebody took the line out while it was
 * being typed in. The answer has to be the same one, or a line somebody
 * deliberately removed comes back on a sign-in nobody connected to it.
 */
test('a line removed while the session was over does not come back on the way in', async ({ page, request }) => {
  const cookie = await openWithOwnSession(page, request);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  await editAsTheSessionEnds(page, request, cookie, 750);
  await expect(page.locator('#authScreen')).toBeVisible();

  const stored = (await apiPlan(request)).budgetItems[0];
  const removed = await request.delete(
    `${API_URL}/api/v1/budget-items/${stored.id}?revision=${stored.revision}`,
    { headers: await apiAuth(request) },
  );
  expect(removed.status()).toBe(204);

  await signInThroughTheForm(page, request);

  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(0);
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 2 });
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item))
    .toEqual(['Flowers']);
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
  await openWithOwnSession(page, request, 'linus');
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
  await signInThroughTheForm(page, request, 'linus');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).item).toHaveValue('Venue deposit');
  await expect(budgetRow(page, 0).paid).toHaveValue('500');
});

// Found in production on the first day: an admin signed out and back in,
// imported the whole plan, saw it on screen, and nobody else ever did. Signing
// out raises a flag so that a signed-out page cannot write its emptied planner
// back; nothing lowered it on the way back in, and the import takes the one
// save path that checks it. The file was not sent, and not stored either — the
// next reload would have taken it off the importer's own screen too.
test('an import made after signing out and back in reaches the server', async ({ page, request }) => {
  await openWithOwnSession(page, request, 'linus');

  // On the same page, with no reload in between: that is what kept the flag.
  await signOutButton(page).click();
  await expect(page.locator('#authScreen')).toBeVisible();
  // Count the plan reads that signing in causes, and let them all finish
  // before importing. There are two: the one that adopts the server's plan,
  // and one more when the live stream opens and the server says `resync`.
  // That second read is why this went unnoticed — every merge ends by pushing
  // whatever is pending, so an import made in the first second after signing
  // in was rescued by it. One made a minute later, as in production, had
  // nothing left to rescue it. This test imports in the quiet afterwards.
  let planReads = 0;
  page.on('response', (res) => {
    if (res.url().endsWith('/api/v1/plan') && res.status() === 200) planReads += 1;
  });
  await signInThroughTheForm(page, request, 'linus');
  await gotoTab(page, 'budget');
  await expect.poll(() => planReads, { message: 'the adopt and the post-subscribe resync' }).toBeGreaterThanOrEqual(2);
  await page.evaluate(() => new Promise((done) => setTimeout(done, 100)));

  const plan = {
    budgetItems: [
      { id: 'b1', item: 'Imported venue', vendor: '', unit: 1200, qty: 1, paid: 300, note: '', sponsors: [] },
      { id: 'b2', item: 'Imported band', vendor: '', unit: 450, qty: 2, paid: 0, note: '', sponsors: [] },
    ],
    tasks: [], sponsors: [], notes: [],
    ceiling: 0, fxRate: 0, inflationPct: 0, splitEvenly: false,
  };
  page.once('dialog', (dialog) => dialog.accept());
  await page.setInputFiles('#importFile', {
    name: 'plan.json', mimeType: 'application/json', buffer: Buffer.from(JSON.stringify(plan)),
  });
  await expect(budgetRow(page, 0).item).toHaveValue('Imported venue');

  // On screen was never the question. This is.
  await expect
    .poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item).sort(), { timeout: 10_000 })
    .toEqual(['Imported band', 'Imported venue']);
  // And kept in this browser, which the flag also prevented.
  expect(await page.evaluate(() => localStorage.getItem('soiree.v1'))).toContain('Imported venue');
});

// Small, at the foot of the screens somebody signed in can open: what to quote
// when reporting a problem. The server tells it to a session and to nobody
// else, so the public page never names its build.
test('the release is shown to somebody signed in, and to nobody else', async ({ page, request }) => {
  const anonymous = await request.get(`${API_URL}/api/v1/version`, { headers: { Cookie: '' } });
  expect(anonymous.status(), 'asked with no session').toBe(401);

  await openWithOwnSession(page, request, 'linus');
  await page.goto('/#/account');
  const line = page.locator('#appVersion');
  await expect(line).toBeVisible();
  // The test binary is built without a release stamped into it.
  await expect(line).toHaveText('soiree dev');
  const size = await line.evaluate((el) => parseFloat(getComputedStyle(el).fontSize));
  expect(size, 'very small').toBeLessThanOrEqual(11.5);

  await page.click('#signOutBtn');
  await expect(page.locator('#authScreen')).toBeVisible();
  await expect(line).toBeHidden();
  await expect(line).toHaveText('');
});

test('signing out with changes that never reached the server asks first', async ({ page, request }) => {
  await openWithOwnSession(page, request, 'linus');
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

/*
 * Reminders, as a switch that is still there afterwards.
 *
 * The planner offers them once, the first time an admin gives a task a due
 * date. Somebody who never does is never asked, "Not now" had no way back, and
 * there was no way to turn them off at all. The account screen has the standing
 * version.
 *
 * The browser's Push API is stubbed: headless Chromium has no push service to
 * subscribe to. Everything on this side of it is real — the page, the routes,
 * the database row.
 *
 * Here rather than in auth-api.spec.js, which answers the planner's own probe
 * with a 404 to keep its pages out of the shared plan. A page told there is no
 * API rightly draws no switch for a feature that needs one.
 */
async function stubPush(page, { permission = 'default', subscribes = true } = {}) {
  await page.addInitScript((opts) => {
    const state = { sub: null, permission: opts.permission };
    const made = {
      endpoint: 'https://push.example.test/send/e2e-device',
      unsubscribe: async () => { state.sub = null; return true; },
      toJSON() {
        return { endpoint: this.endpoint, expirationTime: null, keys: { p256dh: 'BPfixture-p256dh', auth: 'fixture-auth' } };
      },
    };
    const reg = {
      pushManager: {
        getSubscription: async () => state.sub,
        subscribe: async () => {
          // What Brave does until its push setting is on, and any browser with
          // no push service behind it: permission granted, and then this.
          if (!opts.subscribes) throw new DOMException('Registration failed - push service not available', 'AbortError');
          state.sub = made;
          return made;
        },
      },
    };
    Object.defineProperty(navigator, 'serviceWorker', {
      configurable: true,
      value: { ready: Promise.resolve(reg), register: async () => reg, getRegistration: async () => reg, addEventListener() {} },
    });
    window.PushManager = window.PushManager || function PushManager() {};
    function FakeNotification() {}
    FakeNotification.requestPermission = async () => {
      if (state.permission === 'default') state.permission = 'granted';
      return state.permission;
    };
    Object.defineProperty(FakeNotification, 'permission', { get: () => state.permission });
    window.Notification = FakeNotification;
  }, { permission, subscribes });
}

test('an admin can turn reminders on for a device, and off again, from their account', async ({ page }) => {
  await stubPush(page);
  const calls = [];
  page.on('request', (r) => {
    if (r.url().includes('/api/v1/push/subscriptions')) calls.push(`${r.method()} ${new URL(r.url()).search}`);
  });

  // The run's shared session is the bootstrap admin.
  await openSharedPlanner(page, '/#/account');

  await expect(page.locator('#remindersSection')).toBeVisible();
  await expect(page.locator('#remindersState')).toHaveText('Reminders are off on this device.');
  await page.click('#remindersToggle');
  await expect(page.locator('#remindersState')).toHaveText('Reminders are on for this device.');
  await expect(page.locator('#remindersToggle')).toHaveText('Turn off');
  expect(calls).toContain('POST ');

  await page.click('#remindersToggle');
  await expect(page.locator('#remindersState')).toHaveText('Reminders are off on this device.');
  await expect(page.locator('#remindersToggle')).toHaveText('Turn on');
  // The endpoint travels in the query string, as a revision does on any other
  // delete here; a body on DELETE is what this API does not do.
  expect(calls.some((c) => c.startsWith('DELETE ?endpoint=https%3A%2F%2Fpush.example.test'))).toBe(true);
});

test('somebody the digest is never sent to is not offered a switch for it', async ({ page, request }) => {
  // The server pushes to active admins and nobody else, so for an editor this
  // would be a switch connected to nothing.
  await stubPush(page);
  await openWithOwnSession(page, request, 'linus');
  await page.goto('/#/account');
  await expect(page.locator('#signOutBtn')).toBeVisible();
  await expect(page.locator('#remindersSection')).toBeHidden();
});

/*
 * What the switch says when it cannot be one.
 *
 * It had a single sentence for that — "This browser cannot receive
 * notifications from a web page. On an iPhone, add this page to the Home
 * Screen first" — and showed it for everything that was not on, off or
 * blocked. The person who reported it was reading it in Chrome on a Windows
 * PC, where none of it was true: the browser could, and the service worker the
 * server handed out did not parse (internal/httpd renders it; see
 * TestServiceWorkerIsServedAsWritten), so `ready` never settled and the wait
 * on it was reported as the browser's shortcoming.
 *
 * The stubbed tests above cannot see any of that, because a stub is always
 * ready. These let the real worker register, which the suite otherwise blocks
 * (playwright.config.js; service-worker.spec.js is the worker's own spec, and
 * these are here because they need an admin and so a database).
 *
 * In the full Chromium rather than the headless shell the rest of the suite
 * runs in, and for one reason: the shell reports Notification.permission as
 * "denied" whatever the context was granted, so every page in it is "blocked"
 * before the worker is ever asked about. `playwright install chromium` fetches
 * both.
 */
const SAYS_CANNOT = /cannot receive|Home Screen|iPhone/;

let fullChromium = null;
test.afterAll(async () => {
  if (fullChromium) await fullChromium.close();
  fullChromium = null;
});

async function accountWithRealWorker(before) {
  fullChromium = fullChromium || await chromium.launch({ channel: 'chromium' });
  const context = await fullChromium.newContext({ baseURL: API_URL, serviceWorkers: 'allow', permissions: ['notifications'] });
  const page = await context.newPage();
  if (before) await before(page);
  await openSharedPlanner(page, '/#/account');
  return { context, page };
}

test('a browser that can receive reminders is offered the switch, with the real service worker behind it', async () => {
  // Opened straight at the account screen, which is the load where the screen
  // asks before anything else has happened.
  const { context, page } = await accountWithRealWorker();
  try {
    const line = page.locator('#remindersState');
    await expect(line).toHaveText('Reminders are off on this device.', { timeout: 15_000 });
    await expect(page.locator('#remindersToggle')).toBeVisible();
    await expect(page.locator('#remindersToggle')).toHaveText('Turn on');
    // The worker itself, not a stand-in for it: active, and the one with the
    // push handler in it.
    const worker = await page.evaluate(() => navigator.serviceWorker.ready.then((reg) => reg.active && reg.active.scriptURL));
    expect(worker).toBe(`${API_URL}/sw.js`);

    // And as far as turning it on goes without a push service, which a test
    // browser does not have: the real permission, the real worker, the real
    // subscribe() with this run's VAPID key — refused by the browser with
    // "Registration failed". That is exactly the refusal Brave gives until its
    // setting is on, and it used to be answered with "Try again".
    await page.click('#remindersToggle');
    await expect(line).toHaveText(/this browser could not turn reminders on/, { timeout: 15_000 });
    await expect(page.locator('#remindersToggle')).toBeVisible();
  } finally {
    await context.close();
  }
});

test('a service worker that is slow to start is waited for, not called a browser that cannot', async () => {
  // A first visit on a slow line: the worker has eleven files to fetch before
  // it is active, and the account screen asks once, three seconds in. Held
  // here rather than slowed, so that nothing in the test is a clock.
  const { context, page } = await accountWithRealWorker((p) => p.addInitScript(() => {
    const real = ServiceWorkerContainer.prototype.register;
    let release;
    const held = new Promise((resolve) => { release = resolve; });
    window.__releaseWorker = release;
    ServiceWorkerContainer.prototype.register = function register(...args) {
      return held.then(() => real.apply(this, args));
    };
  }));
  try {
    const line = page.locator('#remindersState');
    await expect(line).toHaveText(/still being set up/, { timeout: 15_000 });
    await expect(line).not.toHaveText(SAYS_CANNOT);
    await expect(line).not.toHaveClass(/is-refusal/);
    await expect(page.locator('#remindersToggle')).toBeHidden();

    // And the verdict is revisited: nobody reloads, the worker arrives, and
    // the switch is drawn.
    await page.evaluate(() => window.__releaseWorker());
    await expect(line).toHaveText('Reminders are off on this device.', { timeout: 15_000 });
    await expect(page.locator('#remindersToggle')).toBeVisible();
  } finally {
    await context.close();
  }
});

for (const [name, error, sentence] of [
  ['set to block site data', ['SecurityError', 'The operation is insecure.'], /did not let the page set up reminders.*cookies and site data/],
  ['handed a worker that does not run', ['TypeError', 'ServiceWorker script evaluation failed'], /could not start on this device.*fault is with this site/],
  // Registered, and then the install fails: nothing rejects at all.
  ['whose worker registers and then fails to install', ['redundant', ''], /could not start on this device.*fault is with this site/],
]) {
  test(`a browser ${name} is told that, and not that it cannot receive notifications`, async () => {
    const { context, page } = await accountWithRealWorker((p) => p.addInitScript(([kind, message]) => {
      ServiceWorkerContainer.prototype.register = function register() {
        if (kind === 'redundant') {
          // Fails the moment somebody is listening, so there is no order of
          // events for the page to win or lose.
          const installing = new EventTarget();
          installing.state = 'installing';
          const listen = installing.addEventListener.bind(installing);
          installing.addEventListener = (type, fn) => {
            listen(type, fn);
            queueMicrotask(() => {
              installing.state = 'redundant';
              installing.dispatchEvent(new Event('statechange'));
            });
          };
          return Promise.resolve({ active: null, waiting: null, installing });
        }
        return Promise.reject(kind === 'TypeError' ? new TypeError(message) : new DOMException(message, kind));
      };
    }, error));
    try {
      const line = page.locator('#remindersState');
      await expect(line).toHaveText(sentence, { timeout: 15_000 });
      await expect(line).not.toHaveText(SAYS_CANNOT);
      await expect(page.locator('#remindersToggle')).toBeHidden();
    } finally {
      await context.close();
    }
  });
}

/*
 * And the browsers that really cannot, each told the thing that is true of it.
 *
 * Nothing here is an iPhone: it is Chromium with an iPhone's user-agent string
 * and the three things iOS leaves out of a Safari tab taken away, which is all
 * the page has to go on as well.
 */
const IPHONE = 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko)';
const DEVICES = [{
  name: 'Safari on an iPhone, in a tab',
  userAgent: `${IPHONE} Version/17.5 Mobile/15E148 Safari/604.1`,
  says: /on the Home Screen\. Tap the Share button.*Add to Home Screen.*open the planner from its new icon/,
}, {
  name: 'an iPad, which says it is a Mac',
  userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15',
  touchPoints: 5,
  says: /on the Home Screen\. Tap the Share button/,
}, {
  name: 'Chrome on an iPhone',
  userAgent: `${IPHONE} CriOS/126.0.6478.54 Mobile/15E148 Safari/604.1`,
  says: /^On an iPhone or iPad, reminders start in Safari\. Open this page in Safari/,
}, {
  name: 'a link opened inside another app on an iPhone',
  userAgent: `${IPHONE} Mobile/15E148`,
  says: /^On an iPhone or iPad, reminders start in Safari\./,
}, {
  name: 'the Home Screen app on an iOS older than 16.4',
  userAgent: `${IPHONE} Version/16.3 Mobile/15E148 Safari/604.1`,
  standalone: true,
  says: /needs iOS 16\.4 or newer/,
}, {
  name: 'a Mac whose Safari is too old, which is not an iPad',
  userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.6 Safari/605.1.15',
  touchPoints: 0,
  says: /^This browser cannot receive reminders from a web page\./,
  never: /iPhone|iPad|Home Screen/,
}, {
  name: 'a Windows PC whose browser has no Push API',
  userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0',
  says: /^This browser cannot receive reminders from a web page\. If this is a private window/,
  never: /iPhone|iPad|Home Screen/,
}];

for (const device of DEVICES) {
  test(`reminders, to ${device.name}, say what is true there`, async ({ browser }) => {
    const context = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block', userAgent: device.userAgent });
    const page = await context.newPage();
    await page.addInitScript((d) => {
      for (const gone of ['PushManager', 'Notification']) {
        delete window[gone];
        if (window[gone]) Object.defineProperty(window, gone, { configurable: true, value: undefined });
      }
      if (d.touchPoints !== undefined) Object.defineProperty(Navigator.prototype, 'maxTouchPoints', { configurable: true, get: () => d.touchPoints });
      if (d.standalone) Object.defineProperty(Navigator.prototype, 'standalone', { configurable: true, get: () => true });
    }, { touchPoints: device.touchPoints, standalone: device.standalone });
    try {
      await openSharedPlanner(page, '/#/account');
      const line = page.locator('#remindersState');
      await expect(line).toHaveText(device.says);
      if (device.never) await expect(line).not.toHaveText(device.never);
      await expect(line).toHaveClass(/is-refusal/);
      await expect(page.locator('#remindersToggle')).toBeHidden();
    } finally {
      await context.close();
    }
  });
}

test('blocked notifications come with the way to unblock them, which is not the same on an iPhone', async ({ page, browser }) => {
  await stubPush(page, { permission: 'denied' });
  await openSharedPlanner(page, '/#/account');
  await expect(page.locator('#remindersState')).toHaveText(/^Notifications are blocked for this site\. Click or tap the icon at the start of the address bar.*choose Allow/);
  await expect(page.locator('#remindersToggle')).toBeHidden();

  // There is no address bar in a Home Screen app, and no site settings: the
  // switch is in the phone's own Settings.
  const context = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block', userAgent: `${IPHONE} Version/17.5 Mobile/15E148 Safari/604.1` });
  const phone = await context.newPage();
  try {
    await stubPush(phone, { permission: 'denied' });
    await phone.addInitScript(() => Object.defineProperty(Navigator.prototype, 'standalone', { configurable: true, get: () => true }));
    await openSharedPlanner(phone, '/#/account');
    await expect(phone.locator('#remindersState')).toHaveText(/^Notifications are turned off for this app\. Open the Settings app, tap Notifications/);
  } finally {
    await context.close();
  }
});

test('a browser that says yes and then will not subscribe is told where to look, not to try again', async ({ page }) => {
  await stubPush(page, { subscribes: false });
  await openSharedPlanner(page, '/#/account');
  await expect(page.locator('#remindersState')).toHaveText('Reminders are off on this device.');
  await page.click('#remindersToggle');
  await expect(page.locator('#remindersState')).toHaveText(/this browser could not turn reminders on.*switched off in its settings/);
  // Still there: the setting is changed elsewhere and this is what to press after.
  await expect(page.locator('#remindersToggle')).toBeVisible();
  await expect(page.locator('#remindersToggle')).toHaveText('Turn on');
  // Pressed again with nothing changed, it says the same and not "try again".
  await page.click('#remindersToggle');
  await expect(page.locator('#remindersToggle')).toBeEnabled();
  await expect(page.locator('#remindersState')).toHaveText(/switched off in its settings/);
});

test('the one-time offer only says "blocked" when that is what happened', async ({ page }) => {
  // It said it for everything that was not "on", so a browser with push
  // switched off sent somebody to a notification setting that was fine.
  await stubPush(page, { subscribes: false });
  let planReads = 0;
  page.on('response', (res) => {
    if (res.url().endsWith('/api/v1/plan') && res.status() === 200) planReads += 1;
  });
  await openSharedPlanner(page);
  // Both of a load's reads, so the row below is not redrawn under the click.
  await expect.poll(() => planReads, { message: 'the adopt and the post-subscribe resync' }).toBeGreaterThanOrEqual(2);

  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm the caterer', due: '2030-05-01' });
  const offer = page.locator('p.empty-note', { hasText: 'Want a reminder here' });
  await expect(offer).toBeVisible();
  await offer.getByRole('button', { name: 'Turn on' }).click();

  await expect(page.locator('#dataMsg')).toHaveText('Reminders are not on yet. “Your account” has the switch, and says what is in the way.');
});

/*
 * One connection per load.
 *
 * Two things ask for the plan on an ordinary signed-in load: the page as it
 * starts, and auth.js reporting the session a moment later, while the first
 * request is still in flight. Both used to go out. That is the largest request
 * the page makes, doubled, on every load — and both answers reached adopt(),
 * which is how a planner being carried up to an empty database was, about one
 * run in six, carried up twice.
 *
 * The answer is held here until the session has been reported, so the count is
 * of requests made while the first is unanswered and owes nothing to timing.
 */
test('a signed-in load asks for the plan once, not once per thing that wanted it', async ({ page }) => {
  await signInPage(page);

  const asked = [];
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  await page.route('**/api/v1/plan', async (route) => {
    asked.push(route.request().method());
    await gate;
    await route.continue();
  });

  await page.goto('/');
  // The session has answered, and auth.js has told the planner so: this is the
  // moment the second request used to go out.
  await expect(page.locator('body')).toHaveClass(/signed-in/);
  await expect(page.locator('#accountActs')).toContainText('Sign out');
  expect(asked).toEqual(['GET']);

  release();
  await expect(page.locator('body')).not.toHaveClass(/showing-auth/);
});

/*
 * The other question asked at load, and the same outage.
 *
 * "Who is signed in" got one request, and no answer was drawn as signed out
 * for the life of the page, while the planner beside it retried, got the plan
 * and ran fully synced. The door is still the right thing to draw meanwhile.
 * It is not an answer, so the question is asked again.
 *
 * The worse order first: the plan has already arrived when the probe is lost.
 * "Signed out" then reaches a planner that is running, which stops its three
 * loops and tells a person with a perfectly good session to sign in again.
 */
test('a session probe lost at load is asked again, and nobody signed in is left looking signed out', async ({ page, request }) => {
  await signInPage(page);

  let probes = 0;
  let planned;
  const plan = new Promise((resolve) => { planned = resolve; });
  page.on('response', (r) => { if (r.url().endsWith('/api/v1/plan')) r.finished().then(planned); });
  await page.route('**/api/v1/auth/session', async (route) => {
    probes += 1;
    if (probes > 1) return route.continue();
    await plan;
    return route.abort('internetdisconnected');
  });

  await page.goto('/');
  await expect(page.locator('.account-email')).toHaveText(ADMIN_EMAIL);
  await expect(page.locator('#accountActs')).toContainText('Sign out');
  await expect(page.locator('#dataMsg')).not.toHaveText(/session has ended/);

  // And the planner it stopped is running again: an edit still gets out.
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.item)).toEqual(['Venue deposit']);
});

// What a lost probe cost the person with the least to fall back on: the lock
// is only ever applied on "signed in, as a viewer", so their planner looked
// editable and every edit in it was refused.
test('a viewer whose session probe was lost at load still gets a read-only planner', async ({ page, request }) => {
  const viewer = await ensureEditor(request, API_URL, 'vera', 'viewer');
  await adoptSession(page, await freshSession(request, API_URL, viewer));

  let probes = 0;
  await page.route('**/api/v1/auth/session', (route) => {
    probes += 1;
    return probes > 1 ? route.continue() : route.abort('internetdisconnected');
  });

  await awaitPlan(page, () => page.goto('/'));
  await expect(page.locator('body')).toHaveClass(/role-viewer/);
  await expect(page.locator('.account-email')).toHaveText(viewer.email);
  await gotoTab(page, 'budget');
  await expect(page.locator('#addBudgetRow')).toBeHidden();
  await expect(page.locator('#importData')).toBeDisabled();
});

/* ------------------------------------------------------------------
 * Activity
 * ------------------------------------------------------------------ */

test('an admin can see who changed what, and an editor is not shown the door', async ({ page, request, browser }) => {
  // The editor does the work.
  const cookie = await openWithOwnSession(page, request, 'linus');
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.paid)).toEqual(['500.00']);
  await budgetRow(page, 0).paid.fill('750');
  await expect.poll(async () => (await apiPlan(request)).budgetItems.map((i) => i.paid)).toEqual(['750.00']);

  // No link for them, no screen behind the address, and no data behind that.
  await expect(page.locator('#accountActs button')).toHaveText(['Your account', 'Sign out']);
  await page.goto('/#/activity');
  await expect(page.locator('#authNote')).toContainText('it is for admins');
  await expect(page.locator('#activityList li')).toHaveCount(0);
  const refused = await request.get(`${API_URL}/api/v1/activity`, { headers: { Cookie: cookie } });
  expect(refused.status(), 'an editor asking the server directly').toBe(403);

  // The admin reads it.
  const context = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block' });
  const theirs = await context.newPage();
  try {
    await openSharedPlanner(theirs);
    await theirs.locator('#accountActs button', { hasText: 'Activity' }).click();
    await expect(theirs.locator('#authTitle')).toHaveText('Activity');

    const newest = theirs.locator('#activityList > li').first();
    await expect(newest.locator('.activity-who')).toHaveText('linus-e2e@example.test');
    await expect(newest.locator('.activity-what')).toHaveText('Changed budget line “Venue deposit”');
    // Amounts as the planner shows them, never the minor units they are stored in.
    await expect(newest.locator('.activity-changes li')).toHaveText(['Paid500.00→750.00']);

    // A new row is one sentence; the dozen fields it was born with are not news.
    const made = theirs.locator('#activityList > li', { hasText: 'Added budget line' }).first();
    await expect(made.locator('.activity-what')).toHaveText('Added budget line “Venue deposit”');
    await expect(made.locator('.activity-changes')).toHaveCount(0);
  } finally {
    await context.close();
  }
});

/* ------------------------------------------------------------------
 * Attachments
 *
 * Against a real bucket, because the browser talks to it directly and that is
 * the part a stub cannot stand in for: the preflight, the signed headers, the
 * redirect to a download. The launcher starts one beside the database; a
 * machine that could not gets a server with attachments off, and these skip.
 * ------------------------------------------------------------------ */

const QUOTE = Buffer.from('%PDF-1.7\n% a caterer\'s quote, or near enough\n'.repeat(40));

async function attachmentsOn(page) {
  return page.evaluate(() => {
    const el = document.getElementById('soiree-config');
    try { return !!JSON.parse(el.textContent).attachments; } catch (e) { return false; }
  });
}

/** A budget line that the server already has, which is when files can be added. */
async function savedLine(page, request, item) {
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item, unit: 100, qty: 1, paid: 0 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);
}

async function openFiles(page, row = 0) {
  await page.locator('#budgetBody .files-btn').nth(row).click();
  await expect(page.locator('.files-pop')).toBeVisible();
}

test('a file goes up from one browser, arrives live in another, and comes back down intact', async ({ page, request, browser }) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Catering');

  // Somebody else, already looking at the same plan.
  const elsewhere = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block', acceptDownloads: true });
  const other = await elsewhere.newPage();
  try {
    // Both plan reads that opening the page causes have to be over before the
    // upload: the one that adopts the plan, and the one the server asks for
    // when the live stream opens. Otherwise that second read delivers the file
    // and this passes with the change notice ignored entirely - which it did,
    // the first time it was checked against a page that ignores the notice.
    let planReads = 0;
    other.on('response', (res) => {
      if (res.url().endsWith('/api/v1/plan') && res.status() === 200) planReads += 1;
    });
    await openSharedPlanner(other);
    await gotoTab(other, 'budget');
    await expect(other.locator('#budgetBody .files-btn')).toHaveCount(1);
    await expect(other.locator('#budgetBody .files-count')).toHaveText('');
    await expect.poll(() => planReads, { message: 'the adopt and the post-subscribe resync' }).toBeGreaterThanOrEqual(2);
    await other.evaluate(() => new Promise((done) => setTimeout(done, 100)));

    await openFiles(page);
    await expect(page.locator('.files-none')).toBeVisible();
    await page.setInputFiles('body > input[type=file]', { name: 'Quote – café.pdf', mimeType: 'application/pdf', buffer: QUOTE });

    // On the uploader's screen, and on the server.
    await expect(page.locator('.files-row a.files-name')).toHaveText('Quote – café.pdf');
    await expect(page.locator('#budgetBody .files-count')).toHaveText('1');
    const listed = (await apiPlan(request)).attachments;
    expect(listed.map((a) => [a.name, a.size, a.viewable])).toEqual([['Quote – café.pdf', QUOTE.length, true]]);

    // And on the other screen, with nobody reloading anything.
    await expect(other.locator('#budgetBody .files-count')).toHaveText('1');

    // Down again, byte for byte, under its own name.
    await openFiles(other);
    const [download] = await Promise.all([
      other.waitForEvent('download'),
      other.locator('.files-row .files-dl').click(),
    ]);
    expect(download.suggestedFilename()).toBe('Quote – café.pdf');
    const saved = await fs.readFile(await download.path());
    expect(saved.equals(QUOTE), 'the downloaded bytes are the uploaded bytes').toBe(true);
  } finally {
    await elsewhere.close();
  }
});

test('a file over the limit is refused on the spot, and nothing is asked of the server', async ({ page, request }) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Venue');

  const asked = [];
  page.on('request', (req) => { if (req.url().includes('/api/v1/attachments')) asked.push(req.method()); });

  await openFiles(page);
  // The test server allows 1 MB a file.
  await page.setInputFiles('body > input[type=file]', {
    name: 'scan.pdf', mimeType: 'application/pdf', buffer: Buffer.alloc(1024 * 1024 + 1, 1),
  });
  await expect(page.locator('.files-failed .files-meta')).toContainText('Too large');
  // Refused for good, not offered again.
  await expect(page.locator('.files-failed .link-btn')).toHaveCount(0);
  expect(asked, 'requests to /attachments').toEqual([]);
  expect((await apiPlan(request)).attachments).toEqual([]);
});

test('a file can be removed, and removing a line takes its files with it', async ({ page, request }) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Band');

  await openFiles(page);
  await page.setInputFiles('body > input[type=file]', [
    { name: 'rider.pdf', mimeType: 'application/pdf', buffer: QUOTE },
    { name: 'stage plot.png', mimeType: 'image/png', buffer: Buffer.from('not really a png') },
  ]);
  await expect(page.locator('.files-row a.files-name')).toHaveCount(2);
  await expect(page.locator('#budgetBody .files-count')).toHaveText('2');

  page.once('dialog', (dialog) => dialog.accept());
  await page.locator('.files-row', { hasText: 'rider.pdf' }).locator('.del-btn').click();
  await expect(page.locator('.files-row a.files-name')).toHaveText(['stage plot.png']);
  await expect.poll(async () => (await apiPlan(request)).attachments.map((a) => a.name)).toEqual(['stage plot.png']);

  // The line goes, and the file it still had goes with it. That is the loss
  // nothing can undo, so the click asks first, counts the files, and says the
  // row is everybody's.
  await page.keyboard.press('Escape');
  const asked = [];
  page.once('dialog', (dialog) => { asked.push(dialog.message()); dialog.accept().catch(() => {}); });
  await page.locator('#budgetBody .del-cell .del-btn').first().click();
  await expect.poll(() => asked, { message: 'removing a line with files must ask' }).toEqual([
    'This changes the planner for everyone, not only in this browser. '
    + 'Files on this line: 1. They go with it and cannot be recovered. Remove the line?',
  ]);
  await expect.poll(async () => {
    const plan = await apiPlan(request);
    return [plan.budgetItems.length, plan.attachments.length];
  }).toEqual([0, 0]);
});

/*
 * The same loss, on a page that has not managed to read the plan.
 *
 * The list of what is attached arrives with the plan and with nothing else, so
 * on a page painted from the copy this browser keeps, with the origin away
 * or a session that ended before the reload, it is empty because nothing has
 * filled it, not because the row has no files. A count of zero is ignorance
 * rather than an answer there, and the delete queued against that copy takes
 * the receipts on the line out of the plan and then out of the bucket the
 * moment the origin is back.
 */
test('a delete asks about the files on the row when the plan has not been read', async ({ page, request }) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Catering');

  await openFiles(page);
  await page.setInputFiles('body > input[type=file]', { name: 'quote.pdf', mimeType: 'application/pdf', buffer: QUOTE });
  await expect(page.locator('#budgetBody .files-count')).toHaveText('1');
  await page.keyboard.press('Escape');
  await flushToStorage(page);

  // The tab comes back to an origin that is away: the planner paints from the
  // copy this browser keeps, and the plan read that would say what is
  // attached never lands. What is attached is in neither.
  await page.route('**/api/v1/**', (route) => route.abort('internetdisconnected'));
  await page.goto('/');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).item).toHaveValue('Catering');

  const asked = [];
  page.once('dialog', (dialog) => { asked.push(dialog.message()); dialog.dismiss().catch(() => {}); });
  await page.locator('#budgetBody .del-cell .del-btn').first().click();
  await expect.poll(() => asked, { message: 'a delete that cannot count the files must still ask' }).toEqual([
    'The files on this line cannot be counted until this browser has the planner from the server. '
    + 'Any there are go with it, for everyone, and cannot be recovered. Remove the line?',
  ]);

  // Answered with no, so the line is still on the page and the quote is still
  // on the line.
  await expect(budgetRow(page, 0).item).toHaveValue('Catering');
  expect((await apiPlan(request)).attachments.map((a) => a.name)).toEqual(['quote.pdf']);
});

test('an import counts the files it would destroy before anything is replaced', async ({ page, request }, testInfo) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Catering');

  await openFiles(page);
  await page.setInputFiles('body > input[type=file]', { name: 'quote.pdf', mimeType: 'application/pdf', buffer: QUOTE });
  await expect(page.locator('#budgetBody .files-count')).toHaveText('1');
  await page.keyboard.press('Escape');

  const incoming = testInfo.outputPath('replacement.json');
  await fs.writeFile(
    incoming,
    JSON.stringify({
      ceiling: 0,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      sponsors: [],
      budgetItems: [{ id: 'b1', item: 'Imported catering', unit: 50, qty: 40, paid: 0, sponsors: [], note: '' }],
      tasks: [],
      notes: [],
    }),
  );

  // Dismissed on purpose: what is being tested is that the number is on the
  // screen before the answer, and that saying no costs the quote nothing.
  const asked = [];
  page.once('dialog', (dialog) => { asked.push(dialog.message()); dialog.dismiss().catch(() => {}); });
  const chooser = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooser).setFiles(incoming);

  await expect
    .poll(() => asked.join(''), { message: 'an import must count the files it destroys' })
    .toContain('Files attached to them: 1, and those cannot be recovered.');
  expect((await apiPlan(request)).attachments.map((a) => a.name)).toEqual(['quote.pdf']);
});

test('a viewer can open the files and is offered no way to add or remove one', async ({ page, request, browser }) => {
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await savedLine(page, request, 'Flowers');
  await openFiles(page);
  await page.setInputFiles('body > input[type=file]', { name: 'florist.pdf', mimeType: 'application/pdf', buffer: QUOTE });
  await expect(page.locator('#budgetBody .files-count')).toHaveText('1');

  const viewer = await ensureEditor(request, API_URL, 'vera', 'viewer');
  const context = await browser.newContext({ baseURL: API_URL, serviceWorkers: 'block' });
  const theirs = await context.newPage();
  try {
    await adoptSession(theirs, await freshSession(request, API_URL, viewer));
    await awaitPlan(theirs, () => theirs.goto('/'));
    await gotoTab(theirs, 'budget');
    await theirs.locator('#budgetBody .files-btn').first().click();

    const pop = theirs.locator('.files-pop');
    await expect(pop.locator('a.files-name')).toHaveText('florist.pdf');
    await expect(pop.locator('.files-add')).toHaveCount(0);
    await expect(pop.locator('.del-btn')).toHaveCount(0);
    await expect(pop).toContainText('You can view files, not add them.');

    // Not merely hidden: the server says the same to anybody who asks anyway.
    const id = (await apiPlan(request)).attachments[0].id;
    const refused = await theirs.request.delete(`${API_URL}/api/v1/attachments/${id}`);
    expect(refused.status()).toBe(403);
  } finally {
    await context.close();
  }
});

// From the first day live. Somebody looked for the paperclip, in the dark
// theme, with it on screen, and reported that it was not there. It was drawn
// at half strength; and on the two lines whose names were long enough to wrap,
// the textarea's scrollbar was drawn over it, so there it truly could not be
// seen or pressed.
test('the paperclip can be seen and pressed, on a line with a long name too, in the dark', async ({ page, request }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await openSharedPlanner(page);
  test.skip(!(await attachmentsOn(page)), 'this run has no bucket');
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Cetak-cetak all sign and the cue cards for the master of ceremonies', unit: 25000, qty: 1, paid: 0 });
  await expect.poll(async () => (await apiPlan(request)).budgetItems.length).toBe(1);

  const clip = page.locator('#budgetBody .files-btn').first();
  await expect(clip).toBeVisible();

  // Nothing is drawn over it: whatever is at its centre is the button itself.
  const onTop = await clip.evaluate((btn) => {
    const r = btn.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return !!hit && (hit === btn || btn.contains(hit));
  });
  expect(onTop, 'the element at the centre of the paperclip is the paperclip').toBe(true);

  // The field ends before the button begins, scrollbar included.
  const gap = await page.locator('#budgetBody tr').first().evaluate((tr) => {
    const field = tr.querySelector('td.has-files textarea').getBoundingClientRect();
    const btn = tr.querySelector('.files-btn').getBoundingClientRect();
    return btn.left - field.right;
  });
  expect(gap, 'pixels between the end of the name field and the paperclip').toBeGreaterThanOrEqual(0);

  // As visible as the remove button on the same row, which nobody has failed to find.
  const strength = await page.locator('#budgetBody tr').first().evaluate((tr) => {
    const look = (el) => { const c = getComputedStyle(el); return { opacity: Number(c.opacity), color: c.color }; };
    return { clip: look(tr.querySelector('.files-btn')), remove: look(tr.querySelector('.del-cell .del-btn')) };
  });
  expect(strength.clip.opacity).toBe(1);
  expect(strength.clip.color).toBe(strength.remove.color);

  await clip.click();
  await expect(page.locator('.files-pop')).toBeVisible();
});

test('a deployment with no bucket draws no paperclip at all', async ({ page }) => {
  const { BASE_URL } = require('../servers');
  await page.goto(BASE_URL + '/');
  await page.locator('.tab-btn[data-tab="budget"]').click();
  await page.click('#addBudgetRow');
  await expect(page.locator('#budgetBody .del-cell .del-btn')).toHaveCount(1);
  await expect(page.locator('.files-btn')).toHaveCount(0);
});
