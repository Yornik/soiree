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

/*
 * A deployment has one locale, and the people using it do not have one
 * language. So the page asks the reader's own browser before it falls back on
 * what the operator configured — and nothing about language is stored against
 * an account, because a setting somebody made on their own device is better
 * evidence than anything an admin typed once, and theirs to change.
 */
test.describe('a browser that asks for Dutch', () => {
  test.use({ locale: 'nl-NL' });

  test('gets Dutch, on a deployment configured in English, with nothing in the URL', async ({ page }) => {
    await openIn(page);
    await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
    await expect(page.locator('#tab-overview')).toHaveText('Overzicht');
    // Language and locale are still separate axes: the figures are the event's.
    await expect(page.locator('#statDaysLabel')).toHaveText('June 12, 2030');
  });

  test('still gets whatever a link asks for', async ({ page }) => {
    await openIn(page, '?lang=id');
    await expect(page.locator('html')).toHaveAttribute('lang', 'id');
  });
});

test.describe('a browser that asks for a language there is no translation for', () => {
  test.use({ locale: 'de-DE' });

  test('gets the language the deployment was configured in', async ({ page }, testInfo) => {
    // This instance is served with SOIREE_LOCALE=nl-NL. The operator
    // configured a Dutch event, and a reader this page cannot place gets a
    // Dutch interface rather than an English one.
    await page.goto(testInfo.config.metadata.altBaseURL + '/');
    await expect(page.locator('body')).toHaveClass(/is-empty/);

    await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
    await expect(page.locator('#tab-overview')).toHaveText('Overzicht');
    await expect(page.locator('.first-run h2')).toHaveText('Nog niets in het kasboek');
  });
});

/*
 * The switcher: three flags above everything. A click is somebody's own
 * explicit choice, so it outranks their browser and is remembered on the
 * device — and it happens in place, because a reload discards whatever was
 * typed while signed out.
 */
test('a flag changes the language in place, and the device remembers it', async ({ page }) => {
  await openIn(page);
  await expect(page.locator('#langSwitch [data-lang="en"]')).toHaveAttribute('aria-pressed', 'true');
  // Named for a screen reader in the language's own word for itself: a flag is
  // not a language, and two of these three are red and white.
  await expect(page.locator('#langSwitch [data-lang]')).toHaveCount(3);
  await expect(page.getByRole('button', { name: 'Bahasa Indonesia' })).toBeVisible();

  await page.evaluate(() => { window.__neverReloaded = true; });
  await page.getByRole('button', { name: 'Nederlands' }).click();

  await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
  await expect(page.locator('#tab-overview')).toHaveText('Overzicht');
  await expect(page.locator('.first-run h2')).toHaveText('Nog niets in het kasboek');
  await expect(page.locator('#langSwitch [data-lang="nl"]')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#langSwitch [data-lang="en"]')).toHaveAttribute('aria-pressed', 'false');
  expect(await page.evaluate(() => window.__neverReloaded)).toBe(true);

  // Labels baked when a control was built are said again too.
  await expect(page.locator('.data-tools .filter-pills .pill').first()).not.toHaveText('System');

  expect(await page.evaluate(() => localStorage.getItem('soiree.lang'))).toBe('nl');
  await openIn(page);
  await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
});

test('a flag clicked on a ?lang= link takes the link\'s language out of the address', async ({ page }) => {
  // ?lang= outranks a stored choice. Left in the address bar it would make the
  // flag somebody just clicked stop working at their next reload.
  await openIn(page, '?lang=id');
  await page.getByRole('button', { name: 'English' }).click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
  expect(page.url()).not.toContain('lang=');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en');
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
