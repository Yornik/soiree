// @ts-check
/*
 * Typing a figure.
 *
 * Every field that takes one used to be an <input type="number">, which reads
 * what was typed by the language of the browser's own menus rather than the
 * page's: "45,50" arrived as 4550 on English menus, and "1.500.000" arrived
 * as 1.5 on all of them. The field went on showing what was typed and nothing
 * was marked wrong, so the only cue was the committed figure beside it.
 *
 * Everything here types with the keyboard rather than fill(), which writes
 * straight into the field and walks past the reading these tests are about.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, gotoTab, openPlanner } = require('./helpers');

/** Types into a field the way a person does, and leaves it. */
async function retype(page, locator, text) {
  await locator.click();
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.type(text);
  await locator.blur();
}

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
});

test('thousands grouped with points are thousands, not a fraction', async ({ page }) => {
  // A price in a currency where everything is in the millions, grouped the
  // way both languages of the interface group thousands. A number field read
  // it as 1.5, and the line went in at a millionth of the quote.
  const row = await addBudgetLine(page, { item: 'Venue deposit', qty: 1 });
  await retype(page, row.unit, '1.500.000');

  await expect(row.unit).toHaveValue('1500000');
  await expect(row.committed).toHaveText('€1,500,000');
});

test('a decimal comma is a decimal, not two digits more', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Catering', qty: 2 });
  await retype(page, row.unit, '45,50');

  await expect(row.unit).toHaveValue('45.5');
  await expect(row.committed).toHaveText('€91');
});

test('the ceiling reads the same way the grid does', async ({ page }) => {
  await addBudgetLine(page, { item: 'Venue deposit', unit: 1000, qty: 1 });
  await retype(page, page.locator('#ceilingInput'), '1.500.000');

  await expect(page.locator('#ceilingInput')).toHaveValue('1500000');
  await expect(page.locator('#sumHeadroom')).toHaveText('€1,499,000');
});

/*
 * The language of the page moves the words, not the arithmetic, and not the
 * reading of a typed figure either. This is the invariance the fix rests on:
 * one figure, typed identically, is one number in all three.
 */
for (const lang of ['en', 'nl', 'id']) {
  test(`a figure is read the same way with the interface in ${lang}`, async ({ page }) => {
    await page.goto(`/?lang=${lang}`);
    await expect(page.locator('body')).toHaveClass(/is-empty/);
    await gotoTab(page, 'budget');

    const row = await addBudgetLine(page, { item: 'Flowers', qty: 1 });
    await retype(page, row.unit, '1.500,25');

    await expect(row.unit).toHaveValue('1500.25');
    // en-US grouping, because that is the instance's locale in every one of
    // the three languages.
    await expect(row.committed).toHaveText('€1,500');
  });
}

test('a figure that cannot be read is said so, and the last one that could is kept', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Photographer', qty: 1 });
  // Two commas, neither of them grouping three digits: no reading of this is
  // a figure. A number field dropped both and made it 123456.
  await retype(page, row.unit, '12,34,56');

  await expect(row.unit).toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#dataMsg')).toHaveText(/not a figure this can read/);
  // Left as typed, because a figure with a typo in it is quicker to correct
  // than to type again, and the plan holds the last reading that made sense.
  await expect(row.unit).toHaveValue('12,34,56');
  await expect(row.committed).toHaveText('€12');

  // And it stops being said the moment the figure reads again.
  await retype(page, row.unit, '1234');
  await expect(row.unit).not.toHaveAttribute('aria-invalid', 'true');
  await expect(row.committed).toHaveText('€1,234');
});

test('a field that was marked stops being marked when it is redrawn', async ({ page }) => {
  await retype(page, page.locator('#ceilingInput'), '1.50.000');
  await expect(page.locator('#ceilingInput')).toHaveAttribute('aria-invalid', 'true');

  // The three settings fields outlive every render, unlike a grid cell, which
  // loses the mark with the row that is rebuilt around it. Reading the page in
  // another language redraws them from what is stored.
  await page.locator('#langSwitch [data-lang="nl"]').click();
  await expect(page.locator('#ceilingInput')).not.toHaveAttribute('aria-invalid', 'true');
  await expect(page.locator('#ceilingInput')).toHaveValue('1.5');
});

/*
 * One figure is left that structure cannot settle: a lone separator with
 * exactly three digits behind it. "1.500" is fifteen hundred to a reader who
 * groups with points and one and a half to a reader who does not, and the tie
 * goes to the locale the plan is configured with, which every figure on the
 * page is already spelled in and which is the same for everyone reading it.
 * Never to the browser's menus, which are neither.
 */
test('an ambiguous figure follows the locale the plan is priced in', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Printed invitations', qty: 1 });
  await retype(page, row.unit, '1.500');

  // en-US here, where a point is the decimal mark.
  await expect(row.unit).toHaveValue('1.5');
});

test('and on a plan priced in a locale that groups with points, the same figure is a thousand and a half', async ({ page }, testInfo) => {
  await page.goto(testInfo.config.metadata.altBaseURL + '/');
  await expect(page.locator('body')).toHaveClass(/is-empty/);
  await gotoTab(page, 'budget');

  const row = await addBudgetLine(page, { item: 'Zaalhuur', qty: 1 });
  await retype(page, row.unit, '1.500');

  await expect(row.unit).toHaveValue('1500');
  // nl-NL formatting on this instance, so the same point is back in the
  // rendered figure.
  await expect(row.committed).toHaveText(/1\.500/);
});
