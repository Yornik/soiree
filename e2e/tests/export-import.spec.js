// @ts-check
/*
 * Export / import round trip.
 *
 * In the deployment with no database this is the only way to move a planner
 * between people; in the one with a database it is how a planner gets in and
 * out of the deployment altogether, and how anything stranded in a browser is
 * recovered when writes are not reaching the server. Either way it is the
 * highest-consequence path in the app: a broken export is silent, and is
 * discovered when someone needs the data.
 *
 * These run against the no-database instance. The same two buttons over a
 * shared plan are covered in api.spec.js, where an import replaces what the
 * server holds rather than what this browser holds.
 *
 * The round trip is done properly — export, wipe, import, compare — rather
 * than importing over state that already matches, which would pass whether or
 * not anything was read.
 */
const fs = require('fs/promises');
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, addTask, expectFigures, gotoTab, openPlanner, readSplit, tagLine } = require('./helpers');

async function buildPlanner(page) {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  await page.locator('#ceilingInput').fill('10000');
  await page.locator('#inflationInput').fill('4');

  await addSponsor(page, { code: 'Rose', name: 'Ada' });
  await addSponsor(page, { code: 'Ivy', name: 'Grace' });

  const venue = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1, paid: 500 });
  const catering = await addBudgetLine(page, { item: 'Catering', unit: 50, qty: 40, paid: 0 });
  await tagLine(venue, ['Rose']);
  await tagLine(catering, ['Rose', 'Ivy']);

  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm final guest count', owner: 'Ada', due: '2030-01-15', status: 'in-progress' });
  await gotoTab(page, 'budget');
}

/**
 * Records every dialog and answers it. Recording rather than asserting inside
 * the handler: an exception thrown from a Playwright event listener does not
 * become a test failure, it becomes a mess.
 */
function watchDialogs(page, answer) {
  const seen = [];
  page.on('dialog', (dialog) => {
    seen.push({ type: dialog.type(), message: dialog.message() });
    if (answer === 'accept') dialog.accept().catch(() => {});
    else dialog.dismiss().catch(() => {});
  });
  return seen;
}

/** Clicks export and returns the saved file's path and parsed contents. */
async function exportTo(page, filePath) {
  // Armed before the click: the handler revokes the blob URL a second later,
  // so there is nothing to wait around for afterwards.
  const downloadPromise = page.waitForEvent('download');
  await page.locator('#exportData').click();
  const download = await downloadPromise;
  await download.saveAs(filePath);
  return JSON.parse(await fs.readFile(filePath, 'utf8'));
}

test('export writes the whole planner to a dated file', async ({ page }, testInfo) => {
  await buildPlanner(page);

  const file = testInfo.outputPath('soiree-export.json');
  const downloadPromise = page.waitForEvent('download');
  await page.locator('#exportData').click();
  const download = await downloadPromise;
  await download.saveAs(file);

  expect(download.suggestedFilename()).toMatch(/^soiree-\d{4}-\d{2}-\d{2}\.json$/);
  await expect(page.locator('#dataMsg')).toHaveText(`Exported ${download.suggestedFilename()}`);

  const data = JSON.parse(await fs.readFile(file, 'utf8'));
  expect(data.ceiling).toBe(10000);
  expect(data.inflationPct).toBe(4);
  expect(data.sponsors.map((s) => s.code)).toEqual(['Rose', 'Ivy']);
  expect(data.budgetItems.map((i) => [i.item, i.unit, i.qty, i.paid])).toEqual([
    ['Venue deposit', 2000, 1, 500],
    ['Catering', 50, 40, 0],
  ]);
  expect(data.tasks).toHaveLength(1);
  expect(data.tasks[0]).toMatchObject({ name: 'Confirm final guest count', owner: 'Ada', status: 'in-progress' });

  // Attribution travels as ids that resolve inside the same file, or the
  // import lands with every line unassigned.
  const ids = new Set(data.sponsors.map((s) => s.id));
  expect(data.budgetItems[0].sponsors.every((id) => ids.has(id))).toBe(true);
  expect(data.budgetItems[1].sponsors).toHaveLength(2);
});

