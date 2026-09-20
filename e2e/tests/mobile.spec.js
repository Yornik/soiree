// @ts-check
/*
 * The budget grid on a phone.
 *
 * Nine columns, a frozen first column and drag handles on the header are a
 * desk interaction. Below the breakpoint the same markup has to become a
 * stack of entries — same DOM, same ids, same cell order, so everything else
 * in this suite still addresses it the same way — with the money still in one
 * right-aligned column that reconciles against the totals.
 *
 * These tests assert the layout, not the styling: what scrolls, what is
 * reachable, how big a target is, and whether the arithmetic still lines up.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, budgetRow, gotoTab, money, openPlanner } = require('./helpers');

// A small phone in portrait. Narrower than anything else this suite runs at,
// which is the point: if it works here it works on the rest.
test.use({ viewport: { width: 390, height: 844 } });

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500, note: 'Balance due a month out' });
  await addBudgetLine(page, { item: 'Catering', unit: 45, qty: 40, paid: 0 });
});

test('the grid stacks instead of scrolling sideways', async ({ page }) => {
  // The desktop grid is ~1200px wide inside a 390px viewport. If it were still
  // a grid here, this container would have something to scroll.
  const overflow = await page.locator('.table-wrap').first().evaluate(
    (el) => el.scrollWidth - el.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);

  // Nothing pushes the document itself sideways either.
  const bodyOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(bodyOverflow).toBeLessThanOrEqual(1);

  // The header row is the one part that cannot stack: with no columns left to
  // head, it is removed rather than left floating above the entries.
  await expect(page.locator('#budgetTable thead')).toBeHidden();

  // Each entry is as wide as the panel, which is what makes it an entry rather
  // than a row.
  const row = page.locator('#budgetBody tr').first();
  const rowBox = await row.boundingBox();
  const itemBox = await budgetRow(page, 0).item.boundingBox();
  expect(itemBox.width).toBeGreaterThan(rowBox.width * 0.8);
});

test('every figure keeps the name of the column it came from', async ({ page }) => {
  // With the header gone the label travels with the cell, printed from
  // data-label. Without this an entry is a column of unexplained numbers.
  const labels = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#budgetBody tr:first-child td')).map((td) =>
      td.getAttribute('data-label'),
    ),
  );
  expect(labels).toEqual([
    null, // the item names the entry and needs no label above it
    'Unit',
    'Qty',
    'Committed',
    'Paid',
    'Outstanding',
    'Cost by',
    'Remarks',
    'Remove',
  ]);

  // And they are actually drawn, not merely present as attributes.
  const drawn = await page.evaluate(() => {
    const td = document.querySelector('#budgetBody tr:first-child td:nth-child(4)');
    return window.getComputedStyle(td, '::before').content;
  });
  expect(drawn).toContain('Committed');
});

test('the item leads the entry and removing it sits at the foot, clear of the fields', async ({ page }) => {
  const row = budgetRow(page, 0);
  const item = await row.item.boundingBox();
  const paid = await row.paid.boundingBox();
  const remove = await row.remove.boundingBox();

  // Reading order down the entry: name, then money, then the destructive one.
  expect(item.y).toBeLessThan(paid.y);
  expect(paid.y).toBeLessThan(remove.y);

  // Not adjacent to the last field it could be mistaken for.
  const note = await row.note.boundingBox();
  expect(remove.y).toBeGreaterThan(note.y + note.height);

  // The item is set larger than the fields under it: it is the thing you scan
  // an entry for.
  const itemSize = await row.item.evaluate((el) => parseFloat(window.getComputedStyle(el).fontSize));
  const paidSize = await row.paid.evaluate((el) => parseFloat(window.getComputedStyle(el).fontSize));
  expect(itemSize).toBeGreaterThan(paidSize);
});

test('resize grips are gone and every target takes a thumb', async ({ page }) => {
  // Dragging a column edge means nothing when there are no columns, and a
  // 10px-wide handle means nothing to a finger either way. Hidden, not shrunk.
  await expect(page.locator('#budgetTable .col-grip').first()).toBeHidden();
  await expect(page.locator('#budgetTable .row-grip').first()).toBeHidden();
  await expect(page.locator('#resetSizes')).toBeHidden();

  const row = budgetRow(page, 0);
  for (const [name, locator] of [
    ['remove', row.remove],
    ['cost by', row.by],
    ['paid', row.paid],
    ['item', row.item],
  ]) {
    const box = await locator.boundingBox();
    expect(box.height, `${name} is too small to hit with a thumb`).toBeGreaterThanOrEqual(44);
  }

  await expect(page.locator('#addBudgetRow')).toBeVisible();
  const add = await page.locator('#addBudgetRow').boundingBox();
  expect(add.height).toBeGreaterThanOrEqual(44);
});

test('the totals still reconcile under the entries they total', async ({ page }) => {
  // The tfoot is the point of the whole layout: it has to survive the stack,
  // carry its own labels, and agree with the lines above it.
  await expect(page.locator('#sumTotal')).toBeVisible();
  await expect(page.locator('#sumPaid')).toBeVisible();
  await expect(page.locator('#sumOwing')).toBeVisible();

  const rowTotals = await page.locator('#budgetBody tr td:nth-child(4)').allTextContents();
  expect(rowTotals.map(money).reduce((a, b) => a + b, 0)).toBe(money(await page.locator('#sumTotal').textContent()));
  expect(money(await page.locator('#sumPaid').textContent())).toBe(500);
  expect(money(await page.locator('#sumOwing').textContent())).toBe(3800);

  const labelled = await page.evaluate(() =>
    ['sumTotal', 'sumPaid', 'sumOwing'].map((id) => document.getElementById(id).getAttribute('data-label')),
  );
  expect(labelled).toEqual(['Committed', 'Paid', 'Outstanding']);

  // Every figure sits in one right-aligned money column — each entry's
  // committed figure and the totals beneath them share an edge — which is what
  // makes the column addable by eye. Unit and quantity are deliberately not in
  // this set: they sit in the left half of the entry by design, and they are
  // inputs rather than figures anyone reconciles a statement against.
  const rights = await page.evaluate(() => {
    const edge = (el) => Math.round(el.getBoundingClientRect().right);
    return [
      ...Array.from(document.querySelectorAll('#budgetBody tr td:nth-child(4)')).map(edge),
      ...['sumTotal', 'sumPaid', 'sumOwing'].map((id) => edge(document.getElementById(id))),
    ];
  });
  expect(rights.length).toBe(5);
  expect(new Set(rights).size).toBe(1);

  // The repeat below the table now only carries what the tfoot does not.
  // Printing committed twice, four lines apart, invites the reader to check
  // whether the two agree.
  await expect(page.locator('#sumForecast')).toBeVisible();
  await expect(page.locator('#sumHeadroom')).toBeVisible();
  await expect(page.locator('#sumTotalAlt')).toBeHidden();
  await expect(page.locator('#sumOwingAlt')).toBeHidden();
  // Hidden, not dropped: it is still the same figure, and still has to agree.
  expect(await page.locator('#sumTotalAlt').textContent()).toBe(
    await page.locator('#sumTotal').textContent(),
  );
});

test('a line can still be edited and removed with nothing but taps', async ({ page }) => {
  const row = budgetRow(page, 0);
  await row.qty.fill('2');
  await expect(row.committed).toHaveText('€5,000');

  await row.by.click();
  await expect(page.locator('.sp-pop')).toBeVisible();
  await page.keyboard.press('Escape');

  await row.remove.click();
  await expect(page.locator('#budgetBody tr:not(:has(td.empty-cell))')).toHaveCount(1);
});

test('no field is small enough to make the browser zoom into it', async ({ page }) => {
  // iOS Safari zooms the whole page whenever a focused control is set under
  // 16px, and does not zoom back out afterwards. So this is a floor rather
  // than a preference: at 14px every tap into a field shifted the layout and
  // left the person to pinch their way back.
  //
  // Every control the document carries, not only the ones this spec fills in.
  // The accounts screens are in the same document, and the first field an
  // invited person meets on a phone is the one on set-password.
  const small = await page.evaluate(() => {
    const fields = 'input[type="text"], input[type="number"], input[type="date"],'
      + ' input[type="email"], input[type="password"], select, textarea';
    return Array.from(document.querySelectorAll(fields))
      .map((el) => ({
        field: el.id || el.className || el.tagName.toLowerCase(),
        size: parseFloat(window.getComputedStyle(el).fontSize),
      }))
      .filter((f) => f.size < 16);
  });
  expect(small).toEqual([]);
});
