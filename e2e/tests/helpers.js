// @ts-check
/*
 * Shared vocabulary for the browser tests.
 *
 * Two rules everything here follows:
 *
 *  - Never wait on a clock. Every helper either performs an action (which
 *    Playwright already waits for) or polls a condition.
 *  - Never assert on an exact Intl string when the point is the arithmetic.
 *    `money()` turns "€2,500" back into 2500, so a test says what it means and
 *    does not break when ICU changes its mind about a space.
 */
const { expect } = require('@playwright/test');

// Must match STORAGE_KEY in web/src/app.js. If that changes, a saved planner
// is silently orphaned, so the constant is worth asserting on directly.
const STORAGE_KEY = 'soiree.v1';

/**
 * Parses a rendered money string back to a number.
 * "€2,500" -> 2500, "-€500" -> -500, "–" (the empty placeholder) -> null.
 * @param {string | null} text
 * @returns {number | null}
 */
function money(text) {
  if (text == null) return null;
  // Group separators go; the en dash placeholder and the currency symbol are
  // not in the allowed set, so they go with them.
  const cleaned = String(text).replace(/,/g, '').replace(/[^0-9.-]/g, '');
  if (cleaned === '' || cleaned === '-' || cleaned === '.') return null;
  const n = Number(cleaned);
  return Number.isFinite(n) ? n : null;
}

/** Opens a planner with empty storage. @param {import('@playwright/test').Page} page */
async function openPlanner(page) {
  await page.goto('/');
  // The app has run its first render by the time the body carries its state
  // class, so this is the one wait that says "the page is up".
  await expect(page.locator('body')).toHaveClass(/is-empty/);
}

/**
 * @param {import('@playwright/test').Page} page
 * @param {'overview'|'budget'|'tasks'} name
 */
async function gotoTab(page, name) {
  await page.locator(`#tab-${name}`).click();
  await expect(page.locator(`#panel-${name}`)).toBeVisible();
}

/** Locators for one row of the budget grid, by column. */
function budgetRow(page, index) {
  const row = page.locator('#budgetBody tr').nth(index);
  return {
    row,
    item: row.locator('td').nth(0).locator('textarea'),
    unit: row.locator('td').nth(1).locator('input'),
    qty: row.locator('td').nth(2).locator('input'),
    committed: row.locator('td').nth(3),
    paid: row.locator('td').nth(4).locator('input'),
    outstanding: row.locator('td').nth(5),
    by: row.locator('td').nth(6).locator('button.by-btn'),
    note: row.locator('td').nth(7).locator('textarea'),
    remove: row.locator('td').nth(8).locator('button'),
  };
}

/**
 * Adds a budget line the way a person does: press the button, then type into
 * the row that appears. Returns its locators.
 */
async function addBudgetLine(page, { item, unit, qty, paid = 0, note }) {
  // The placeholder "no lines yet" row is a <tr> too, so count real ones.
  const rows = page.locator('#budgetBody tr:not(:has(td.empty-cell))');
  const index = await rows.count();

  await page.locator('#addBudgetRow').click();
  await expect(rows).toHaveCount(index + 1);

  const row = budgetRow(page, index);
  if (item !== undefined) await row.item.fill(item);
  if (unit !== undefined) await row.unit.fill(String(unit));
  if (qty !== undefined) await row.qty.fill(String(qty));
  if (paid !== undefined) await row.paid.fill(String(paid));
  if (note !== undefined) await row.note.fill(note);
  return row;
}

/** Adds a sponsor and fills in its callsign and name. */
async function addSponsor(page, { code, name }) {
  const index = await page.locator('#sponsorGrid .sponsor-row').count();
  await page.locator('#addSponsor').click();
  await expect(page.locator('#sponsorGrid .sponsor-row')).toHaveCount(index + 1);

  const row = page.locator('#sponsorGrid .sponsor-row').nth(index);
  await row.locator('.code-input').fill(code);
  await row.locator('.name-input').fill(name);
  return row;
}

/** Tags a budget line with a sponsor through the cost-by picker. */
async function tagLine(row, codes) {
  await row.by.click();
  const pop = row.by.page().locator('.sp-pop');
  await expect(pop).toBeVisible();
  for (const code of codes) {
    await pop.locator('label').filter({ hasText: code }).locator('input[type="checkbox"]').check();
  }
  await row.by.page().keyboard.press('Escape');
  await expect(pop).toHaveCount(0);
}

