// @ts-check
/*
 * What this becomes the day after.
 *
 * The event is dated and happens once. Past the date the planner stops being a
 * plan: it presents the final reckoning, hides the controls that imply
 * anything can still change, and offers one visible way back for the case that
 * matters — the date was wrong, or something still has to be settled.
 *
 * The clock is moved with `setFixedTime`, which moves `Date.now()` and nothing
 * else. `install()` would also fake `setTimeout`, and the app's 500 ms
 * debounced save runs on that: these tests would then fail for a reason that
 * has nothing to do with the archive.
 */
const { test, expect } = require('@playwright/test');
const { STORAGE_KEY, gotoTab, money, readStored } = require('./helpers');

// The fixture instance is dated 2030-06-12.
const EVENT_DAY = '2030-06-12T09:00:00Z';
const DAY_AFTER = '2030-06-13T09:00:00Z';

// A settled planner: two lines, one paid in full, one still owing, split
// between two people. Planted before the first navigation, because a locked
// page cannot be typed into afterwards.
const SETTLED = {
  ceiling: 10000,
  inflationPct: 0,
  fxRate: 0,
  splitEvenly: false,
  sponsors: [
    { id: 's1', code: 'Rose', name: 'Ada' },
    { id: 's2', code: 'Ivy', name: 'Grace' },
  ],
  budgetItems: [
    { id: 'b1', item: 'Venue', unit: 2000, qty: 1, paid: 2000, sponsors: ['s1'], note: '' },
    { id: 'b2', item: 'Catering', unit: 1000, qty: 1, paid: 400, sponsors: ['s2'], note: '' },
  ],
  tasks: [{ id: 't1', name: 'Return the glassware', owner: 'Ada', due: '', status: 'not-started' }],
  notes: [],
};

/** Opens the planner with a planted state, at a given instant. */
async function openAt(page, when, state = SETTLED, base = '/') {
  await page.clock.setFixedTime(new Date(when));
  // Only if nothing is there yet. An init script runs on every document,
  // including a reload — and a test that re-plants its fixture on reload can
  // never see whether anything survived one.
  await page.addInitScript(
    ([key, payload]) => {
      try {
        if (!localStorage.getItem(key)) localStorage.setItem(key, payload);
      } catch (e) {
        /* not on the origin yet */
      }
    },
    [STORAGE_KEY, JSON.stringify(state)],
  );
  await page.goto(base);
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
}

test('on the day itself nothing has changed', async ({ page }) => {
  await openAt(page, EVENT_DAY);

  // The morning of is when a planner is most useful. The ledger closes the day
  // after the date, not on it.
  await expect(page.locator('body')).not.toHaveClass(/is-archived/);
  await expect(page.locator('#archiveNote')).toBeHidden();
  await expect(page.locator('#daysNum')).toHaveText('0');
  await expect(page.locator('#daysLabel')).toHaveText('days to go');

  await gotoTab(page, 'budget');
  await expect(page.locator('#addBudgetRow')).toBeVisible();
});

test('the day after, the planner reads as a record rather than a plan', async ({ page }) => {
  await openAt(page, DAY_AFTER);

  await expect(page.locator('body')).toHaveClass(/is-archived/);

  // Nothing left to count down to: the numeral goes, the date stays.
  await expect(page.locator('#daysNum')).toBeHidden();
  await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
  await expect(page.locator('#daysLabel')).toHaveText('the ledger is closed');

  // The banner says what happened, in the interface's voice, and carries the
  // way back with it rather than hiding it behind a flag.
  await expect(page.locator('#archiveNote')).toBeVisible();
  await expect(page.locator('#archiveLine')).toContainText('This event has passed');
  await expect(page.locator('#reopenPlanner')).toHaveText('Reopen for editing');

  // The final reckoning: the four figures, and what each person covered.
  await expect(page.locator('.settlement')).toBeVisible();
  const settle = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#settleList li')).map((li) => ({
      who: li.children[0].textContent,
      amount: li.querySelector('.amt').textContent,
    })),
  );
  expect(settle.map((r) => ({ who: r.who, amount: money(r.amount) }))).toEqual([
    { who: 'Rose — Ada', amount: 2000 },
    { who: 'Ivy — Grace', amount: 1000 },
  ]);

  // And what was left outstanding, which is the figure an archive exists for.
  expect(money(await page.locator('#mOutstanding').textContent())).toBe(600);
  expect(money(await page.locator('#mPaid').textContent())).toBe(2400);
});

test('a closed ledger offers nothing to edit, but everything to read', async ({ page }) => {
  await openAt(page, DAY_AFTER);
  await gotoTab(page, 'budget');

  // Adding and removing are gone outright: a disabled "add" is a promise the
  // page cannot keep.
  await expect(page.locator('#addBudgetRow')).toBeHidden();
  await expect(page.locator('#addSponsor')).toBeHidden();
  await expect(page.locator('#budgetBody .del-btn').first()).toBeHidden();
  await expect(page.locator('#resetSizes')).toBeHidden();

  // Fields are readonly rather than disabled, so the figures stay selectable
  // and copyable — which is most of what an archive is for.
  const cell = page.locator('#budgetBody tr').first().locator('td').nth(1).locator('input');
  await expect(cell).toHaveAttribute('readonly', '');
  await expect(cell).toHaveValue('2000');
  // The user's own path: put the caret in the field and type. A readonly input
  // takes focus and takes nothing else.
  await cell.click();
  await page.keyboard.type('9999');
  await expect(cell).toHaveValue('2000');

  await expect(page.locator('#splitEvenly')).toBeDisabled();
  await expect(page.locator('#budgetBody .by-btn').first()).toBeDisabled();

  await gotoTab(page, 'tasks');
  await expect(page.locator('#tasksBody select.status-select').first()).toBeDisabled();
  await expect(page.locator('#addTaskRow')).toBeHidden();

  // Export stays open: an archive nobody can take a copy of is a worse
  // archive. Import does not — replacing the planner is an edit.
  await expect(page.locator('#exportData')).toBeEnabled();
  await expect(page.locator('#importData')).toBeDisabled();
});