test('export flushes pending edits rather than exporting the last saved copy', async ({ page }, testInfo) => {
  await openPlanner(page);
  await gotoTab(page, 'budget');
  const row = await addBudgetLine(page, { item: 'Venue deposit', unit: 2000, qty: 1 });

  // Typed and exported with nothing in between: the handler flushes first, so
  // the very last keystroke is in the file.
  await row.note.fill('Balance due one month before');
  const data = await exportTo(page, testInfo.outputPath('flush.json'));
  expect(data.budgetItems[0].note).toBe('Balance due one month before');
});

test('a planner exported and imported somewhere else comes back whole', async ({ page, browser }, testInfo) => {
  await buildPlanner(page);
  const file = testInfo.outputPath('round-trip.json');
  const exported = await exportTo(page, file);

  const splitBefore = await readSplit(page);
  await expectFigures(page, { committed: 4000, paid: 500, outstanding: 3500, forecast: 4160 });

  /*
   * Import into a browser that has never seen this planner, which is the
   * reason export exists in the first place: until state is shared, a file is
   * how it reaches the next person.
   *
   * Not by clearing localStorage and reloading, which does not work and is
   * worth knowing: the live page flushes its own state on pagehide, so the
   * reload puts everything straight back. A second context is the only wipe
   * that sticks — and it is the honest scenario anyway.
   */
  const origin = new URL(page.url()).origin;
  const elsewhere = await browser.newContext({ baseURL: origin, serviceWorkers: 'block' });
  const other = await elsewhere.newPage();
  try {
    await other.goto('/');
    await expect(other.locator('body')).toHaveClass(/is-empty/);

    // Replacing everything is destructive, so it asks first. Accept
    // explicitly: Playwright's default is to dismiss, which makes confirm()
    // return false and would leave this test passing having imported nothing.
    const dialogs = watchDialogs(other, 'accept');
    const chooserPromise = other.waitForEvent('filechooser');
    await other.locator('#importData').click();
    const chooser = await chooserPromise;
    await chooser.setFiles(file);

    await expect(other.locator('#dataMsg')).toHaveText(/^Imported /);
    expect(dialogs).toEqual([{ type: 'confirm', message: expect.stringContaining('Replace everything') }]);
    await expect(other.locator('body')).not.toHaveClass(/is-empty/);

    await expectFigures(other, { committed: 4000, paid: 500, outstanding: 3500, forecast: 4160 });
    expect(await readSplit(other)).toEqual(splitBefore);

    await gotoTab(other, 'budget');
    await expect(other.locator('#ceilingInput')).toHaveValue('10000');
    await expect(other.locator('#inflationInput')).toHaveValue('4');
    await expect(other.locator('#sponsorGrid .sponsor-row')).toHaveCount(2);
    await expect(other.locator('#budgetBody tr')).toHaveCount(2);
    await expect(other.locator('#budgetBody tr').first().locator('button.by-btn')).toHaveText('Rose');

    await gotoTab(other, 'tasks');
    await expect(other.locator('#tasksBody tr')).toHaveCount(1);
    await expect(other.locator('#tasksBody select.status-select')).toHaveValue('in-progress');

    // The imported planner is now the saved one there too: it survives a
    // reload, and re-exporting gives the same document back, so a round trip
    // is not lossy.
    await other.reload();
    await expect(other.locator('body')).not.toHaveClass(/is-empty/);
    const again = await exportTo(other, testInfo.outputPath('round-trip-2.json'));
    expect(again).toEqual(exported);
  } finally {
    await elsewhere.close();
  }
});

