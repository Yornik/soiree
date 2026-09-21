// @ts-check
/*
 * The budget grid and the task list on a phone.
 *
 * Nine columns, a frozen first column and drag handles on the header are a
 * desk interaction, and so are the five columns of the task list. Below the
 * breakpoint the same markup has to become a stack of entries — same DOM,
 * same ids, same cell order, so everything else in this suite still addresses
 * it the same way — with the money still in one right-aligned column that
 * reconciles against the totals.
 *
 * These tests assert the layout, not the styling: what scrolls, what is
 * reachable, how big a target is, and whether the arithmetic still lines up.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, addTask, budgetRow, gotoTab, money, openPlanner, tagLine } = require('./helpers');

/** How far the document itself can be scrolled sideways, in CSS pixels. */
function documentOverflow(page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
}

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
    'Vendor',
    'Decide by',
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

/*
 * The task list. Five columns is a desk layout too, and it was never given
 * the treatment the budget grid got: at this width the name field was 70px
 * wide, about eight characters of "Confirm final guest count", and the
 * status and the remove button sat off the right-hand edge of the screen
 * with nothing on the page to say they were there.
 */
test('a task becomes an entry instead of a row running off the screen', async ({ page }) => {
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada', due: '2030-05-01' });

  // Nothing to scroll: not the region around the table, and not the page.
  const wrap = page.locator('#panel-tasks .table-wrap');
  expect(await wrap.evaluate((el) => el.scrollWidth - el.clientWidth)).toBeLessThanOrEqual(1);
  expect(await documentOverflow(page)).toBeLessThanOrEqual(1);

  // The header row is the one part that cannot stack. It goes, and so does
  // the off-screen "Remove" it carried for screen readers: that span is
  // absolutely positioned inside a cell nothing positions, so it escaped the
  // region's clip and panned the whole document sideways.
  await expect(page.locator('#panel-tasks thead')).toBeHidden();

  const row = page.locator('#tasksBody tr').first();
  const rowBox = await row.boundingBox();

  // The name leads the entry across its full width rather than being cut to
  // a word and a half.
  const name = await row.locator('td').nth(0).locator('input').boundingBox();
  expect(name.width).toBeGreaterThan(rowBox.width * 0.8);

  // And what used to be past the right edge is on the screen.
  const viewport = page.viewportSize().width;
  for (const [what, locator] of [
    ['status', row.locator('select.status-select')],
    ['remove', row.locator('td.del-cell button')],
  ]) {
    const box = await locator.boundingBox();
    expect(box.x + box.width, `${what} is off the right edge`).toBeLessThanOrEqual(viewport);
  }

  // Reading order down the entry: what it is, then who has it and when, then
  // what state it is in, with the destructive one last.
  const owner = await row.locator('td').nth(1).locator('input').boundingBox();
  const status = await row.locator('select.status-select').boundingBox();
  const remove = await row.locator('td.del-cell button').boundingBox();
  expect(name.y).toBeLessThan(owner.y);
  expect(owner.y).toBeLessThan(status.y);
  expect(remove.y).toBeGreaterThanOrEqual(status.y);
});

test('an empty task list does not pan the page sideways either', async ({ page }) => {
  // The header row is drawn whether or not there is anything under it, so
  // this was the state a phone met before adding a single task.
  await gotoTab(page, 'tasks');
  await expect(page.locator('#tasksBody td.empty-cell')).toBeVisible();
  expect(await documentOverflow(page)).toBeLessThanOrEqual(1);
});

/*
 * Long words and large figures. A callsign and a name are whatever somebody
 * types, and the lists that are laid out from them are grids: a column told
 * to take the space left over will not shrink below the longest word in it,
 * so one unbroken word or one figure with enough digits used to make the
 * whole page scroll sideways.
 */
test('a callsign too long to break does not widen the page', async ({ page }) => {
  const code = 'C'.repeat(40);
  await addSponsor(page, { code, name: 'Example Family' });
  await tagLine(budgetRow(page, 0), [code]);

  await expect(page.locator('#splitList li').first()).toContainText(code);
  expect(await documentOverflow(page)).toBeLessThanOrEqual(1);
});

test('a large figure beside a sponsor keeps their remove button on the screen', async ({ page }) => {
  await addSponsor(page, { code: 'AB', name: 'Example Family' });
  await budgetRow(page, 0).unit.fill('125000000');
  await tagLine(budgetRow(page, 0), ['AB']);

  await expect(page.locator('#sponsorGrid .sp-amt')).toContainText('125,000,000');
  const remove = await page.locator('#sponsorGrid .sponsor-row .del-btn').boundingBox();
  expect(remove.x + remove.width).toBeLessThanOrEqual(page.viewportSize().width);
  expect(await documentOverflow(page)).toBeLessThanOrEqual(1);
});

/*
 * Room taken from the name field is not room. A field is drawn at a width of
 * its own whatever its column is told it may shrink to, so a column that
 * gives way entirely does not move the field out of the way: it leaves it
 * standing over whatever is beside it, which here is the figure that sponsor
 * is covering. Nothing above sees this: the page does not scroll sideways
 * and every control is still on the screen, so it is asserted directly.
 */
test("a sponsor's name is never drawn over the figure beside it", async ({ page }) => {
  await addSponsor(page, { code: 'AB', name: 'Example Family' });
  await tagLine(budgetRow(page, 0), ['AB']);

  const name = page.locator('#sponsorGrid .name-input');
  const figure = page.locator('#sponsorGrid .sp-amt');

  // An ordinary seven-figure line, and one drawn with as many digits and
  // separators as any currency and locale would ever put in front of a
  // reader. What a figure costs the row is its width, so the longest of them
  // is the case that has to hold.
  for (const unit of [1250000, 125000000, 18014398543952]) {
    await budgetRow(page, 0).unit.fill(String(unit));
    await expect.poll(async () => money(await figure.textContent())).toBe(unit);

    for (const width of [390, 375, 320]) {
      await page.setViewportSize({ width, height: 844 });
      const [over, under] = [await name.boundingBox(), await figure.boundingBox()];
      const shared = over.x < under.x + under.width && under.x < over.x + over.width
        && over.y < under.y + under.height && under.y < over.y + over.height;
      expect(shared, `the name and ${unit} share pixels at ${width}px`).toBe(false);
      expect(await documentOverflow(page)).toBeLessThanOrEqual(1);
    }
  }
});

test.describe('on a phone narrower still', () => {
  // 375px: the width of every iPhone up to the 8, and of an SE bought this
  // year. The list on the overview fits an owner's full name at 390 and not
  // here, which is why this one test moves the wall in.
  test.use({ viewport: { width: 375, height: 812 } });

  test("an owner's whole name does not push the date off the page", async ({ page }) => {
    await gotoTab(page, 'tasks');
    await addTask(page, { name: 'Confirm the count', owner: 'Grandma and Grandpa Example', due: '2030-05-01' });
    await gotoTab(page, 'overview');

    await expect(page.locator('#upNextList li')).toHaveCount(1);
    expect(await documentOverflow(page)).toBeLessThanOrEqual(1);
  });
});
