// @ts-check
/*
 * Names written in a script that reads right to left.
 *
 * Nothing in the page sets a direction, so every field and every cell that
 * echoes one used to be laid out left to right whatever was typed into it: an
 * Arabic or Hebrew name was aligned to the wrong edge of its field, and the
 * punctuation that closes it was drawn at the end an Arabic reader starts
 * from. No figure is affected and nothing is stored differently; it is the
 * reading of a name.
 *
 * The stylesheet says it with `unicode-bidi: plaintext`, which is what
 * `dir="auto"` does: the first letter somebody types decides which way that
 * one field or that one cell runs. A name that starts with a Latin letter is
 * laid out exactly as it was, which the second half of each test here pins.
 */
const { test, expect } = require('@playwright/test');
const { addTask, gotoTab, openPlanner } = require('./helpers');

// "Confirm the guest count!", and its plain English twin. The exclamation
// mark is the whole point: it is a neutral character, so which end of the
// name it is drawn at is decided by the direction of the line, not by itself.
const RIGHT_TO_LEFT = 'تأكيد عدد الضيوف!';
const LEFT_TO_RIGHT = 'Confirm the guest count!';

/**
 * Where a cell's text is drawn: the first and last character, and the text's
 * own edges inside the box it sits in. A Range measures what the browser
 * actually laid out, which is the only thing worth asserting here.
 */
function laidOut(page, selector) {
  return page.locator(selector).first().evaluate((el) => {
    const text = el.firstChild;
    const at = (index) => {
      const range = document.createRange();
      range.setStart(text, index);
      range.setEnd(text, index + 1);
      return Math.round(range.getBoundingClientRect().left);
    };
    const whole = document.createRange();
    whole.selectNodeContents(el);
    const line = whole.getBoundingClientRect();
    const box = el.getBoundingClientRect();
    return {
      first: at(0),
      last: at(el.textContent.length - 1),
      textLeft: Math.round(line.left),
      textRight: Math.round(line.right),
      boxLeft: Math.round(box.left),
      boxRight: Math.round(box.right),
    };
  });
}

test('a right-to-left name is written out right to left where it is echoed', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'tasks');
  await addTask(page, { name: RIGHT_TO_LEFT, due: '2030-05-01' });
  await gotoTab(page, 'overview');

  const name = await laidOut(page, '#upNextList li > :first-child');

  // The name closes on the left, where an Arabic reader finishes reading it.
  // Laid out left to right, the "!" was drawn at the other end instead.
  expect(name.last, 'the name is still laid out left to right').toBeLessThan(name.first);

  // And it is set against the right-hand edge of its cell, the edge it
  // starts from, rather than the one it used to be pushed against.
  expect(name.textRight).toBeGreaterThan(name.boxLeft + (name.boxRight - name.boxLeft) / 2);
  expect(name.boxRight - name.textRight).toBeLessThanOrEqual(2);
});

test('a name in Latin letters is laid out exactly as it was', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'tasks');
  await addTask(page, { name: LEFT_TO_RIGHT, due: '2030-05-01' });
  await gotoTab(page, 'overview');

  const name = await laidOut(page, '#upNextList li > :first-child');
  expect(name.last).toBeGreaterThan(name.first);
  expect(name.textLeft - name.boxLeft).toBeLessThanOrEqual(2);
});

test('the fields a name is typed into take their direction from the name', async ({ page }) => {
  // A field cannot be measured the way a cell can, so this asserts the rule
  // that gives it the behaviour above: plaintext is dir="auto" said in CSS,
  // and it reaches every field somebody writes their own words into.
  await openPlanner(page);
  await gotoTab(page, 'tasks');
  await addTask(page, { name: RIGHT_TO_LEFT, owner: 'Ada', due: '2030-05-01' });
  await gotoTab(page, 'budget');
  await page.locator('#addBudgetRow').click();
  await page.locator('#addSponsor').click();

  const direction = (selector) => page.locator(selector).first()
    .evaluate((el) => window.getComputedStyle(el).unicodeBidi);

  for (const [what, selector] of [
    ['a task name', '#tasksBody tr td:first-child input'],
    ['a task owner', '#tasksBody tr td:nth-child(2) input'],
    ['a budget line', '#budgetBody td.item-cell textarea'],
    ['a remark on a line', '#budgetBody td.note-cell textarea'],
    ["a sponsor's name", '#sponsorGrid .name-input'],
  ]) {
    expect(await direction(selector), `${what} is fixed left to right`).toBe('plaintext');
  }
});
