// @ts-check
/*
 * Tasks: add, set a status, filter down to what is still open.
 *
 * The tasks tab and the overview are two views of one list, so the progress
 * gauge and the "up next" panel are checked alongside the table rather than
 * separately — the bug worth catching is them disagreeing.
 */
const { test, expect } = require('@playwright/test');
const { addTask, gotoTab, openPlanner } = require('./helpers');

const rows = (page) => page.locator('#tasksBody tr:not(:has(td.empty-cell))');

test.beforeEach(async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'tasks');
});

test('a new task starts not-started and empty', async ({ page }) => {
  await page.locator('#addTaskRow').click();

  await expect(rows(page)).toHaveCount(1);
  const row = rows(page).first();
  await expect(row.locator('td').nth(0).locator('input')).toHaveValue('');
  await expect(row.locator('select.status-select')).toHaveValue('not-started');
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);
});

test('statuses drive the progress gauge', async ({ page }) => {
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada' });
  await addTask(page, { name: 'Send invitations', owner: 'Grace' });

  await expect(page.locator('#taskBarPct')).toHaveText('0%');

  await rows(page).nth(1).locator('select.status-select').selectOption('done');
  await expect(page.locator('#taskBarPct')).toHaveText('50%');
  await expect(page.locator('#statTasks')).toHaveText('1 of 2 done');
  await expect(page.locator('#taskBarFill')).toHaveAttribute('style', /width:\s*50%/);

  await rows(page).nth(0).locator('select.status-select').selectOption('in-progress');
  // In progress is not done: the gauge must not flatter the plan.
  await expect(page.locator('#taskBarPct')).toHaveText('50%');

  await rows(page).nth(0).locator('select.status-select').selectOption('done');
  await expect(page.locator('#taskBarPct')).toHaveText('100%');
  await expect(page.locator('#statTasks')).toHaveText('2 of 2 done');
});

test('the filter pills narrow the table to one status', async ({ page }) => {
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada', status: 'in-progress' });
  await addTask(page, { name: 'Send invitations', owner: 'Grace' });
  await addTask(page, { name: 'Book the photographer', owner: 'Linus', status: 'done' });

  await expect(rows(page)).toHaveCount(3);

  await page.locator('#taskFilters .pill[data-filter="done"]').click();
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first().locator('td').nth(0).locator('input')).toHaveValue('Book the photographer');
  await expect(page.locator('#taskFilters .pill[data-filter="done"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#taskFilters .pill[data-filter="all"]')).toHaveAttribute('aria-pressed', 'false');

  await page.locator('#taskFilters .pill[data-filter="in-progress"]').click();
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first().locator('td').nth(0).locator('input')).toHaveValue('Confirm final guest count');

  await page.locator('#taskFilters .pill[data-filter="all"]').click();
  await expect(rows(page)).toHaveCount(3);
});

test('a filter that matches nothing says so, and says it differently from an empty list', async ({ page }) => {
  await addTask(page, { name: 'Send invitations', owner: 'Grace', status: 'done' });

  await page.locator('#taskFilters .pill[data-filter="not-started"]').click();
  await expect(rows(page)).toHaveCount(0);
  await expect(page.locator('#tasksBody .empty-cell')).toHaveText('No tasks with that status.');
});

test('changing a status while filtered removes the row from view', async ({ page }) => {
  await addTask(page, { name: 'Send invitations', owner: 'Grace' });
  await addTask(page, { name: 'Order the cake', owner: 'Ada' });

  await page.locator('#taskFilters .pill[data-filter="not-started"]').click();
  await expect(rows(page)).toHaveCount(2);

  await rows(page).first().locator('select.status-select').selectOption('done');
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first().locator('td').nth(0).locator('input')).toHaveValue('Order the cake');
});

test('adding a task while filtered shows it rather than filing it out of sight', async ({ page }) => {
  await addTask(page, { name: 'Send invitations', owner: 'Grace', status: 'done' });
  await page.locator('#taskFilters .pill[data-filter="done"]').click();
  await expect(rows(page)).toHaveCount(1);

  // A new task is not-started, so under the "Done" filter it would vanish the
  // moment it was created. The filter resets instead.
  await page.locator('#addTaskRow').click();
  await expect(page.locator('#taskFilters .pill[data-filter="all"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(rows(page)).toHaveCount(2);
});

test('"up next" lists the open tasks, soonest first', async ({ page }) => {
  await addTask(page, { name: 'Send invitations', owner: 'Grace', due: '2030-03-01' });
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada', due: '2030-01-15' });
  await addTask(page, { name: 'Book the photographer', owner: 'Linus', status: 'done' });
  await addTask(page, { name: 'Order the cake', owner: 'Ada' });

  await gotoTab(page, 'overview');
  await expect(page.locator('#upNextEmpty')).toBeHidden();

  const items = page.locator('#upNextList li');
  await expect(items).toHaveCount(3);
  // Dated tasks first in date order; the undated one brings up the rear rather
  // than jumping the queue. Nothing that is already done appears at all.
  await expect(items.nth(0)).toContainText('Confirm final guest count');
  await expect(items.nth(0).locator('.who')).toHaveText('Ada');
  await expect(items.nth(0).locator('.due')).toHaveText('2030-01-15');
  await expect(items.nth(1)).toContainText('Send invitations');
  await expect(items.nth(2)).toContainText('Order the cake');
  await expect(page.locator('#upNextList')).not.toContainText('Book the photographer');
});

test('removing the last task puts the start screen back', async ({ page }) => {
  await addTask(page, { name: 'Send invitations', owner: 'Grace' });
  await expect(page.locator('body')).not.toHaveClass(/is-empty/);

  await rows(page).first().getByRole('button', { name: 'Remove task' }).click();
  await expect(rows(page)).toHaveCount(0);
  await expect(page.locator('body')).toHaveClass(/is-empty/);
});
