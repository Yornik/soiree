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
const { API_URL, ADMIN_EMAIL, ADMIN_PASSWORD } = require('../servers');

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

/* ------------------------------------------------------------------
 * The instance with a database behind it
 * ------------------------------------------------------------------
 * Only api.spec.js uses these. Everything above works against any of the
 * three servers and knows nothing about whether one of them has an API.
 * ------------------------------------------------------------------ */

/**
 * Signs a raw request context in and returns the session cookie to send back.
 *
 * The API is behind a session: a viewer may read, an editor or admin may
 * write, an anonymous caller gets 401. A page carries the cookie by itself,
 * but a Playwright request context is a separate client — and the cookie is
 * marked Secure, which its jar will not store over plain http to a loopback
 * address even though a browser treats that origin as trustworthy. So the
 * value is read off the response and sent back by hand.
 *
 * Returns null when the instance has no accounts at all, which is how the
 * two servers without a database answer.
 */
const sessions = new Map();

async function apiSignIn(request, base = API_URL) {
  if (sessions.has(base)) return sessions.get(base);
  const cookie = await freshSession(request, base);
  sessions.set(base, cookie);
  return cookie;
}

/**
 * A session nobody else is using, never cached.
 *
 * For the specs that END a session. The run shares one login, replayed into
 * every context, so a spec that signed that one out would leave every spec
 * after it anonymous — and an anonymous request is answered 401 whether or not
 * the thing it asked for works, which is a suite that goes green by testing
 * nothing. A spec that means to destroy a session brings its own.
 *
 * Each call is a real login against the real limiters (ten per account per
 * quarter hour), so this is for the two or three specs that need it and not a
 * replacement for apiSignIn.
 */
async function freshSession(request, base = API_URL) {
  const res = await request.post(`${base}/api/v1/auth/login`, {
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });
  if (res.status() === 404) return null;
  expect(res.status(), 'POST /api/v1/auth/login').toBe(200);

  const setCookie = res
    .headersArray()
    .filter((h) => h.name.toLowerCase() === 'set-cookie')
    .map((h) => h.value)
    .find((v) => v.startsWith('soiree_session='));
  expect(setCookie, 'login set no session cookie').toBeTruthy();

  return setCookie.split(';')[0];
}

/** Headers carrying the session, for a raw request context. */
async function apiAuth(request, base = API_URL) {
  const cookie = await apiSignIn(request, base);
  return cookie ? { Cookie: cookie } : {};
}

/** The whole plan as the server reports it, or null if this one has no database. */
async function apiPlan(request, base = API_URL) {
  const res = await request.get(`${base}/api/v1/plan`, { headers: await apiAuth(request, base) });
  if (res.status() === 404) return null;
  expect(res.status(), 'GET /api/v1/plan').toBe(200);
  return res.json();
}

const PLAN_COLLECTIONS = [
  ['notes', 'notes'],
  ['tasks', 'tasks'],
  ['budgetItems', 'budget-items'],
  ['sponsors', 'sponsors'],
];

/**
 * Empties the shared plan.
 *
 * This state really is shared — one database, one event — so a test that did
 * not start from a known page would be reading whatever the previous one left.
 * Done over the API rather than against the database directly, so the tests
 * need no second connection and no credentials of their own.
 *
 * Tried more than once on purpose. The page from the test before this one can
 * still have a write in flight as its context closes, and a sweep that loses
 * to it either gets a 409 on a revision that has moved or finishes and finds a
 * row back. Either way the answer is to read again and sweep again, so that a
 * stray write fails the test that made it rather than the one after.
 */
async function resetPlan(request, base = API_URL) {
  let problem = null;
  for (let attempt = 0; attempt < 3; attempt += 1) {
    problem = await sweepPlan(request, base);
    if (!problem) return;
  }
  throw new Error(`could not reset the shared plan: ${problem}`);
}

