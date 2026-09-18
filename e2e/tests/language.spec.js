// @ts-check
/*
 * The interface is not English-only.
 *
 * Every user-visible string comes from one table in app.js. There is no
 * framework and no extra request: the whole of English, Dutch and Indonesian
 * ships in the script that was already being downloaded.
 *
 * Language and locale are deliberately separate axes. Locale decides how a
 * number is spelled; language decides what the page is written in. An event
 * priced in one country is read by people in another, so the tests below check
 * that moving one does not move the other.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, budgetRow, gotoTab, money } = require('./helpers');

/** Opens a planner in a given language and waits for the first render. */
async function openIn(page, query = '') {
  await page.goto('/' + query);
  await expect(page.locator('body')).toHaveClass(/is-empty/);
}

test('Dutch replaces the interface without touching the figures', async ({ page }) => {
  await openIn(page, '?lang=nl');

  // lang drives screen-reader pronunciation and hyphenation, so it has to
  // follow the strings rather than the template's default.
  await expect(page.locator('html')).toHaveAttribute('lang', 'nl');

  await expect(page.locator('#tab-overview')).toHaveText('Overzicht');
  await expect(page.locator('#tab-budget')).toHaveText('Budget');
  await expect(page.locator('#tab-tasks')).toHaveText('Taken');
  await expect(page.locator('.first-run h2')).toHaveText('Nog niets in het kasboek');
  await expect(page.locator('#daysLabel')).toHaveText('dagen te gaan');

  await gotoTab(page, 'budget');
  await expect(page.locator('#budgetTable thead th').nth(0)).toHaveText('Post');
  await expect(page.locator('#budgetTable thead th').nth(3)).toHaveText('Vastgelegd');
  await expect(page.locator('#budgetTable thead th').nth(5)).toHaveText('Openstaand');
  await expect(page.locator('#addBudgetRow')).toHaveText('Post toevoegen');

  // The currency label still comes from the configuration, not from the
  // language, and the figure is still formatted in the configured locale.
  await expect(page.locator('#panel-budget .cur-code').first()).toHaveText('EUR');
  const row = await addBudgetLine(page, { item: 'Zaalhuur', unit: 2500, qty: 1, paid: 0 });
  // en-US grouping, because SOIREE_LOCALE is en-US on this instance. Reading
  // the page in Dutch does not re-price the event.
  await expect(row.committed).toHaveText('€2,500');
  await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');

  // Strings built in JavaScript are translated too, not just the markup.
  await expect(row.by).toHaveText('Niet toegewezen');
  await expect(page.locator('#budgetBody .del-btn').first()).toHaveAttribute(
    'aria-label',
    'Post verwijderen',
  );
});

test('Indonesian ships too, and an untranslated string falls back to English', async ({ page }) => {
  await openIn(page, '?lang=id');

  await expect(page.locator('html')).toHaveAttribute('lang', 'id');
  await expect(page.locator('#tab-overview')).toHaveText('Ringkasan');
  await expect(page.locator('#tab-budget')).toHaveText('Anggaran');
  await expect(page.locator('#tab-tasks')).toHaveText('Tugas');
  await expect(page.locator('.first-run h2')).toHaveText('Buku kas masih kosong');
  await expect(page.locator('#daysLabel')).toHaveText('hari lagi');

  await gotoTab(page, 'budget');
  await expect(page.locator('#budgetTable thead th').nth(3)).toHaveText('Total biaya');
  await expect(page.locator('#budgetTable thead th').nth(5)).toHaveText('Sisa bayar');
  await expect(page.locator('#budgetTable thead th').nth(6)).toHaveText('Ditanggung');

  // "Item" is left out of the Indonesian table on purpose — it is the word an
  // Indonesian spreadsheet uses — so it arrives through the English fallback.
  // That path has to produce the word, never the key.
  await expect(page.locator('#budgetTable thead th').nth(0)).toHaveText('Item');
});

test('an unknown language falls back to English rather than to raw keys', async ({ page }) => {
  await openIn(page, '?lang=fr');

  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await expect(page.locator('#tab-overview')).toHaveText('Overview');
  await expect(page.locator('.first-run h2')).toHaveText('Nothing in the ledger yet');
});

test('with no override the language follows the configured locale', async ({ page }, testInfo) => {
  // This instance is served with SOIREE_LOCALE=nl-NL and nothing else: no
  // query string, no stored preference. The operator configured a Dutch event
  // and got a Dutch interface.
  await page.goto(testInfo.config.metadata.altBaseURL + '/');
  await expect(page.locator('body')).toHaveClass(/is-empty/);

  await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
  await expect(page.locator('#tab-overview')).toHaveText('Overzicht');
  await expect(page.locator('.first-run h2')).toHaveText('Nog niets in het kasboek');
});

test('the language is a view of the page, not a setting written into the planner', async ({ page }) => {
  await openIn(page, '?lang=nl');
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Zaalhuur', unit: 2500, qty: 1, paid: 0 });
  await page.evaluate(() => window.dispatchEvent(new Event('pagehide')));

  // Nothing about the choice is persisted: one shared planner, and two people
  // reading it in two languages must not overwrite each other's.
  const keys = await page.evaluate(() => Object.keys(localStorage));
  expect(keys).toEqual(['soiree.v1']);
  const stored = await page.evaluate(() => JSON.parse(localStorage.getItem('soiree.v1')));
  expect(stored).not.toHaveProperty('lang');
  expect(stored).not.toHaveProperty('language');

  // Same planner, opened in English: the data is identical, the words are not.
  await page.goto('/');
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  await gotoTab(page, 'budget');
  await expect(budgetRow(page, 0).item).toHaveValue('Zaalhuur');
  expect(money(await page.locator('#sumTotal').textContent())).toBe(2500);
  await expect(page.locator('#addBudgetRow')).toHaveText('Add budget line');
});
