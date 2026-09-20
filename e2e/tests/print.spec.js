// @ts-check
/*
 * What comes out of the printer.
 *
 * A plan is a record, and the thing people do with a record is print it or
 * save it as a PDF. Three things have to be true on paper and none of them is
 * true on screen: the light palette whatever the device is set to, every
 * section rather than the tab in front, and nothing on the sheet that can be
 * pressed.
 *
 * Two of those are viewport-independent and are asserted here.
 * `emulateMedia({ media: 'print' })` applies the print styles at the browser's
 * current viewport, not at a page box, so anything about the width of paper
 * is checked by rendering an actual PDF instead - see the pull request that
 * added this file. The layout assertions below therefore set the viewport to
 * an A4 page area first, and ask only what that arrangement can answer: that
 * the grid is no wider than the page, and that nothing in it is wider than
 * the room it has.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, gotoTab, openPlanner } = require('./helpers');

// The page area of A4 portrait at the margins a browser prints with: 794 CSS
// pixels of paper less two 12mm margins.
const A4_PAGE_AREA = { width: 703, height: 1032 };

test('paper is read in the light palette, whatever the device is set to', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await openPlanner(page);

  // Dark on screen: the ink is nearly white, which is exactly what a browser
  // sends to the printer when nothing tells it otherwise.
  const screenInk = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--ink').trim());
  expect(screenInk.toUpperCase()).toBe('#F1EEF8');

  await page.emulateMedia({ media: 'print', colorScheme: 'dark' });
  const paperInk = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--ink').trim());
  expect(paperInk.toUpperCase()).toBe('#231B3A');
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'light');
});

test('a hand-picked dark theme prints light too', async ({ page }) => {
  await openPlanner(page);
  await page.evaluate(() => {
    localStorage.setItem('soiree.theme', 'dark');
    document.documentElement.setAttribute('data-theme', 'dark');
  });

  await page.emulateMedia({ media: 'print' });
  const paperInk = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--ink').trim());
  expect(paperInk.toUpperCase()).toBe('#231B3A');
});

test('every section prints, not only the tab in front', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });
  await gotoTab(page, 'overview');

  // On screen the other two are not in the page at all.
  await expect(page.locator('#panel-budget')).toBeHidden();
  await expect(page.locator('#panel-tasks')).toBeHidden();

  await page.emulateMedia({ media: 'print' });
  await expect(page.locator('#panel-overview')).toBeVisible();
  await expect(page.locator('#panel-budget')).toBeVisible();
  await expect(page.locator('#panel-tasks')).toBeVisible();
});

test('nothing that can only be pressed reaches the paper', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Catering', unit: 45, qty: 40, paid: 0 });

  await page.emulateMedia({ media: 'print' });
  for (const chrome of [
    '.lang-switch',          // the flags
    '.tabs',                 // the three tab buttons
    '.data-tools',           // export, import and the theme control
    '.size-tools',           // add a line, reset the column widths
    '#addBudgetRow',
    '#budgetBody .del-btn',
    '#taskFilters',
  ]) {
    await expect(page.locator(chrome).first(), chrome).toBeHidden();
  }

  // The figures themselves stay, and so does the line that was entered.
  await expect(page.locator('#sumTotalAlt')).toBeVisible();
  await expect(page.locator('#budgetBody tr').first().locator('textarea').first())
    .toHaveValue('Catering');
});

test('the budget grid keeps its columns and stays on the sheet', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Venue deposit', unit: 2500, qty: 1, paid: 500 });

  await page.setViewportSize(A4_PAGE_AREA);
  await page.emulateMedia({ media: 'print' });

  // A page area this narrow is a phone on screen, and the grid unwinds into
  // one entry per line there. Paper is not a phone: fourteen lines that way
  // are four sheets, so the columns stay and the table is fitted to them.
  await expect(page.locator('#budgetTable thead')).toBeVisible();

  const fits = await page.evaluate(() => {
    const table = document.getElementById('budgetTable');
    return {
      table: Math.round(table.getBoundingClientRect().width),
      page: Math.round(document.querySelector('.wrap').getBoundingClientRect().width),
    };
  });
  expect(fits.table).toBeLessThanOrEqual(fits.page);
});

test('every figure and heading prints whole, inside its own column', async ({ page }) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await addBudgetLine(page, {
    item: 'A very long vendor name that keeps going',
    unit: 12345.67,
    qty: 12,
    paid: 999.99,
    note: 'a remark that runs on past the width of the column it is in',
  });

  await page.setViewportSize(A4_PAGE_AREA);
  await page.emulateMedia({ media: 'print' });

  // A sheet cannot be scrolled and a field cannot be widened by hand, so
  // anything wider than the room it has is either a figure with a digit
  // missing or a word printed across the column next to it. Both have
  // happened here: a money field carries a 70px minimum width that a typed
  // selector sets, and the cost-by button carries six pixels of padding each
  // side of a word that is already as wide as its column.
  const spilled = await page.evaluate(() => {
    const over = [];
    const check = (el, what) => {
      if (el.scrollWidth > el.clientWidth) {
        over.push(`${what}: ${el.scrollWidth}px of content in ${el.clientWidth}px`);
      }
    };
    document.querySelectorAll('#budgetTable thead th').forEach((th) => {
      check(th, `heading "${th.textContent.trim()}"`);
    });
    document.querySelectorAll('#budgetBody input, #budgetBody textarea, #budgetBody .by-btn').forEach((el) => {
      const value = (el.value || el.textContent || '').trim();
      check(el, `field "${value}"`);
      const cell = el.closest('td').getBoundingClientRect();
      const box = el.getBoundingClientRect();
      // Half a pixel of tolerance: a percentage share of a page rarely lands
      // on a whole one.
      if (box.right > cell.right + 0.5 || box.left < cell.left - 0.5) {
        over.push(`field "${value}" is laid out outside its column`);
      }
    });
    return over;
  });
  expect(spilled).toEqual([]);
});
