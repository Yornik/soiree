// @ts-check
/*
 * The reckoning.
 *
 * The same four figures appear in the overview, in the table's totals row and
 * in the repeated block below the table. They are the reason anyone opens this
 * page, so the rule is absolute: the page must never show the same number two
 * different ways. `expectFigures` checks both halves of that — that every copy
 * of a figure reads identically, and that the figure is the arithmetic it
 * claims to be.
 */
const { test, expect } = require('@playwright/test');
const {
  addBudgetLine, addSponsor, budgetRow, expectFigures, flushToStorage, gotoTab, money,
  openLineDetails, openPlanner, tagLine,
} = require('./helpers');
const { BASE_URL } = require('../servers');

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('10000');
});

test('a budget line moves committed, outstanding and both gauges together', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

  // The line itself.
  await expect(row.committed).toHaveText('€2,500');
  await expect(row.outstanding).toHaveText('€2,000');
  await expect(row.outstanding).toHaveClass(/owing/);

  // Every place the same figures are repeated.
  await expectFigures(page, { committed: 2500, paid: 500, outstanding: 2000, forecast: 2500 });
  await expect(page.locator('#sumHeadroom')).toHaveText('€7,500');

  await gotoTab(page, 'overview');

  // 2500 of a 10000 ceiling, and 500 paid of 2500 committed.
  await expect(page.locator('#statBudget')).toHaveText('25%');
  await expect(page.locator('#budgetBarFill')).toHaveAttribute('style', /width:\s*25%/);
  await expect(page.locator('#paidBarPct')).toHaveText('20%');
  await expect(page.locator('#paidBarFill')).toHaveAttribute('style', /width:\s*20%/);
  await expect(page.locator('#mPaidSub')).toHaveText('20% of committed');

  // The start screen is gone and the real overview is in its place.
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
  await expect(page.locator('.when-data')).toBeVisible();
});

test('a second line aggregates rather than replacing', async ({ page }) => {
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  const catering = await addBudgetLine(page, { item: 'Catering', unit: 45, qty: 40, paid: 300 });

  // Unit × quantity, which is the one bit of arithmetic in the row.
  await expect(catering.committed).toHaveText('€1,800');
  await expect(catering.outstanding).toHaveText('€1,500');

  await expectFigures(page, { committed: 4300, paid: 800, outstanding: 3500, forecast: 4300 });

  await gotoTab(page, 'overview');
  await expect(page.locator('#statBudget')).toHaveText('43%');
  await expect(page.locator('#budgetBarFill')).toHaveAttribute('style', /width:\s*43%/);
});

test('editing a quantity re-reckons everything, and removing the line puts it back', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Printed invitations', unit: 4, qty: 40, paid: 0 });
  await expectFigures(page, { committed: 160, paid: 0, outstanding: 160, forecast: 160 });

  await row.qty.fill('60');
  await expect(row.committed).toHaveText('€240');
  await expectFigures(page, { committed: 240, paid: 0, outstanding: 240, forecast: 240 });

  await row.paid.fill('240');
  await expect(row.outstanding).toHaveText('€0');
  await expect(row.outstanding).not.toHaveClass(/owing/);
  await expectFigures(page, { committed: 240, paid: 240, outstanding: 0, forecast: 240 });

  await row.remove.click();
  await expect(page.locator('#budgetBody .empty-cell')).toBeVisible();
  await expectFigures(page, { committed: 0, paid: 0, outstanding: 0, forecast: 0 });
});

test('the inflation buffer is applied to the forecast and eats the headroom', async ({ page }) => {
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 0 });
  await addBudgetLine(page, { item: 'Catering', unit: 45, qty: 40, paid: 0 });

  await page.locator('#inflationInput').fill('4');

  // 4300 × 1.04 = 4472, and the headroom is what is left of the ceiling after
  // the buffer rather than after the quoted total.
  await expectFigures(page, { committed: 4300, paid: 0, outstanding: 4300, forecast: 4472 });
  await expect(page.locator('#sumHeadroom')).toHaveText('€5,528');

  await gotoTab(page, 'overview');
  await expect(page.locator('#mForecastSub')).toHaveText('+4% on quoted');
  await expect(page.locator('#statBudgetSub')).toContainText('leaving');
});

