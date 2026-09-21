// @ts-check
/*
 * Who's covering what.
 *
 * This is the part a spreadsheet is bad at and the reason the project exists,
 * so the arithmetic gets checked from both directions: the per-sponsor totals
 * in the editor, and the split list under the table — in both of its modes.
 *
 * The invariant that matters in both modes: the split adds up to the committed
 * total. Money attributed to nobody still has to appear somewhere.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, expectFigures, gotoTab, money, openPlanner, readSplit, tagLine } = require('./helpers');

// Round numbers, so a wrong split is obvious rather than arguable:
//   Venue 2000 -> Rose
//   Catering 2000 -> Rose + Ivy
//   Invitations 1000 -> nobody
// Total 5000.
async function fixture(page) {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('10000');

  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  await addSponsor(page, { code: 'Ivy', name: 'Grace' });

  const venue = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1, paid: 0 });
  const catering = await addBudgetLine(page, { item: 'Catering', unit: 50, qty: 40, paid: 0 });
  const invitations = await addBudgetLine(page, { item: 'Printed invitations', unit: 25, qty: 40, paid: 0 });

  await tagLine(venue, ['Rose']);
  await tagLine(catering, ['Rose', 'Ivy']);

  return { venue, catering, invitations };
}

test('an untagged line reads as unassigned', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  await expect(row.by).toHaveText('Unassigned');
  await expect(row.by).toHaveClass(/none/);
  expect(await readSplit(page)).toEqual([{ label: 'Unassigned', amount: 2000, pct: '100%' }]);
});

test('the picker offers nothing to pick until there is a sponsor', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  await row.by.click();
  const pop = page.locator('.sp-pop');
  await expect(pop).toBeVisible();
  await expect(pop.locator('.pop-note').first()).toContainText('No sponsors yet');
  await expect(pop.locator('input[type="checkbox"]')).toHaveCount(0);
});

test('tagging a line attributes it, and a shared line is marked as shared', async ({ page }) => {
  const { venue, catering } = await fixture(page);

  await expect(venue.by).toHaveText('Rose');
  await expect(venue.by).not.toHaveClass(/none/);
  // Two callsigns on one line: the button lists both. Matched loosely across
  // the separator, which is a typographic choice rather than a behaviour.
  await expect(catering.by).toHaveText(/^Rose\s*\S\s*Ivy$/);

  const split = await readSplit(page);
  expect(split).toEqual([
    { label: 'Rose', amount: 2000, pct: '40%' },
    { label: 'Rose + Ivy (shared)', amount: 2000, pct: '40%' },
    { label: 'Unassigned', amount: 1000, pct: '20%' },
  ]);

  // The whole committed total is accounted for, including the part nobody has
  // agreed to cover.
  expect(split.reduce((sum, r) => sum + r.amount, 0)).toBe(5000);
  await expectFigures(page, { committed: 5000, paid: 0, outstanding: 5000, forecast: 5000 });
});

test('split evenly divides shared lines between their sponsors', async ({ page }) => {
  await fixture(page);

  await page.locator('#splitEvenly').check();

  // Rose: 2000 outright + half of the 2000 shared line.
  // Ivy:  half of the shared line.
  // The 1000 nobody claimed stays unassigned rather than being spread around.
  const split = await readSplit(page);
  expect(split.map((r) => ({ amount: r.amount, pct: r.pct }))).toEqual([
    { amount: 3000, pct: '60%' },
    { amount: 1000, pct: '20%' },
    { amount: 1000, pct: '20%' },
  ]);
  // Callsign and who it is, so a split list is readable by someone who does
  // not have the sponsor table memorised.
  expect(split[0].label).toMatch(/^Rose\b.*\bAda$/);
  expect(split[1].label).toMatch(/^Ivy\b.*\bGrace$/);
  expect(split[2].label).toBe('Unassigned');
  expect(split.reduce((sum, r) => sum + r.amount, 0)).toBe(5000);

  // Toggling back restores the by-line grouping, with the same grand total.
  await page.locator('#splitEvenly').uncheck();
  const back = await readSplit(page);
  expect(back.map((r) => r.label)).toEqual(['Rose', 'Rose + Ivy (shared)', 'Unassigned']);
  expect(back.reduce((sum, r) => sum + r.amount, 0)).toBe(5000);
});

test("each sponsor's own total always splits shared lines, whatever the toggle says", async ({ page }) => {
  await fixture(page);

  // The figure beside the sponsor is that person's share, which is a fact
  // about the data rather than a display preference — so it does not move when
  // the split list changes mode.
  const amounts = () =>
    page.locator('#sponsorGrid .sponsor-row .sp-amt').allTextContents().then((t) => t.map(money));

  expect(await amounts()).toEqual([3000, 1000]);
  await page.locator('#splitEvenly').check();
  expect(await amounts()).toEqual([3000, 1000]);
});

test('renaming a sponsor follows through to the table and the split', async ({ page }) => {
  const { venue } = await fixture(page);

  await page.locator('#sponsorGrid .sponsor-row').first().locator('.code-input').fill('Rosa');

  await expect(venue.by).toHaveText('Rosa');
  expect((await readSplit(page)).map((r) => r.label)).toEqual(['Rosa', 'Rosa + Ivy (shared)', 'Unassigned']);

  await page.locator('#splitEvenly').check();
  expect((await readSplit(page))[0].label).toContain('Rosa');
});

test('removing a sponsor releases the lines it was covering', async ({ page }) => {
  const { venue, catering } = await fixture(page);

  // Nobody is told that a split has moved, and there is no way back to the
  // attributions, so a name that is on lines is asked about first, with the
  // number of lines, which is what makes it worth a second thought.
  const asked = [];
  page.on('dialog', (dialog) => { asked.push(dialog.message()); dialog.accept().catch(() => {}); });
  await page.locator('#sponsorGrid .sponsor-row').first().getByRole('button', { name: 'Remove sponsor' }).click();
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(1);
  expect(asked).toEqual([
    'Lines on this sponsor: 2. They lose the attribution and the split changes. Remove the sponsor?',
  ]);

  // Rose is gone, so the line only she covered falls back to unassigned and
  // the shared line becomes Ivy's alone — no dangling reference, and no money
  // lost on the way.
  await expect(venue.by).toHaveText('Unassigned');
  await expect(catering.by).toHaveText('Ivy');

  const split = await readSplit(page);
  expect(split).toEqual([
    { label: 'Unassigned', amount: 3000, pct: '60%' },
    { label: 'Ivy', amount: 2000, pct: '40%' },
  ]);
  expect(split.reduce((sum, r) => sum + r.amount, 0)).toBe(5000);
});

test('a sponsor nobody is tagged with goes in one click, and declining keeps the split', async ({ page }) => {
  const { venue } = await fixture(page);
  await addSponsor(page, { code: 'Clover', name: 'Barbara' });

  // Recorded and dismissed: a dialog that should not appear is a failure, and
  // one that does appear must leave everything as it was.
  const asked = [];
  page.on('dialog', (dialog) => { asked.push(dialog.message()); dialog.dismiss().catch(() => {}); });

  // Nothing is attributed to Clover, so nothing is lost and nothing is asked.
  await page.locator('#sponsorGrid .sponsor-row').nth(2).getByRole('button', { name: 'Remove sponsor' }).click();
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(2);
  expect(asked, 'a name on no lines is still one click').toEqual([]);

  // Rose is on two. Saying no leaves her there, with the lines still hers.
  await page.locator('#sponsorGrid .sponsor-row').first().getByRole('button', { name: 'Remove sponsor' }).click();
  await expect.poll(() => asked.length, { message: 'removing a tagged sponsor must ask' }).toBe(1);
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(2);
  await expect(venue.by).toHaveText('Rose');
  expect((await readSplit(page)).map((r) => r.label)).toEqual(['Rose', 'Rose + Ivy (shared)', 'Unassigned']);
});

test('untagging a line through the picker puts it back to unassigned', async ({ page }) => {
  const { venue } = await fixture(page);

  await venue.by.click();
  const pop = page.locator('.sp-pop');
  await pop.locator('label').filter({ hasText: 'Rose' }).locator('input[type="checkbox"]').uncheck();
  await expect(venue.by).toHaveText('Unassigned');
  await page.keyboard.press('Escape');
  await expect(pop).toHaveCount(0);

  expect((await readSplit(page)).find((r) => r.label === 'Unassigned').amount).toBe(3000);
});