test('reopening is one deliberate click, and it sticks', async ({ page }) => {
  await openAt(page, DAY_AFTER);

  await page.locator('#reopenPlanner').click();

  await expect(page.locator('body')).not.toHaveClass(/is-archived/);
  await expect(page.locator('body')).toHaveClass(/is-reopened/);
  await expect(page.locator('#archiveLine')).toContainText('Reopened for editing');
  await expect(page.locator('#reopenPlanner')).toHaveText('Close the planner');
  await expect(page.locator('#daysLabel')).toHaveText('the day has passed');

  await gotoTab(page, 'budget');
  await expect(page.locator('#addBudgetRow')).toBeVisible();
  const cell = page.locator('#budgetBody tr').first().locator('td').nth(4).locator('input');
  await expect(cell).not.toHaveAttribute('readonly', '');
  await cell.fill('2100');
  await expect(page.locator('#budgetBody tr').first().locator('td').nth(5)).toHaveText('-€100');

  // Written straight through rather than on the debounce: this is the setting
  // someone changes and then closes the tab.
  expect(await readStored(page).then((s) => s.reopened)).toBe(true);

  // And it survives the reload, or "the date was wrong" is a fix that lasts
  // until you look away.
  await page.reload();
  await expect(page.locator('body')).toHaveClass(/is-reopened/);
  await gotoTab(page, 'budget');
  await expect(page.locator('#addBudgetRow')).toBeVisible();
});

test('a reopened planner can be closed again once it is settled', async ({ page }) => {
  await openAt(page, DAY_AFTER, { ...SETTLED, reopened: true });

  await expect(page.locator('body')).toHaveClass(/is-reopened/);
  await page.locator('#reopenPlanner').click();

  await expect(page.locator('body')).toHaveClass(/is-archived/);
  await expect(page.locator('#reopenPlanner')).toHaveText('Reopen for editing');
  await gotoTab(page, 'budget');
  await expect(page.locator('#addBudgetRow')).toBeHidden();
});

test('a planner saved before this existed opens as a planner, not an archive', async ({ page }) => {
  // No `reopened` key at all, which is every planner saved until now.
  const legacy = { ...SETTLED };
  delete legacy.reopened;

  await openAt(page, EVENT_DAY, legacy);
  await expect(page.locator('body')).not.toHaveClass(/is-archived/);
  await expect(page.locator('body')).not.toHaveClass(/is-reopened/);
  expect(await readStored(page)).not.toBeNull();
});

// The assertions below read Dutch, which this instance speaks because it is
// served with SOIREE_LOCALE=nl-NL. The page asks the browser first, so the
// browser here asks for a language there is no translation for — which is the
// case the deployment's locale exists to answer.
test.describe('on the instance with no event date', () => {
  test.use({ locale: 'de-DE' });

  test('with no event date configured there is no "after"', async ({ page }, testInfo) => {
    // This instance is served with SOIREE_EVENT_DATE unset. However far the
    // clock is moved, there is no date for it to be past.
    await openAt(page, '2099-01-01T00:00:00Z', SETTLED, testInfo.config.metadata.altBaseURL + '/');

    await expect(page.locator('body')).not.toHaveClass(/is-archived/);
    await expect(page.locator('body')).not.toHaveClass(/is-reopened/);
    await expect(page.locator('#archiveNote')).toBeHidden();
    await expect(page.locator('#daysLabel')).toHaveText('geen datum ingesteld');

    await gotoTab(page, 'budget');
    await expect(page.locator('#addBudgetRow')).toBeVisible();
    await expect(page.locator('#budgetBody .del-btn').first()).toBeVisible();
  });
});

// The event is on a calendar day, in a place, and the page has to say the same
// thing about it to everybody. The server under test is configured for the
// 12th at +09:00, at 01:00 - an instant that is still the 11th in UTC and in
// Amsterdam, and the morning of the 11th in Los Angeles.
for (const timezoneId of ['Asia/Tokyo', 'Europe/Amsterdam', 'America/Los_Angeles']) {
  test(`the day and the days to go are the same for a reader in ${timezoneId}`, async ({ browser }, testInfo) => {
    const context = await browser.newContext({ baseURL: testInfo.project.use.baseURL, timezoneId, locale: 'en-US' });
    const page = await context.newPage();
    try {
      // 23:00 on the 11th where the event is: one day to go, for everyone.
      await openAt(page, '2030-06-11T14:00:00Z');
      await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
      await expect(page.locator('#daysNum')).toHaveText('1');
      await expect(page.locator('#daysLabel')).toHaveText('day to go');
    } finally {
      await context.close();
    }

    const later = await browser.newContext({ baseURL: testInfo.project.use.baseURL, timezoneId, locale: 'en-US' });
    const onTheDay = await later.newPage();
    try {
      // Ninety minutes on, it is 00:30 on the 12th there. It is still the 11th
      // in UTC, in Amsterdam and in Los Angeles, and it is the day for all of them.
      await openAt(onTheDay, '2030-06-11T15:30:00Z');
      await expect(onTheDay.locator('#statDaysLabel')).toHaveText('June 12, 2030');
      await expect(onTheDay.locator('#daysNum')).toHaveText('0');
    } finally {
      await later.close();
    }
  });
}
