// @ts-check
/*
 * The page in a forced palette: Windows High Contrast and the rest.
 *
 * In that mode the browser throws every author colour away and redraws the
 * page in the four or five colours the person picked. Anything the page says
 * with a background, a transparent border or a box-shadow therefore stops
 * saying it: the selected tab, the filter that is on, the theme in use and
 * the language in use all came out looking exactly like the ones beside them,
 * and the run-up's line and marks, the money bar, the gauges and the share
 * bars were drawn in the canvas colour on the canvas.
 *
 * No figure was ever lost, because every one of them is written out as text
 * as well. What was lost is which of a row of controls is the one that is on.
 *
 * Every assertion below compares one computed colour with another. The
 * palette belongs to the person, not to us, so there is no colour here that a
 * test is allowed to name.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, addTask, budgetRow, gotoTab, openPlanner, tagLine } = require('./helpers');

test.use({ forcedColors: 'active' });

/**
 * A computed colour as an "r,g,b" triple, with the alpha dropped: a 70% white
 * is the same white as the canvas it is drawn on, and is just as invisible
 * however the string is spelled.
 */
function colour(page, selector, property) {
  return page.locator(selector).first().evaluate((el, prop) => {
    const numbers = window.getComputedStyle(el)[prop].match(/\d+(\.\d+)?/g) || [];
    return numbers.slice(0, 3).join(',');
  }, property);
}

/** What the page is drawn on, which is what a graphic has to differ from. */
function paper(page) {
  return colour(page, 'body', 'backgroundColor');
}

test.beforeEach(async ({ page }) => {
  await openPlanner(page);

  // Chromium's forced-colors user agent sheet gives a hovered button a
  // Highlight border, and the pointer starts at 0,0, which is the first tab.
  // Left there, whichever control is under it looks selected and these tests
  // pass for the wrong reason.
  await page.mouse.move(600, 500);
});

test('the tab you are on, the filter that is on and the theme in use still look it', async ({ page }) => {
  // The tab you are on is an ink underline and the others a transparent one,
  // and a forced palette draws transparent in its own ink.
  const here = await colour(page, '#tab-overview', 'borderBottomColor');
  const elsewhere = await colour(page, '#tab-tasks', 'borderBottomColor');
  expect(here, 'every tab is underlined the same way').not.toBe(elsewhere);

  // The pills say which one is on by filling in, and a fill in a forced
  // palette is the canvas.
  await gotoTab(page, 'tasks');
  const behind = await paper(page);
  expect(await colour(page, '#taskFilters .pill.active', 'backgroundColor')).not.toBe(behind);
  expect(await colour(page, '.data-tools .filter-pills .pill.active', 'backgroundColor')).not.toBe(behind);

  // And the filter's name has to survive the fill. Chromium paints a plate in
  // the canvas colour behind every run of text in this mode, so text set to
  // the colour that goes with the fill lands on that plate and disappears:
  // "All" came out as a white block on the blue. Turning the forcing off for
  // the pill is what stops the plate being painted.
  const adjust = (selector) => page.locator(selector).first()
    .evaluate((el) => window.getComputedStyle(el).forcedColorAdjust);
  expect(await adjust('#taskFilters .pill.active')).toBe('none');

  // The language in use is marked with a box-shadow, which a forced palette
  // drops altogether.
  const flag = page.locator('.flag-btn[aria-pressed="true"]');
  expect(await flag.evaluate((el) => window.getComputedStyle(el).outlineStyle)).not.toBe('none');
});

test('the run-up, the money bar and the shares are still drawn', async ({ page }) => {
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await page.locator('#ceilingInput').fill('4000');
  // A second line nobody has taken on, so the split list holds both a share
  // and the remainder.
  await addBudgetLine(page, { item: 'Flowers', unit: 300, qty: 1 });
  await addSponsor(page, { code: 'AB', name: 'Example Family' });
  await tagLine(budgetRow(page, 0), ['AB']);
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm final guest count', due: '2030-05-01' });
  await gotoTab(page, 'overview');
  await page.mouse.move(600, 500);

  const behind = await paper(page);
  for (const [what, selector, property] of [
    ['the run-up line', '.runup-line', 'backgroundColor'],
    ['a mark on the run-up', '.runup-mark', 'backgroundColor'],
    ["today's mark", '.runup-today', 'backgroundColor'],
    ['the paid length of the money bar', '.moneybar-paid', 'backgroundColor'],
    ['the owed length of the money bar', '.moneybar-owed', 'backgroundColor'],
    ['the ceiling the money bar is measured against', '#moneyBarCeiling', 'backgroundColor'],
    ['a gauge', '#taskBarFill', 'backgroundColor'],
    ["a sponsor's share", '.split-list .share-fill:not(.unassigned)', 'backgroundColor'],
    ['the share nobody has taken on', '.split-list .share-fill.unassigned', 'backgroundColor'],
  ]) {
    expect(await colour(page, selector, property), `${what} is drawn in the canvas colour`).not.toBe(behind);
  }

  // Paid and owed are two lengths of one bar, so they have to be told apart
  // as well as seen.
  expect(await colour(page, '.moneybar-paid', 'backgroundColor'))
    .not.toBe(await colour(page, '.moneybar-owed', 'backgroundColor'));

  // A ceiling that has been crossed is drawn in the alarm colour, and that
  // rule outranks the one above it: it has to be named on its own or the one
  // mark worth seeing is the one that goes.
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('100');
  await gotoTab(page, 'overview');
  await expect(page.locator('#moneyBarCeiling')).toHaveClass(/over/);
  expect(await colour(page, '#moneyBarCeiling', 'backgroundColor')).not.toBe(behind);

  // A mark that stands for several dates carries its count inside it, and
  // that count is drawn in the colour of the page so that it reads against
  // the mark. Same plate as the filter pill: without the forcing off, the
  // number is drawn on it and cannot be seen.
  const mark = page.locator('.runup-mark').first();
  expect(await mark.evaluate((el) => window.getComputedStyle(el).forcedColorAdjust)).toBe('none');
  // The name hanging off the mark asks for the forcing back, or it is drawn
  // in our own grey on the reader's canvas.
  const label = page.locator('.runup-label').first();
  expect(await label.evaluate((el) => window.getComputedStyle(el).forcedColorAdjust)).toBe('auto');
  expect(await colour(page, '.runup-label', 'color')).not.toBe(behind);

  // A length means nothing without the whole it is a length of, and the bar's
  // own tint goes the way of every other background.
  const edge = await page.locator('.moneybar').evaluate((el) => window.getComputedStyle(el).borderTopStyle);
  expect(edge, 'the money bar has no edge to measure its lengths against').not.toBe('none');
});