/*
 * The rate field is not on this instance: a second currency is what puts it
 * there, and none is configured. Rather than a fourth server for one input,
 * the config block the page is served with is rewritten on the way through,
 * which is the only thing that decides the question.
 */
test('an exchange rate below one is a rate the field takes', async ({ page }) => {
  await page.route(`${BASE_URL}/`, async (route) => {
    const res = await route.fetch();
    const body = (await res.text()).replace('"secondaryCurrency":""', '"secondaryCurrency":"USD"');
    await route.fulfill({ response: res, body });
  });
  await page.reload();
  await gotoTab(page, 'budget');

  // A euro buys less than a dollar, so euros per dollar is below one. A plan
  // kept in the stronger of its two currencies is the same plan as one kept
  // in the weaker, the other way round, and the field has to take both.
  await expect(page.locator('#rateLabel')).toHaveText('Exchange rate (EUR per USD)');
  const rate = page.locator('#rateInput');
  await rate.fill('0.92');
  expect(await rate.evaluate((el) => {
    const input = /** @type {HTMLInputElement} */ (el);
    return {
      value: input.value,
      valid: input.checkValidity(),
      low: input.validity.rangeUnderflow,
      step: input.validity.stepMismatch,
    };
  })).toEqual({ value: '0.92', valid: true, low: false, step: false });

  // And it is the rate the second figure is then reckoned at: €920 at 0.92
  // euros to the dollar is $1,000.
  await addBudgetLine(page, { item: 'Venue deposit', unit: 920, qty: 1 });
  await gotoTab(page, 'overview');
  expect(money(await page.locator('#mCommittedEur').textContent())).toBe(1000);
});

test('crossing the ceiling is marked, not just reported', async ({ page }) => {
  await page.locator('#ceilingInput').fill('1000');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 0 });

  await gotoTab(page, 'overview');
  await expect(page.locator('#statBudget')).toHaveText('250%');
  await expect(page.locator('#statBudget')).toHaveClass(/warn/);
  // The bar is clamped — a 250% wide fill would run off the page.
  await expect(page.locator('#budgetBarFill')).toHaveAttribute('style', /width:\s*100%/);
  await expect(page.locator('#budgetBarFill')).toHaveClass(/over/);

  await page.locator('#tab-budget').click();
  await page.locator('#ceilingInput').fill('10000');
  await gotoTab(page, 'overview');
  await expect(page.locator('#statBudget')).toHaveText('25%');
  await expect(page.locator('#statBudget')).not.toHaveClass(/warn/);
  await expect(page.locator('#budgetBarFill')).not.toHaveClass(/over/);
});

test('with no ceiling the gauge reports the total rather than a meaningless percentage', async ({ page }) => {
  await page.locator('#ceilingInput').fill('');
  await addBudgetLine(page, { item: 'Photographer', unit: 900, qty: 1, paid: 0 });

  await gotoTab(page, 'overview');
  await expect(page.locator('#statBudget')).toHaveText('€900');
  await expect(page.locator('#statBudgetSub')).toContainText('No ceiling set');
  // A dash, not a figure: there is no headroom to report against no ceiling.
  expect(money(await page.locator('#sumHeadroom').textContent())).toBeNull();
});

test('the exact figures are shown exactly, and the compact ones only in the sub-labels', async ({ page }) => {
  await addBudgetLine(page, { item: 'Venue deposit', unit: 5660, qty: 1, paid: 0 });

  // €5,660 — not €5.7K. Rounding a headline figure is what makes a page
  // impossible to reconcile a bank statement against.
  const committed = await page.locator('#sumTotal').textContent();
  expect(money(committed)).toBe(5660);
  expect(committed).not.toMatch(/[KkMm]/);

  await gotoTab(page, 'overview');
  const overview = await page.locator('#mCommitted').textContent();
  expect(overview).toBe(committed);

  // The gauge sub-label is allowed to be compact: it is a caption, not a
  // figure anyone reconciles against.
  await expect(page.locator('#statBudgetSub')).toContainText('of');
});

