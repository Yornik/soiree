// @ts-check
/*
 * The page, under the policy it is written to.
 *
 * "No inline script that executes, no `style=` attribute" is a house rule the
 * frontend obeys, and until the binary sent a Content-Security-Policy of its
 * own nothing here could tell: the servers this suite starts had no policy, so
 * a stray inline style passed every test and broke only in production, behind
 * whichever proxy happened to write the policy out by hand.
 *
 * The browser is the only thing that can enforce the rule, so it does. Every
 * page opened here reports its violations into an array, and the run that
 * proves the array would not stay empty by accident is in this file too.
 */
const { test, expect } = require('@playwright/test');
const { addBudgetLine, addSponsor, addTask, gotoTab, openPlanner } = require('./helpers');
const { API_URL, S3_URL } = require('../servers');

// The policy a deployment with no bucket gets. Written out rather than read
// from the response, so that a directive quietly going missing is a failure
// and not a tautology.
const POLICY = "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; "
  + "form-action 'self'; object-src 'none'; script-src 'self'; style-src 'self'; "
  + "img-src 'self' data:; font-src 'self'; connect-src 'self'";

/**
 * Collects every violation the page reports, from before its first script runs.
 * @param {import('@playwright/test').Page} page
 */
async function watchViolations(page) {
  await page.addInitScript(() => {
    window.__cspViolations = [];
    document.addEventListener('securitypolicyviolation', (e) => {
      window.__cspViolations.push(`${e.effectiveDirective} | ${e.blockedURI} | ${e.sourceFile || ''}`);
    });
  });
}

/**
 * @param {import('@playwright/test').Page} page
 * @returns {Promise<string[]>}
 */
async function violations(page) {
  return page.evaluate(() => window.__cspViolations || []);
}

test.beforeEach(async ({ page }) => {
  await watchViolations(page);
});

test('the server sends the policy the page is written to', async ({ page }) => {
  const res = await page.goto('/');
  expect(res, 'a response for /').not.toBeNull();
  expect(res && res.headers()['content-security-policy']).toBe(POLICY);
});

test('nothing the planner does violates the policy', async ({ page }) => {
  await openPlanner(page);

  // One pass over the three tabs and the things that draw the most: a grid
  // row, a sponsor, a task, a picker, a dialog. A violation anywhere in here
  // is an inline style or an inline script that review did not catch.
  await gotoTab(page, 'budget');
  await addBudgetLine(page, { item: 'Marquee', unit: '1200', qty: 1, paid: 200 });
  await addSponsor(page, { code: 'AB', name: 'A Benefactor' });
  await gotoTab(page, 'tasks');
  await addTask(page, { name: 'Confirm the caterer', owner: 'Ada', due: '2030-05-01' });
  await gotoTab(page, 'overview');

  expect(await violations(page), 'Content-Security-Policy violations').toEqual([]);
});

/*
 * The control. An empty array is the same result whether the page is clean or
 * the listener never fired, and those are not the same thing, so this asks the
 * page for exactly the breach the house rule forbids and expects to be told.
 */
test('an inline style attribute is reported, so an empty report means something', async ({ page }) => {
  await openPlanner(page);
  await page.evaluate(() => {
    document.body.setAttribute('style', 'outline: 1px solid red');
  });

  // The event is delivered as a task of its own, so poll rather than read.
  await expect.poll(async () => (await violations(page)).join('\n'))
    .toMatch(/style-src/);
  await expect(page.locator('body')).not.toHaveCSS('outline-color', 'rgb(255, 0, 0)');
});

/*
 * The one directive the binary knows better than any proxy: the browser
 * uploads to the bucket itself, so the policy has to name that origin, and the
 * origin is already configured here. Hand-copied into a proxy instead, it is a
 * string that drifts, and an upload it no longer matches fails in the browser
 * with nothing in any server log.
 */
test('connect-src names the bucket when there is one', async ({ request }) => {
  const res = await request.get(`${API_URL}/`);
  test.skip(!/"attachments":/.test(await res.text()), 'this run has no bucket');

  expect(res.headers()['content-security-policy'])
    .toBe(`${POLICY} ${S3_URL}`);
});
