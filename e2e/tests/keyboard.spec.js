// @ts-check
/*
 * Keyboard reachability.
 *
 * The cost-by picker is appended to document.body, outside the container the
 * rest of the page lives in. That is a rendering convenience — it must escape
 * the table's scroll box — but it has a consequence: opening it from the
 * keyboard leaves focus on the button, and the next Tab goes to the cell after
 * it rather than into the popup, which is on the other side of the document.
 * The popup was unreachable by keyboard until focus was moved explicitly.
 *
 * Assigning a cost to a person is a data operation, not a display preference,
 * so these are not optional niceties. Each one is a regression that has
 * happened or could.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, addTask, gotoTab, openPlanner } = require('./helpers');

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  await addSponsor(page, { code: 'Ivy', name: 'Grace' });
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1, paid: 0 });
});

const byButton = (page) => page.locator('#budgetBody tr').first().locator('button.by-btn');
const popup = (page) => page.locator('.sp-pop');

test('the cost-by button sits in the row\'s tab order', async ({ page }) => {
  // Walking the row rather than jumping to the button: the claim is that a
  // keyboard user reaches it by tabbing through the line they are editing.
  await page.locator('#budgetBody tr').first().locator('td').nth(4).locator('input').focus();
  await page.keyboard.press('Tab');
  await expect(byButton(page)).toBeFocused();
});

test('opening the picker from the keyboard moves focus into it', async ({ page }) => {
  await byButton(page).focus();
  await expect(byButton(page)).toHaveAttribute('aria-expanded', 'false');

  await page.keyboard.press('Enter');

  await expect(popup(page)).toBeVisible();
  await expect(byButton(page)).toHaveAttribute('aria-expanded', 'true');
  // The whole point: focus is inside the popup, not left behind on the button
  // with the popup stranded at the far end of the document.
  await expect(popup(page).locator('input[type="checkbox"]').first()).toBeFocused();
});

test('the picker can be operated and dismissed entirely from the keyboard', async ({ page }) => {
  await byButton(page).focus();
  await page.keyboard.press('Enter');
  await expect(popup(page).locator('input[type="checkbox"]').first()).toBeFocused();

  // Space ticks the focused checkbox; the button behind the popup updates.
  await page.keyboard.press('Space');
  await expect(byButton(page)).toHaveText('Rose');

  // Arrow-free navigation between the options, then tick the second one too.
  await page.keyboard.press('Tab');
  await expect(popup(page).locator('input[type="checkbox"]').nth(1)).toBeFocused();
  await page.keyboard.press('Space');
  await expect(byButton(page)).toHaveText(/^Rose\s*\S\s*Ivy$/);

  await page.keyboard.press('Escape');

  await expect(popup(page)).toHaveCount(0);
  await expect(byButton(page)).toHaveAttribute('aria-expanded', 'false');
});

/*
 * REGRESSION GUARD. This failed when it was written, and the fix is subtle
 * enough to be worth spelling out so it is not undone by a tidy-up.
 *
 * closePop() hands focus back to the button it opened from. It used to remove
 * the popup first — and removing the focused element fires focusout
 * synchronously, so the removal re-entered closePop(false), sailed past the
 * guard because openPop was still set, and nulled openBtn before the outer
 * call reached focus(). Focus landed on <body>, so the next Tab restarted at
 * the top of the page. For someone assigning costs line by line, that is the
 * whole journey lost on every Escape.
 *
 * The fix is ordering: clear openPop and openBtn *before* removing the node,
 * so the re-entrant call stops at the guard.
 */
test('Escape hands focus back to the button the picker was opened from', async ({ page }) => {
  await byButton(page).focus();
  await page.keyboard.press('Enter');
  await expect(popup(page).locator('input[type="checkbox"]').first()).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(popup(page)).toHaveCount(0);
  await expect(byButton(page)).toBeFocused();
});

test('tabbing past the last option dismisses the picker rather than stranding it', async ({ page }) => {
  await byButton(page).focus();
  await page.keyboard.press('Enter');
  await expect(popup(page)).toBeVisible();

  // Two sponsors, so two checkboxes; a third Tab leaves the popup entirely.
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');

  await expect(popup(page)).toHaveCount(0);
  await expect(byButton(page)).toHaveAttribute('aria-expanded', 'false');
});