test('the totals below the table are the same totals as inside it', async ({ page }) => {
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 300 });
  await addBudgetLine(page, { item: 'Photographer', unit: 900, qty: 1, paid: 0 });

  // The table scrolls sideways on a phone and takes its own tfoot with it, so
  // the repeat below it is the copy that survives. It must not drift.
  const inside = await page.locator('#sumTotal').textContent();
  const below = await page.locator('#sumTotalAlt').textContent();
  expect(below).toBe(inside);

  const owingInside = await page.locator('#sumOwing').textContent();
  const owingBelow = await page.locator('#sumOwingAlt').textContent();
  expect(owingBelow).toBe(owingInside);

  // And the sum of the rows equals the total, which is the only thing that
  // makes any of it trustworthy.
  const rowTotals = await page.locator('#budgetBody tr td:nth-child(4)').allTextContents();
  expect(rowTotals.map(money).reduce((a, b) => a + b, 0)).toBe(money(inside));
});

test('the first row of a fresh grid starts at zero rather than at NaN', async ({ page }) => {
  await page.locator('#addBudgetRow').click();
  const row = budgetRow(page, 0);
  await expect(row.unit).toHaveValue('0');
  await expect(row.qty).toHaveValue('1');
  await expect(row.paid).toHaveValue('0');
  await expect(row.committed).toHaveText('€0');
  await expectFigures(page, { committed: 0, paid: 0, outstanding: 0, forecast: 0 });
});

test('a line paid to the cent is settled, not a rounding error short', async ({ page }) => {
  // 45.33 x 40 is 1813.1999999999998 in floating point, and 1813.2 paid
  // against it leaves a negative fraction of a cent. Rendered with no decimal
  // places that reads "-€0": a ledger that says nothing is outstanding and
  // prints a minus sign in front of it is a ledger nobody trusts. Counting in
  // whole minor units and converting once is what makes it exactly zero.
  const row = await addBudgetLine(page, { item: 'Catering', unit: 45.33, qty: 40, paid: 1813.2 });

  await expect(row.committed).toHaveText('€1,813');
  await expect(row.outstanding).toHaveText('€0');
  await expect(row.outstanding).not.toHaveClass(/owing/);
  await expect(page.locator('#sumOwing')).toHaveText('€0');
  await expect(page.locator('#sumOwingAlt')).toHaveText('€0');
});

/*
 * The decide-by date is not the date the money moves: a quote expires or a
 * slot goes, and the line is late for that decision until something is paid
 * against it. The reminder digest reads the column by exactly that rule
 * (internal/reminders/digest.go), so the row on screen and the mail say the
 * same thing about the same line.
 */
test('a decide-by date that has gone by marks the line, until something is paid', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 0 });

  const details = await openLineDetails(row);
  await details.vendor.fill('The Orangery');
  await details.lockBy.fill('2020-01-01');
  await expect(row.row).toHaveClass(/late/);
  // The row says it where the row has room to: on the button the two fields
  // are behind, whose name carries it for anyone not reading the colour.
  await expect(row.details).toHaveAttribute('aria-label', 'Vendor and decide-by date (late)');

  await page.keyboard.press('Escape');
  await expect(details.pop).toHaveCount(0);

  // Money against the line is the decision having been made, and it is typed
  // out here rather than in the popup: the marker has to move with the row,
  // not only with the field that set it.
  await row.paid.fill('500');
  await expect(row.row).not.toHaveClass(/late/);
  await expect(row.details).toHaveAttribute('aria-label', 'Vendor and decide-by date');

  // A date still ahead is not late at all.
  await row.paid.fill('0');
  const again = await openLineDetails(row);
  await again.lockBy.fill('2099-01-01');
  await expect(row.row).not.toHaveClass(/late/);

  // And both are part of the line like every other field, kept in this
  // browser with the rest of it.
  await flushToStorage(page);
  await page.reload();
  await gotoTab(page, 'budget');
  const kept = await openLineDetails(budgetRow(page, 0));
  await expect(kept.vendor).toHaveValue('The Orangery');
  await expect(kept.lockBy).toHaveValue('2099-01-01');
});

