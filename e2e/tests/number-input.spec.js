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

  // A million and a half, in the field as well as in the cell it feeds: a
  // left field spells the money, so the reading is legible in both of them.
  await expect(row.unit).toHaveValue('€1,500,000.00');
  await expect(row.committed).toHaveText('€1,500,000');
});

test('a decimal comma is a decimal, not two digits more', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Catering', qty: 2 });
  await retype(page, row.unit, '45,50');

  // Forty-five fifty, not four and a half thousand. The cents are why the
  // field keeps the currency's own decimals instead of the whole units the
  // committed cell rounds to.
  await expect(row.unit).toHaveValue('€45.50');
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

    // en-US grouping and the euro, because that is the instance's locale and
    // currency in every one of the three languages. The language moves the
    // words above the column, never the spelling of the money in it.
    await expect(row.unit).toHaveValue('€1,500.25');
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

  // And it is still there when the field is opened to correct it. A marked
  // field is the one place the stored figure is not put back on the way in:
  // it would take the typo away before the reader had seen what they typed.
  await row.unit.click();
  await expect(row.unit).toHaveValue('12,34,56');
  await row.unit.blur();

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
  await expect(row.unit).toHaveValue('€1.50');
});

test('and on a plan priced in a locale that groups with points, the same figure is a thousand and a half', async ({ page }, testInfo) => {
  await page.goto(testInfo.config.metadata.altBaseURL + '/');
  await expect(page.locator('body')).toHaveClass(/is-empty/);
  await gotoTab(page, 'budget');

  const row = await addBudgetLine(page, { item: 'Zaalhuur', qty: 1 });
  await retype(page, row.unit, '1.500');

  // nl-NL formatting on this instance, so the same point is back in both the
  // field and the cell it feeds. Matched rather than spelled out: that locale
  // puts a non-breaking space after the symbol, and which space ICU reaches
  // for is not what this test is about.
  await expect(row.unit).toHaveValue(/^€.1\.500,00$/);
  await expect(row.committed).toHaveText(/1\.500/);
});

/*
 * ---------- What a field says when nobody is in it ----------
 *
 * The two figures a line is typed with and the two it computes are the same
 * money, and one row spelled them two ways: the computed cells carried the
 * currency and the fields beside them were bare digits, so a line priced at
 * 25000 read "25000 … Rp 25.000" across a single row. A field carries the
 * currency once it is left, and gives back a figure to type over once it is
 * opened.
 */
test('every figure in a line is spelled the same money', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  // fill() leaves the field it wrote in only because the next one takes the
  // caret, so the last of them is left by hand.
  await row.paid.blur();

  await expect(row.unit).toHaveValue('€2,500.00');
  await expect(row.paid).toHaveValue('€500.00');
  await expect(row.committed).toHaveText('€2,500');
  await expect(row.outstanding).toHaveText('€2,000');
});

test('a money field opens as a figure to type over, and the figure does not drift', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Catering', qty: 1 });
  await retype(page, row.unit, '2500,50');
  await expect(row.unit).toHaveValue('€2,500.50');

  // Opened and left three times with nothing typed in between. What the field
  // gives back has to be a figure the same reader would put back: a spelling
  // it cannot read again would lose the cents on the first person who clicked
  // into the cell and out of it.
  for (let i = 0; i < 3; i += 1) {
    await row.unit.click();
    await expect(row.unit).toHaveValue('2500.5');
    await row.unit.blur();
    await expect(row.unit).toHaveValue('€2,500.50');
  }
  // The committed cell rounds to whole units, which is what a column of
  // totals is for; the field holds the figure that was typed.
  await expect(row.committed).toHaveText('€2,501');

  // And typing over what was opened is the ordinary way to correct a figure.
  await retype(page, row.unit, '3000');
  await expect(row.unit).toHaveValue('€3,000.00');
  await expect(row.committed).toHaveText('€3,000');
});

test('a figure typed over a figure replaces it rather than joining onto it', async ({ page }) => {
  const row = await addBudgetLine(page, { item: 'Photographer', unit: 2000, qty: 1 });

  // Select what the field holds and type something else, which is what
  // correcting a price is. The field puts the bare figure back on the way in,
  // and writing to a field collapses the selection it was holding: without
  // that selection put back, 2000 corrected to 2100 became 20002100 and the
  // line went in at ten thousand times the quote.
  await row.unit.fill('2100');
  await row.unit.blur();

  await expect(row.unit).toHaveValue('€2,100.00');
  await expect(row.committed).toHaveText('€2,100');
});

test('the quantity beside it is not money and gains no currency', async ({ page }) => {
  // Three decimals and no currency mark: a third of a case is an ordinary
  // quantity, and the field it is typed in shares its code with the two money
  // fields on either side of it.
  const row = await addBudgetLine(page, { item: 'Wine', unit: 12, qty: 1 });
  await retype(page, row.qty, '2,5');

  await expect(row.qty).toHaveValue('2.5');
  await expect(row.committed).toHaveText('€30');
});