test('Escape inside the picker does not also collapse anything behind it', async ({ page }) => {
  await byButton(page).focus();
  await page.keyboard.press('Enter');
  await page.keyboard.press('Escape');

  await expect(popup(page)).toHaveCount(0);
  // Still on the budget tab, with the row intact: the Escape was consumed by
  // the popup and did not leak out to the rest of the page.
  await expect(page.locator('#panel-budget')).toBeVisible();
  await expect(page.locator('#budgetBody tr')).toHaveCount(1);
});

test('the tab strip is a tab widget, not three links that need tabbing through', async ({ page }) => {
  // Roving tabindex: only the selected tab is in the tab order, and the arrow
  // keys move between them. Without this, reaching the third panel means
  // walking the whole of the second.
  await page.locator('#tab-budget').focus();
  await expect(page.locator('#tab-budget')).toHaveAttribute('tabindex', '0');
  await expect(page.locator('#tab-overview')).toHaveAttribute('tabindex', '-1');

  await page.keyboard.press('ArrowRight');
  await expect(page.locator('#tab-tasks')).toBeFocused();
  await expect(page.locator('#panel-tasks')).toBeVisible();

  await page.keyboard.press('ArrowRight');
  // Wraps rather than stopping dead at the end.
  await expect(page.locator('#tab-overview')).toBeFocused();
  await expect(page.locator('#panel-overview')).toBeVisible();

  await page.keyboard.press('End');
  await expect(page.locator('#tab-tasks')).toBeFocused();
  await page.keyboard.press('Home');
  await expect(page.locator('#tab-overview')).toBeFocused();
});

test('every delete button says what it deletes', async ({ page }) => {
  // "×" is a fine mark to look at and a useless one to hear.
  await gotoTab(page, 'overview');
  await page.locator('#addWatch').click();

  await expect(page.locator('#watchList .del-btn')).toHaveAttribute('aria-label', 'Remove note');
  await gotoTab(page, 'budget');
  await expect(page.locator('#budgetBody .del-btn')).toHaveAttribute('aria-label', 'Remove budget line');
  await expect(page.locator('#sponsorGrid .del-btn').first()).toHaveAttribute('aria-label', 'Remove sponsor');
});

/*
 * The same point, for the fields between the delete buttons.
 *
 * A <th> names a cell only while the table is being read as a table. Tabbing
 * along a line - which is how this grid is filled in - announces the control
 * and nothing else, so three money spinners in a row were "spin button 2500,
 * spin button 1, spin button 500" with nothing saying which was the unit
 * price and which the amount paid. On a phone it is worse: the header row is
 * not drawn at all there.
 */
const gridNames = (page, sel) => page.evaluate(
  (s) => Array.from(document.querySelectorAll(s)).map((el) => el.getAttribute('aria-label')),
  `${sel} input, ${sel} select, ${sel} textarea`,
);

test('every field in the grid says which column it is in', async ({ page }) => {
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Book the band', owner: 'Ada', due: '2030-05-01' });
  expect(await gridNames(page, '#tasksBody')).toEqual(['Task', 'Owner', 'Due date', 'Status']);

  await gotoTab(page, 'budget');
  expect(await gridNames(page, '#budgetBody')).toEqual(['Item', 'Unit', 'Qty', 'Paid', 'Remarks']);

  // And the button in the middle of the budget row, whose name was the code
  // alone: "Rose" says who, never what about them.
  await expect(byButton(page)).toHaveAttribute('aria-label', 'Cost by: Unassigned');

  // The phone layout, where the headings are gone and the name on the field
  // is the only one there is.
  await page.setViewportSize({ width: 375, height: 720 });
  await expect(page.locator('#budgetTable thead')).toBeHidden();
  expect(await gridNames(page, '#budgetBody')).toEqual(['Item', 'Unit', 'Qty', 'Paid', 'Remarks']);
});