/** One sweep. Returns null when the plan is empty afterwards, or what went wrong. */
async function sweepPlan(request, base) {
  const plan = await apiPlan(request, base);
  if (!plan) return null;

  for (const [key, route] of PLAN_COLLECTIONS) {
    for (const row of plan[key] || []) {
      const res = await request.delete(`${base}/api/v1/${route}/${row.id}?revision=${row.revision}`, {
        headers: await apiAuth(request, base),
      });
      // Already gone is the outcome that was wanted; a stale revision is the
      // race this function exists to absorb.
      if (![204, 404].includes(res.status())) {
        return `DELETE ${route}/${row.id} answered ${res.status()}`;
      }
    }
  }

  // The singleton cannot be deleted, only put back. Its ceiling is money, so
  // zero has to carry the currency's decimal places — taken from the value the
  // server just reported rather than hardcoded, which keeps this honest if the
  // instance is ever reconfigured.
  const places = (String(plan.settings.ceiling).split('.')[1] || '').length;
  const res = await request.patch(`${base}/api/v1/settings`, {
    headers: await apiAuth(request, base),
    data: {
      revision: plan.settings.revision,
      ceiling: (0).toFixed(places),
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
    },
  });
  if (res.status() !== 200) return `PATCH /api/v1/settings answered ${res.status()}`;

  // Read it back: a row that reappeared means a write landed after the sweep
  // passed it, and the sweep has to happen again.
  const after = await apiPlan(request, base);
  const left = PLAN_COLLECTIONS.reduce((n, [key]) => n + (after[key] || []).length, 0);
  return left ? `${left} row(s) came back after the sweep` : null;
}

/**
 * Runs an action that loads the page, and waits for the plan it fetches.
 *
 * The page paints from its cached copy first and adopts the server's plan when
 * it arrives, so "the document is loaded" is not the same moment as "this
 * browser has the shared planner". Waiting on the request is the honest
 * signal, and it is a condition rather than a clock.
 */
async function awaitPlan(page, action) {
  const planned = page.waitForResponse(
    (r) => r.request().method() === 'GET' && r.url().includes('/api/v1/plan'),
  );
  await action();
  await planned;
}

/**
 * Signs a page in, through its own cookie jar.
 *
 * page.request shares cookies with the page, so logging in here is what the
 * page itself would do through the form — without making every spec drive a
 * login screen it is not testing. The API refuses an anonymous caller, and
 * app.js treats that 401 as "wait for a session" rather than as "no API", so
 * a page that skipped this would sit in local-only mode and never write
 * anything the next person could read.
 */
async function signInPage(page, base = API_URL) {
  // One login for the whole run, replayed as a cookie into each context.
  //
  // Logging in per page would be both unrealistic — a browser signs in once —
  // and self-defeating: the per-IP and per-account limiters are real, every
  // test shares one address and one account, and a suite that logs in fifty
  // times answers 429 to the ones at the end.
  await adoptSession(page, await apiSignIn(page.request, base), base);
}

/** Puts one particular session into a page's cookie jar. */
async function adoptSession(page, cookie, base = API_URL) {
  if (!cookie) return;

  const [name, value] = cookie.split('=');
  await page.context().addCookies([{
    name,
    value,
    url: base,
    httpOnly: true,
    // Matching how the server set it. 127.0.0.1 is a secure context, so a
    // Secure cookie is accepted there despite the plain-http scheme.
    secure: true,
    sameSite: 'Lax',
  }]);
}

/** Opens the shared planner, signed in, and waits for it to have the server's plan. */
async function openSharedPlanner(page, path = '/') {
  await signInPage(page);
  await awaitPlan(page, () => page.goto(path));
}

/** Reloads it, same wait. */
async function reloadSharedPlanner(page) {
  await awaitPlan(page, () => page.reload());
}

module.exports = {
  ADMIN_EMAIL,
  ADMIN_PASSWORD,
  adoptSession,
  apiSignIn,
  freshSession,
  signInPage,
  apiAuth,
  API_URL,
  STORAGE_KEY,
  apiPlan,
  awaitPlan,
  openSharedPlanner,
  reloadSharedPlanner,
  resetPlan,
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