test('declining the confirmation leaves the planner untouched', async ({ page }, testInfo) => {
  await buildPlanner(page);
  const file = testInfo.outputPath('declined.json');
  await exportTo(page, file);

  // Different data on disk from what is on screen, so an import that ignored
  // the answer would be obvious.
  const other = JSON.parse(await fs.readFile(file, 'utf8'));
  other.ceiling = 999;
  other.budgetItems = [{ id: 'b1', item: 'Something else entirely', unit: 1, qty: 1, paid: 0, sponsors: [], note: '' }];
  other.tasks = [];
  const otherFile = testInfo.outputPath('other.json');
  await fs.writeFile(otherFile, JSON.stringify(other));

  const dialogs = watchDialogs(page, 'dismiss');
  const chooserPromise = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooserPromise).setFiles(otherFile);

  await expect.poll(() => dialogs.length, { message: 'import must ask before replacing' }).toBe(1);
  await expect(page.locator('#ceilingInput')).toHaveValue('10000');
  await expect(page.locator('#budgetBody tr')).toHaveCount(2);
  await expectFigures(page, { committed: 4000, paid: 500, outstanding: 3500, forecast: 4160 });
});

test('a file that is not planner data is refused, and nothing is lost', async ({ page }, testInfo) => {
  await buildPlanner(page);

  const junk = testInfo.outputPath('not-a-planner.json');
  await fs.writeFile(junk, JSON.stringify({ hello: 'world' }));

  // Nothing destructive happens, so nothing is asked: the file is rejected
  // before it can replace anything.
  const dialogs = watchDialogs(page, 'dismiss');

  const chooserPromise = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooserPromise).setFiles(junk);

  await expect(page.locator('#dataMsg')).toHaveText('That file does not look like planner data.');
  await expectFigures(page, { committed: 4000, paid: 500, outstanding: 3500, forecast: 4160 });
  expect(dialogs).toEqual([]);

  // Not JSON at all is a different failure from JSON that is not a planner,
  // and says so.
  const badAgain = testInfo.outputPath('still-not-a-planner.json');
  await fs.writeFile(badAgain, 'this is not json at all');
  const secondChooser = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await secondChooser).setFiles(badAgain);
  await expect(page.locator('#dataMsg')).toHaveText('Could not read that file.');
  expect(dialogs).toEqual([]);
});

/*
 * The copy an import takes on its way through is the only way back: the
 * server keeps no undo, and the files on the rows it removes are in neither
 * the export nor the backup. The dialog says the copy has been taken, and a
 * browser that cannot make a blob for it (a quota, an extension in the way)
 * made that a promise the page did not keep, then replaced the planner
 * anyway. The export button has always reported the same failure.
 */
test('an import whose copy cannot be saved replaces nothing', async ({ page }, testInfo) => {
  // Where downloadPlan fails. A download the person cancels does not throw
  // and is not this case.
  await page.addInitScript(() => {
    URL.createObjectURL = () => { throw new Error('no object URLs here'); };
  });
  await buildPlanner(page);

  const incoming = testInfo.outputPath('replacement.json');
  await fs.writeFile(
    incoming,
    JSON.stringify({
      ceiling: 999,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      sponsors: [],
      budgetItems: [{ id: 'b1', item: 'Something else entirely', unit: 1, qty: 1, paid: 0, sponsors: [], note: '' }],
      tasks: [],
      notes: [],
    }),
  );

  const dialogs = watchDialogs(page, 'accept');
  const chooserPromise = page.waitForEvent('filechooser');
  await page.locator('#importData').click();
  await (await chooserPromise).setFiles(incoming);

  await expect.poll(() => dialogs.length, { message: 'import must ask before replacing' }).toBe(1);
  await expect(page.locator('#dataMsg'))
    .toHaveText('Could not export automatically — copy the JSON from the console instead.');

  // Said yes to a replacement that did not happen, which is the right way
  // round: the planner is still here and so is the way back to it.
  await expect(page.locator('#ceilingInput')).toHaveValue('10000');
  await expect(page.locator('#budgetBody tr')).toHaveCount(2);
  await expectFigures(page, { committed: 4000, paid: 500, outstanding: 3500, forecast: 4160 });
});