/** Adds a task and fills it in. Returns the row locator. */
async function addTask(page, { name, owner = '', due = '', status }) {
  const index = await page.locator('#tasksBody tr:not(:has(td.empty-cell))').count();
  await page.locator('#addTaskRow').click();
  await expect(page.locator('#tasksBody tr:not(:has(td.empty-cell))')).toHaveCount(index + 1);

  const row = page.locator('#tasksBody tr').nth(index);
  await row.locator('td').nth(0).locator('input').fill(name);
  if (owner) await row.locator('td').nth(1).locator('input').fill(owner);
  if (due) await row.locator('td').nth(2).locator('input').fill(due);
  if (status) await row.locator('select.status-select').selectOption(status);
  return row;
}

/**
 * Every figure the page shows more than once, grouped by the figure it is.
 * These are the same numbers rendered into different corners of the page, so
 * each group must collapse to exactly one rendering.
 */
async function duplicatedFigures(page) {
  return page.evaluate(() => {
    const t = (id) => {
      const el = document.getElementById(id);
      return el ? el.textContent : '(missing #' + id + ')';
    };
    return {
      committed: [t('mCommitted'), t('sumTotal'), t('sumTotalAlt')],
      paid: [t('mPaid'), t('sumPaid')],
      outstanding: [t('mOutstanding'), t('sumOwing'), t('sumOwingAlt')],
      forecast: [t('mForecast'), t('sumForecast')],
    };
  });
}

/**
 * Asserts the four headline figures both agree with each other everywhere they
 * appear, and equal the arithmetic they claim to be.
 *
 * @param {import('@playwright/test').Page} page
 * @param {{committed: number, paid: number, outstanding: number, forecast: number}} expected
 */
async function expectFigures(page, expected) {
  await expect
    .poll(
      async () => {
        const groups = await duplicatedFigures(page);
        /** @type {Record<string, unknown>} */
        const out = {};
        for (const [name, texts] of Object.entries(groups)) {
          const distinct = [...new Set(texts.map((s) => (s || '').trim()))];
          // One rendering per figure, or the page is contradicting itself —
          // in which case surface the differing strings, not a number.
          out[name] = distinct.length === 1 ? money(distinct[0]) : distinct;
        }
        return out;
      },
      { message: 'a figure shown in more than one place must read the same in all of them' },
    )
    .toEqual(expected);
}

/** The persisted planner, or null if nothing has been written yet. */
async function readStored(page, key = STORAGE_KEY) {
  return page.evaluate((k) => {
    try {
      const raw = localStorage.getItem(k);
      return raw ? JSON.parse(raw) : null;
    } catch (e) {
      return null;
    }
  }, key);
}

/**
 * Forces the debounced writer to flush, the way leaving the page does.
 *
 * The app writes 500 ms after the last edit and flushes on `pagehide`. Both
 * paths end in the same synchronous `Store.write`, so once this resolves the
 * write has happened — no sleeping, and no race either way round.
 */
async function flushToStorage(page) {
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));
}

/**
 * Waits for the debounced write to land on its own, without provoking it.
 * Used where the point is that an ordinary edit does get persisted.
 */
async function expectStored(page, predicate, message) {
  await expect.poll(() => readStored(page).then(predicate), { message }).toBe(true);
}

/** The "who's covering what" list as plain data. */
async function readSplit(page) {
  const rows = await page.evaluate(() =>
    Array.from(document.querySelectorAll('#splitList li')).map((li) => ({
      label: li.children[0].textContent,
      amount: li.querySelector('.amt').textContent,
      pct: li.querySelector('.pct').textContent,
    })),
  );
  return rows.map((r) => ({ label: r.label, amount: money(r.amount), pct: r.pct }));
}

module.exports = {
  STORAGE_KEY,
  addBudgetLine,
  addSponsor,
  addTask,
  budgetRow,
  duplicatedFigures,
  expectFigures,
  expectStored,
  flushToStorage,
  gotoTab,
  money,
  openPlanner,
  readSplit,
  readStored,
  tagLine,
};
