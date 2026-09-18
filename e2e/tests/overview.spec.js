// @ts-check
/*
 * The overview's two pictures: the run-up from today to the day, and the money
 * bar. Both are drawings of facts the page also states in words, so what is
 * checked here is that the drawing agrees with the facts - a mark in the wrong
 * place is worse than no mark.
 *
 * The server under test is configured for 12 June 2030 at +09:00.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addTask, gotoTab } = require('./helpers');

/** The left offset of each mark on the scale, as a percentage. */
const marks = (page) => page.locator('#runupMarks .runup-mark').evaluateAll(
  (els) => els.map((el) => ({ at: parseFloat(el.style.left), late: el.classList.contains('late'), label: el.textContent })),
);

test('open tasks are pinned on the run-up where their dates fall, and late ones are counted', async ({ page }) => {
  // Noon on 3 June where the event is: nine days to go.
  await page.clock.setFixedTime(new Date('2030-06-03T03:00:00Z'));
  await page.goto('/');
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Order the cake', due: '2030-06-06' });          // 3 of 9 days along
  await addTask(page, { name: 'Confirm the band', due: '2030-06-09' });        // 6 of 9
  await addTask(page, { name: 'Pay the florist', due: '2030-05-20' });         // late
  await addTask(page, { name: 'Sign the contract', due: '2030-06-05', status: 'done' });
  await addTask(page, { name: 'Think about speeches' });                       // no date
  await gotoTab(page, 'overview');

  await expect(page.locator('#daysNum')).toHaveText('9');
  const pinned = await marks(page);
  // Done and undated tasks are not on it. The late one is, at today.
  expect(pinned.map((m) => Math.round(m.at))).toEqual([0, 33, 67]);
  expect(pinned.map((m) => m.late)).toEqual([true, false, false]);
  expect(pinned[1].label).toBe('Order the cake');
  await expect(page.locator('#runupFrom')).toHaveText('today – 1 overdue');
  await expect(page.locator('#upNextList .due.late')).toHaveText(['2030-05-20']);

  // Finishing the late one takes it off the scale and out of the count.
  await gotoTab(page, 'tasks');
  // The third row added; an input's typed value is not an attribute to select on.
  await page.locator('#tasksBody tr').nth(2).locator('select.status-select').selectOption('done');
  await gotoTab(page, 'overview');
  expect((await marks(page)).map((m) => Math.round(m.at))).toEqual([33, 67]);
  await expect(page.locator('#runupFrom')).toHaveText('today');
});

test('with no distance left to draw there is no scale, and the date still shows', async ({ page }) => {
  // The day itself where the event is.
  await page.clock.setFixedTime(new Date('2030-06-12T03:00:00Z'));
  await page.goto('/');
  await expect(page.locator('#runup')).toHaveClass(/no-scale/);
  await expect(page.locator('#runupScale')).toBeHidden();
  await expect(page.locator('#daysNum')).toHaveText('0');
  await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
});

test('names on the run-up never overlap, on a phone either', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.clock.setFixedTime(new Date('2030-01-04T03:00:00Z'));
  await page.goto('/');
  await gotoTab(page, 'tasks');
  for (const [name, due] of [['Choose the menu', '2030-02-12'], ['Send invitations', '2030-03-30'],
    ['Confirm the band', '2030-05-02'], ['Order the cake', '2030-05-20']]) {
    await addTask(page, { name, due });
  }
  await gotoTab(page, 'overview');
  await expect(page.locator('#runupMarks .runup-mark')).toHaveCount(4);
  const boxes = await page.locator('.runup-label').evaluateAll((els) => els.map((el) => {
    const r = el.getBoundingClientRect();
    return { left: r.left, right: r.right };
  }));
  expect(boxes.length).toBeGreaterThan(0);
  for (let i = 1; i < boxes.length; i++) {
    expect(boxes[i].left, `label ${i} starts after label ${i - 1} ends`).toBeGreaterThanOrEqual(boxes[i - 1].right);
  }
  const scale = await page.locator('#runupScale').evaluate((el) => el.getBoundingClientRect().right);
  expect(boxes[boxes.length - 1].right, 'the last name stays inside the scale').toBeLessThanOrEqual(scale + 8);
});

test('the money bar draws paid and owed against the ceiling', async ({ page }) => {
  await page.goto('/');
  await gotoTab(page, 'budget');
  await page.fill('#ceilingInput', '10000');
  await addBudgetLine(page, { item: 'Venue', unit: 2500, qty: 1, paid: 500 });
  await gotoTab(page, 'overview');

  const width = (id) => page.locator(id).evaluate((el) => parseFloat(el.style.width));
  expect(await width('#moneyBarPaid')).toBeCloseTo(5, 1);      // 500 of 10,000
  expect(await width('#moneyBarOwed')).toBeCloseTo(20, 1);     // 2,000 of 10,000
  const ceiling = page.locator('#moneyBarCeiling');
  await expect(ceiling).toBeVisible();
  expect(await ceiling.evaluate((el) => parseFloat(el.style.left))).toBeCloseTo(100, 1);
  await expect(ceiling).not.toHaveClass(/over/);

  // Past the ceiling the bar is the commitment, and the ceiling a mark inside it.
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Catering', unit: 17500, qty: 1, paid: 0 });
  await gotoTab(page, 'overview');
  expect(await width('#moneyBarPaid')).toBeCloseTo(2.5, 1);    // 500 of 20,000
  expect(await width('#moneyBarOwed')).toBeCloseTo(97.5, 1);
  expect(await ceiling.evaluate((el) => parseFloat(el.style.left))).toBeCloseTo(50, 1);
  await expect(ceiling).toHaveClass(/over/);

  // No ceiling, no mark.
  await gotoTab(page, 'budget');
  await page.fill('#ceilingInput', '0');
  await gotoTab(page, 'overview');
  await expect(ceiling).toBeHidden();
});
