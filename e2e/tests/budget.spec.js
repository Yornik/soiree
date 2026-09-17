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
const { addBudgetLine, budgetRow, expectFigures, gotoTab, money, openPlanner } = require('./helpers');

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
