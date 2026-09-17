// @ts-check
/*
 * The first thing anyone sees: an empty planner, with demo data off.
 *
 * The Go tests prove the configured event name is in the served HTML. They
 * cannot prove the page read it, so the shell assertions here close that loop
 * from the other end.
 */
const { test, expect } = require('@playwright/test');
const { openPlanner } = require('./helpers');

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
});

test('the shell renders the configuration the server injected', async ({ page }) => {
  await expect(page).toHaveTitle('Rehearsal Dinner (e2e)');
  await expect(page.locator('.masthead h1')).toHaveText('Rehearsal Dinner (e2e)');
  await expect(page.locator('.masthead .tagline')).toHaveText('Synthetic fixture data');

  // The countdown is computed in the browser from an RFC3339 instant, pinned
  // to UTC so it does not read a day out depending on where the viewer is.
  await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
  await expect(page.locator('#daysNum')).toHaveText(/^\d+$/);
  await expect(page.locator('#daysLabel')).toHaveText('days to go');

  // No secondary currency configured, so its readouts are removed rather than
  // left showing a dash nobody can act on. Asserted on computed display rather
  // than on visibility: these live inside panels that are hidden anyway while
  // the planner is empty, so `toBeHidden` would pass without proving anything.
  await expect(page.locator('#mCommittedEur')).toHaveCSS('display', 'none');
  await expect(page.locator('#mOutstandingEur')).toHaveCSS('display', 'none');

  // Currency labels come from the same config block.
  await page.locator('#tab-budget').click();
  await expect(page.locator('#panel-budget .cur-code').first()).toHaveText('EUR');
  await expect(page.locator('#rateField')).toBeHidden();
});

test('an empty planner shows the start screen instead of a grid of dashes', async ({ page }) => {
  await expect(page.locator('.first-run')).toBeVisible();
  await expect(page.locator('.first-run h2')).toHaveText('Nothing in the ledger yet');
  await expect(page.locator('.when-data')).toBeHidden();

  await expect(page.locator('.first-run .add-row-btn')).toHaveText([
    'Set the ceiling',
    'Add a sponsor',
    'Add a budget line',
  ]);
});

/*
 * Each button does the thing it names rather than explaining where to find it.
 * That is the claim; these three check it, including where the focus lands,
 * because a button that switches tabs and then leaves you looking for the
 * field has not done the thing it named.
 */

test('"Set the ceiling" opens the budget tab with the ceiling field focused', async ({ page }) => {
  await page.locator('#startCeiling').click();

  await expect(page.locator('#panel-budget')).toBeVisible();
  await expect(page.locator('#tab-budget')).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('#ceilingInput')).toBeFocused();

  // Typing straight away works because the field is focused and selected.
  await page.keyboard.type('12000');
  await expect(page.locator('#ceilingInput')).toHaveValue('12000');
  await expect(page.locator('#statBudgetSub')).not.toHaveText(/No ceiling set/);
});

test('"Add a sponsor" opens the budget tab with a new sponsor row focused', async ({ page }) => {
  await page.locator('#startSponsor').click();

  await expect(page.locator('#panel-budget')).toBeVisible();
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(1);
  await expect(page.locator('#sponsorGrid .code-input').first()).toBeFocused();

  await page.keyboard.type('Rose');
  await expect(page.locator('#sponsorGrid .code-input').first()).toHaveValue('Rose');

  // A sponsor is a record, so the ledger is no longer empty and the start
  // screen stands down — even though the overview itself has not changed.
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
});

test('"Add a budget line" opens the budget tab with the new line focused', async ({ page }) => {
  await page.locator('#startLine').click();

  await expect(page.locator('#panel-budget')).toBeVisible();
  await expect(page.locator('#budgetBody tr')).toHaveCount(1);
  await expect(page.locator('#budgetBody textarea').first()).toBeFocused();

  await page.keyboard.type('Venue deposit');
  await expect(page.locator('#budgetBody tr').first().locator('td').nth(0).locator('textarea')).toHaveValue(
    'Venue deposit',
  );
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
});

test('the empty budget and task grids say so rather than showing a bare header', async ({ page }) => {
  await page.locator('#tab-budget').click();
  await expect(page.locator('#budgetBody .empty-cell')).toHaveText('No budget lines yet. Add the first one below.');

  await page.locator('#tab-tasks').click();
  await expect(page.locator('#tasksBody .empty-cell')).toHaveText('No tasks yet. Add the first one below.');
});