/*
 * Why those two fields are behind a button rather than in columns of their
 * own, kept honest.
 *
 * DEFAULT_COL_WIDTHS adds up to the width of the page, so a tenth and an
 * eleventh column can only be paid for out of the nine that are there, and
 * the ones with width to give are the remark and the money. This is what not
 * taking it bought, and it is measured rather than asserted by eye: an amount
 * that fits its field, and a remark of five words on one line.
 */
test('the grid keeps the width a five-figure amount and a five-word remark need', async ({ page }) => {
  const row = await addBudgetLine(page, {
    item: 'Venue deposit', unit: 12500, qty: 1, paid: 12500, note: 'Balance due one month before',
  });
  const fits = await row.paid.evaluate((el) => el.scrollWidth <= el.clientWidth);
  expect(fits).toBe(true);

  // An empty remark on the line below is what one line measures, so the test
  // does not have a pixel height of its own to go stale.
  const blank = await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1, paid: 0 });
  const oneLine = await blank.note.evaluate((el) => el.scrollHeight);
  expect(await row.note.evaluate((el) => el.scrollHeight)).toBe(oneLine);
});

/*
 * A touchscreen laptop answers yes to (pointer: coarse) at a width where the
 * grid is still a grid, and the suite's other browsers never do. That is why
 * the rule giving the button a thumb's worth of target and the rules that
 * unwind the entry on a phone are in different media blocks: applied here,
 * the stacking put the button on a line of its own inside the cell and made
 * every line 44px taller.
 */
test.describe('on a touchscreen at desk width', () => {
  test.use({ hasTouch: true });

  test('the button sits beside the item name, not under it', async ({ page }) => {
    const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 0 });
    const name = await row.item.boundingBox();
    const btn = await row.details.boundingBox();

    // Beside: on the same line as the name, and clear of its right edge.
    expect(btn.y).toBeLessThan(name.y + name.height);
    expect(name.x + name.width).toBeLessThanOrEqual(btn.x);
  });
});

test('a figure with cents is a valid figure, not one the browser calls invalid', async ({ page }) => {
  // The same 45.33 the line above reckons with. The field used to declare a
  // step of one whole unit, which makes every price with cents a step
  // mismatch: a browser then reports the field as invalid and a screen reader
  // reads that out on a perfectly good quote. These are text fields read by
  // parseAmount now, so there is no step left to mismatch and the only thing
  // that can be wrong with a figure is that it cannot be read at all, which
  // number-input.spec.js covers.
  const row = await addBudgetLine(page, { item: 'Catering', unit: 45.33, qty: 2.5, paid: 22.5 });

  const validity = (locator) => locator.evaluate((el) => ({
    invalid: el.matches(':invalid'), stepMismatch: el.validity.stepMismatch,
  }));
  expect(await validity(row.unit)).toEqual({ invalid: false, stepMismatch: false });
  expect(await validity(row.paid)).toEqual({ invalid: false, stepMismatch: false });
  // Quantity carries three decimals, which is what the column stores.
  expect(await validity(row.qty)).toEqual({ invalid: false, stepMismatch: false });

  // And the cents are still there after the field has been left, rather than
  // rounded away by a control that had opinions about whole units.
  await row.unit.click();
  await row.unit.blur();
  await expect(row.unit).toHaveValue('45.33');
  await expect(row.committed).toHaveText('€113');
});

// Reported from the first real deployment, by people on desktop monitors: "add
// a remove button". There was one. The page was capped at 1000px and the
// table's columns add up to more, so Remarks and the remove button were cut
// off at any screen width, reachable only by scrolling the table sideways with
// nothing to say so.
for (const width of [1280, 1920]) {
  test(`on a ${width}px desktop the whole budget table is on screen, remove button included`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/');
    await gotoTab(page, 'budget');
    await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

    const wrap = page.locator('.table-wrap', { has: page.locator('#budgetBody') });
    const overflow = await wrap.evaluate((el) => el.scrollWidth - el.clientWidth);
    expect(overflow, 'pixels of table hidden behind a sideways scroll').toBe(0);

    const remove = page.locator('#budgetBody .del-btn').first();
    await expect(remove).toBeInViewport({ ratio: 1 });
    await remove.click();
    // The line is gone. (The empty table draws a row of its own to say so.)
    await expect(page.locator('#budgetBody .del-btn')).toHaveCount(0);
  });
}

// "It is ugly if it is a long title." The name field was as tall as its row and
// no taller, so a name that wrapped showed a line and a half and a scrollbar.
test('a long name makes its row taller instead of hiding behind a scrollbar', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/');
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Band', unit: 100, qty: 1, paid: 0 });
  await addBudgetLine(page, { item: 'Printing of every sign, the pop-up banner by the lift and the cue cards', unit: 25, qty: 1, paid: 0 });

  const short = budgetRow(page, 0).item;
  const long = budgetRow(page, 1).item;
  const hidden = (field) => field.evaluate((el) => el.scrollHeight - el.clientHeight);

  expect(await hidden(long), 'pixels of the name out of sight').toBeLessThanOrEqual(1);
  const heights = await Promise.all([short, long].map((f) => f.evaluate((el) => el.getBoundingClientRect().height)));
  expect(heights[1], 'the long name takes more room than the short one').toBeGreaterThan(heights[0] * 1.5);
  expect(await long.evaluate((el) => getComputedStyle(el).overflowY)).toBe('hidden');

  // A tab that is not showing cannot be measured, and sizing a field to a
  // height of zero makes its text disappear. Away and back, it still fits.
  await gotoTab(page, 'tasks');
  await gotoTab(page, 'budget');
  expect(await hidden(long)).toBeLessThanOrEqual(1);
  expect(await long.evaluate((el) => el.getBoundingClientRect().height)).toBeGreaterThan(30);

  // Typing more makes more room; a narrower column does too.
  const before = await long.evaluate((el) => el.getBoundingClientRect().height);
  await long.fill('Printing of every sign, the pop-up banner by the lift, the cue cards, the table numbers, the menu cards and the thank-you notes');
  expect(await hidden(long)).toBeLessThanOrEqual(1);
  expect(await long.evaluate((el) => el.getBoundingClientRect().height)).toBeGreaterThan(before);

  // And after a reload, when the table is drawn from what was saved.
  await page.reload();
  await gotoTab(page, 'budget');
  expect(await hidden(budgetRow(page, 1).item)).toBeLessThanOrEqual(1);
});

test('who is covering what is drawn as shares, and a line paid in full says so', async ({ page }) => {
  await page.goto('/');
  await gotoTab(page, 'budget');
  await addSponsor(page, { code: 'North', name: 'Ada' });
  await addBudgetLine(page, { item: 'Venue', unit: 3000, qty: 1, paid: 3000 });
  await addBudgetLine(page, { item: 'Band', unit: 1000, qty: 1, paid: 0 });
  await tagLine(budgetRow(page, 0), ['North']);

  const shares = await page.locator('#splitList li').evaluateAll((lis) => lis.map((li) => [
    li.querySelector('span').textContent,
    li.querySelector('.pct').textContent,
    parseFloat(li.querySelector('.share-fill').style.width),
    li.querySelector('.share-fill').classList.contains('unassigned'),
  ]));
  expect(shares).toEqual([['North', '75%', 75, false], ['Unassigned', '25%', 25, true]]);

  const settled = () => page.locator('#budgetBody tr').evaluateAll((trs) => trs.map((tr) => tr.classList.contains('settled')));
  expect(await settled()).toEqual([true, false]);
  await budgetRow(page, 1).paid.fill('1000');
  expect(await settled()).toEqual([true, true]);
  await budgetRow(page, 0).paid.fill('2999');
  expect(await settled()).toEqual([false, true]);
});

