// @ts-check
/*
 * The accounts interface, driven against a scripted server.
 *
 * Why scripted rather than real: this spec is about what the *page* does —
 * which request it sends, what it puts in the body, what it draws with the
 * answer, and what it refuses to send at all. Those are properties of
 * auth.js, and pinning them against a live PostgreSQL would make every one of
 * them depend on a container, on rate limiters shared across the whole run,
 * and on state left by the test before. The companion spec, auth-api.spec.js,
 * does the same journeys against the real server; between them the boundary
 * is covered from both sides.
 *
 * It runs against the default instance, which has no database and therefore
 * no /api/v1 at all. Every route below is intercepted before it reaches it,
 * and anything not intercepted is answered 404 — exactly as that server would.
 * So there is no shared state here, and these tests stay parallel-safe.
 *
 * Two rules inherited from the rest of the suite: never wait on a clock, and
 * never assert on an exact Intl string where the point is the behaviour.
 */
const fs = require('fs');
const path = require('path');
const { test, expect } = require('@playwright/test');
const { STORAGE_KEY } = require('./helpers');

// Synthetic throughout. No real person, no real address, no real secret.
const ADA = { id: 'a0000000-0000-4000-8000-000000000001', email: 'ada@example.test', role: 'admin', status: 'active', createdBy: null, createdAt: '2026-01-04T10:00:00Z', revision: 1, updatedAt: '2026-01-04T10:00:00Z' };
const GRACE = { id: 'a0000000-0000-4000-8000-000000000002', email: 'grace@example.test', role: 'editor', status: 'active', createdBy: ADA.id, createdAt: '2026-02-11T10:00:00Z', revision: 3, updatedAt: '2026-02-11T10:00:00Z' };
const LINUS = { id: 'a0000000-0000-4000-8000-000000000003', email: 'linus@example.test', role: 'viewer', status: 'invited', createdBy: ADA.id, createdAt: '2026-03-02T10:00:00Z', revision: 1, updatedAt: '2026-03-02T10:00:00Z' };

// One entry of the shape /activity answers with, so the list has something
// to draw when the request it is asked for works.
const ENTRY = {
  at: '2026-03-04T11:52:09Z',
  actor: { kind: 'user', email: ADA.email },
  action: 'update',
  entity: 'budget_items',
  label: 'Venue deposit',
  changes: [{ field: 'paid', old: '0', new: '500' }],
};

const PASSWORD = 'a-long-enough-one';
// Stands in for the 256-bit token a real link carries. It is a fixture, and
// the assertions on it are the point: it must arrive in the body and must not
// stay in the URL.
const TOKEN = 'fixture-token-not-a-real-credential';

/**
 * Mounts a scripted accounts surface in front of the page.
 *
 * Returns the state it keeps, so a test can read what was asked of it —
 * `calls` is every request the page made, in order, with its body.
 */
async function mountAccounts(page, opts = {}) {
  const state = {
    session: opts.session || null,
    users: (opts.users || []).map((u) => ({ ...u })),
    passkeys: opts.passkeys || [],
    mailSent: !!opts.mailSent,
    // A test sets this to make the next write answer 409 with the row as the
    // server has it, which is the concurrent-edit path.
    stale: opts.stale || null,
    activity: opts.activity || [],
    // How many /activity requests are refused before one is answered. The
    // screen's own retry is what is being tested, so the failure has to stop.
    activityFails: opts.activityFails || 0,
    calls: [],
  };

  const json = (route, status, body) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

  await page.route('**/api/v1/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace('/api/v1', '');
    let body = null;
    try { body = req.postDataJSON(); } catch (e) { body = null; }
    state.calls.push({ method: req.method(), path, search: url.search, body });

    const user = (id) => state.users.find((u) => u.id === id);

    if (path === '/auth/session') {
      return state.session
        ? json(route, 200, state.session)
        : json(route, 401, { error: 'unauthenticated' });
    }
    if (path === '/auth/login') {
      const who = state.users.find((u) => u.email === (body && body.email));
      if (who && body.password === PASSWORD) {
        state.session = who;
        return json(route, 200, who);
      }
      return json(route, 401, { error: 'invalid_credentials' });
    }
    if (path === '/auth/logout') {
      state.session = null;
      return route.fulfill({ status: 204, body: '' });
    }
    if (path === '/auth/password-reset') return json(route, 202, { status: 'accepted' });
    if (path === '/auth/set-password') {
      if (!body || body.token !== TOKEN) return json(route, 400, { error: 'invalid_token' });
      state.session = null;
      return route.fulfill({ status: 204, body: '' });
    }
    // Everything past here is behind a session, and the server answers for
    // that before it looks at what was asked.
    if (!state.session) return json(route, 401, { error: 'unauthenticated' });

    if (path === '/auth/passkeys') return json(route, 200, { passkeys: state.passkeys });

    if (path === '/activity') {
      if (state.activityFails > 0) {
        state.activityFails -= 1;
        return json(route, 500, { error: 'internal' });
      }
      return json(route, 200, { entries: state.activity, nextBefore: null });
    }

    if (path === '/users' && req.method() === 'GET') {
      if (!state.session || state.session.role !== 'admin') return json(route, 403, { error: 'forbidden' });
      return json(route, 200, { users: state.users });
    }
    if (path === '/users' && req.method() === 'POST') {
      const made = {
        ...LINUS,
        id: 'a0000000-0000-4000-8000-00000000009' + state.users.length,
        email: body.email,
        role: body.role,
        status: 'invited',
        revision: 1,
      };
      state.users.push(made);
      return json(route, 201, {
        user: made,
        mailSent: state.mailSent,
        setPasswordUrl: state.mailSent ? undefined : `http://127.0.0.1/#/set-password?token=${TOKEN}`,
      });
    }
    // A row another admin removed: the server re-reads it and finds nothing,
    // which is a 404 whatever was being asked of it.
    const target = path.match(/^\/users\/([^/]+)(?:\/invite)?$/);
    if (target && req.method() !== 'GET' && !user(target[1])) {
      return json(route, 404, { error: 'not_found' });
    }

    const invite = path.match(/^\/users\/([^/]+)\/invite$/);
    if (invite) {
      const who = user(invite[1]);
      return json(route, 200, {
        user: who,
        mailSent: state.mailSent,
        setPasswordUrl: state.mailSent ? undefined : `http://127.0.0.1/#/set-password?token=${TOKEN}`,
      });
    }
    const one = path.match(/^\/users\/([^/]+)$/);
    if (one && req.method() === 'PATCH') {
      if (state.stale) {
        const current = state.stale;
        state.stale = null;
        return json(route, 409, { error: 'stale_revision', current });
      }
      const who = user(one[1]);
      if (body.status === 'active' && who.noPassword) {
        return json(route, 409, {
          error: 'no_password',
          message: 'this account has never set a password; invite it instead',
        });
      }
      Object.assign(who, body.role ? { role: body.role } : {}, body.status ? { status: body.status } : {});
      who.revision += 1;
      return json(route, 200, who);
    }
    if (one && req.method() === 'DELETE') {
      state.users = state.users.filter((u) => u.id !== one[1]);
      return route.fulfill({ status: 204, body: '' });
    }

    // Everything else, /api/v1/plan included: this deployment has no database.
    return json(route, 404, { error: 'not_found' });
  });

  return state;
}

/** Opens the planner and waits for the session probe to have been answered. */
async function open(page, path = '/') {
  await page.goto(path);
  await expect
    .poll(() => page.evaluate(() => document.body.className), {
      message: 'auth.js records what the session probe answered on the body',
    })
    .toMatch(/accounts-none|signed-out|signed-in/);
}

/** Seeds a planner in localStorage, so a signed-in role has a ledger to act on. */
async function seedPlanner(page) {
  await page.addInitScript(
    ([key, payload]) => {
      try { if (!localStorage.getItem(key)) localStorage.setItem(key, payload); } catch (e) { /* not on the origin yet */ }
    },
    [
      STORAGE_KEY,
      JSON.stringify({
        ceiling: 10000,
        inflationPct: 0,
        fxRate: 0,
        splitEvenly: false,
        sponsors: [{ id: 'local-s1', code: 'Rose', name: 'Ada' }],
        budgetItems: [{ id: 'local-b1', item: 'Venue deposit', unit: 2500, qty: 1, paid: 500, sponsors: ['local-s1'], note: '' }],
        tasks: [
          { id: 'local-t1', name: 'Confirm the final guest count', owner: 'Ada', due: '', status: 'not-started' },
          { id: 'local-t2', name: 'Send the menu', owner: 'Grace', due: '', status: 'done' },
        ],
        notes: [],
      }),
    ],
  );
}

/* ------------------------------------------------------------------
 * Is there a door at all
 * ------------------------------------------------------------------ */

test('a deployment with no accounts draws no way in', async ({ page }) => {
  // No interception: the real server behind these tests has no /api/v1, so
  // the session probe genuinely 404s. That is the answer "this deployment has
  // no accounts", and it must not be treated as an error or as a sign-out.
  await open(page);
  await expect(page.locator('body')).toHaveClass(/accounts-none/);
  await expect(page.locator('#accountBar')).toBeHidden();

  // And the routes lead nowhere either, rather than to a form nothing is
  // behind.
  await page.goto('/#/login');
  await expect(page.locator('#authScreen')).toBeHidden();
  await expect(page.locator('#plannerWrap')).toBeVisible();
});

test('a visitor with accounts to sign in to is offered the door, and the planner still works', async ({ page }) => {
  await mountAccounts(page, { users: [ADA] });
  await open(page);

  await expect(page.locator('body')).toHaveClass(/signed-out/);
  await expect(page.locator('#accountActs button')).toHaveText(['Sign in']);
  // The page does not hide the planner from somebody signed out. The server is
  // what refuses them the plan; this browser's own copy is theirs to see.
  await expect(page.locator('#plannerWrap')).toBeVisible();
  await page.locator('#tab-budget').click();
  await expect(page.locator('#addBudgetRow')).toBeVisible();
  await expect(page.locator('#startCeiling')).toBeEnabled();
});

/* ------------------------------------------------------------------
 * Signing in
 * ------------------------------------------------------------------ */

test('signing in sends the address and password, and the page says who you are', async ({ page }) => {
  const server = await mountAccounts(page, { users: [ADA] });
  await open(page);

  await page.locator('#accountActs button').click();
  await expect(page.locator('#panelLogin')).toBeVisible();
  await expect(page.locator('#plannerWrap')).toBeHidden();

  await page.fill('#loginEmail', ADA.email);
  await page.fill('#loginPassword', PASSWORD);
  await page.click('#loginSubmit');

  await expect(page.locator('.account-email')).toHaveText(ADA.email);
  await expect(page.locator('.account-role')).toHaveText('admin');
  await expect(page.locator('#plannerWrap')).toBeVisible();
  await expect(page.locator('#authScreen')).toBeHidden();

  const login = server.calls.find((c) => c.path === '/auth/login');
  expect(login.body).toEqual({ email: ADA.email, password: PASSWORD });

  // The session is an HttpOnly cookie, so the page has no way to read it and
  // must not have kept a copy of anything either. The planner's own key is the
  // only thing this origin is allowed to be holding.
  const kept = await page.evaluate(() => [Object.keys(localStorage), Object.keys(sessionStorage)]);
  expect(kept[0].filter((k) => k !== STORAGE_KEY)).toEqual([]);
  expect(kept[1]).toEqual([]);
});

test('a refusal is reported without saying which half was wrong', async ({ page }) => {
  await mountAccounts(page, { users: [ADA] });
  await open(page, '/#/login');

  await page.fill('#loginEmail', ADA.email);
  await page.fill('#loginPassword', 'not-the-password');
  await page.click('#loginSubmit');

  await expect(page.locator('#loginMsg')).toHaveText('That email and password do not match an account.');
  // Still on the form, with the address kept so it can be tried again.
  await expect(page.locator('#panelLogin')).toBeVisible();
  await expect(page.locator('#loginEmail')).toHaveValue(ADA.email);
});

test('asking for a link answers the same way whoever asks', async ({ page }) => {
  const server = await mountAccounts(page, { users: [ADA] });
  await open(page, '/#/login');

  await page.fill('#loginEmail', 'nobody@example.test');
  await page.click('#loginForgot');

  await expect(page.locator('#loginMsg')).toContainText('If that address has an account');
  expect(server.calls.filter((c) => c.path === '/auth/password-reset')).toHaveLength(1);
});

test('signing out asks the server to end the session and puts the door back', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA] });
  await open(page, '/#/account');

  await page.click('#signOutBtn');

  await expect(page.locator('#accountActs button')).toHaveText(['Sign in']);
  expect(server.calls.some((c) => c.path === '/auth/logout' && c.method === 'POST')).toBe(true);
});

/* ------------------------------------------------------------------
 * The mailed link
 * ------------------------------------------------------------------ */

test('a set-password link is redeemed from the body, and the token leaves the URL', async ({ page }) => {
  const server = await mountAccounts(page, { users: [LINUS] });
  await page.goto(`/#/set-password?token=${TOKEN}`);
  await expect(page.locator('#panelSetPassword')).toBeVisible();

  // The whole reason the token travels in the fragment is that it reaches no
  // log. Leaving it in the address bar would put it in browser history and in
  // the next screenshot somebody takes.
  expect(page.url()).not.toContain(TOKEN);
  expect(page.url()).toContain('#/set-password');

  await page.fill('#newPassword', PASSWORD);
  await page.fill('#newPassword2', PASSWORD);
  await page.click('#setPasswordSubmit');

  // Straight to the form they need next, rather than a screen whose one
  // button leads to it, and what just happened is still on screen while they
  // use it.
  await expect(page.locator('#panelLogin')).toBeVisible();
  await expect(page.locator('#authLede')).toContainText('Your password is saved');

  const sent = server.calls.find((c) => c.path === '/auth/set-password');
  expect(sent.body).toEqual({ token: TOKEN, password: PASSWORD });
  // In the body, never the query string.
  expect(sent.search).toBe('');
});

test('the password being chosen can be looked at, and is covered up again by itself', async ({ page }) => {
  await mountAccounts(page, { users: [LINUS] });
  await page.goto(`/#/set-password?token=${TOKEN}`);

  // Twelve characters or more, typed on a phone, twice, with nothing to check
  // them against: this is the way to check them.
  await page.fill('#newPassword', PASSWORD);
  await expect(page.locator('#newPassword')).toHaveAttribute('type', 'password');

  await page.click('#showPassword');
  await expect(page.locator('#newPassword')).toHaveAttribute('type', 'text');
  await expect(page.locator('#newPassword2')).toHaveAttribute('type', 'text');
  await expect(page.locator('#showPassword')).toHaveAttribute('aria-pressed', 'true');
  await expect(page.locator('#showPassword')).toHaveText('Hide password');

  await page.click('#showPassword');
  await expect(page.locator('#newPassword')).toHaveAttribute('type', 'password');
  await expect(page.locator('#showPassword')).toHaveText('Show password');

  // And it does not stay on for the next person to arrive at this screen.
  await page.click('#showPassword');
  await page.goto(`/#/set-password?token=${TOKEN}`);
  await expect(page.locator('#newPassword')).toHaveAttribute('type', 'password');
  await expect(page.locator('#showPassword')).toHaveAttribute('aria-pressed', 'false');
});

test('a password that cannot work is refused before it costs a round trip', async ({ page }) => {
  const server = await mountAccounts(page, { users: [LINUS] });
  await page.goto(`/#/set-password?token=${TOKEN}`);

  await page.fill('#newPassword', 'short');
  await page.fill('#newPassword2', 'short');
  await page.click('#setPasswordSubmit');
  await expect(page.locator('#setPasswordMsg')).toHaveText('A password needs at least 12 characters.');

  await page.fill('#newPassword', PASSWORD);
  await page.fill('#newPassword2', PASSWORD + '-not');
  await page.click('#setPasswordSubmit');
  await expect(page.locator('#setPasswordMsg')).toHaveText('The two passwords are not the same.');

  // Neither attempt was sent: a link is allowed a limited number of
  // redemptions, and a typo should not spend one.
  expect(server.calls.filter((c) => c.path === '/auth/set-password')).toHaveLength(0);
});

test('a spent or expired link says what to do next', async ({ page }) => {
  await mountAccounts(page, { users: [LINUS] });
  await page.goto('/#/set-password?token=some-other-token');

  await page.fill('#newPassword', PASSWORD);
  await page.fill('#newPassword2', PASSWORD);
  await page.click('#setPasswordSubmit');

  await expect(page.locator('#setPasswordMsg')).toContainText('expired or has already been used');
});

test('a link with no token in it says so rather than failing at the server', async ({ page }) => {
  await mountAccounts(page, { users: [LINUS] });
  await page.goto('/#/set-password');

  await expect(page.locator('#setPasswordMsg')).toContainText('incomplete');
  await expect(page.locator('#setPasswordSubmit')).toBeDisabled();
});

/* ------------------------------------------------------------------
 * People
 * ------------------------------------------------------------------ */

test('the admin panel lists everyone, and offers nothing on your own row that the server would refuse', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA, GRACE, LINUS] });
  await open(page, '/#/admin');

  await expect(page.locator('#peopleList .person')).toHaveCount(3);
  const mine = page.locator('.person', { hasText: ADA.email });
  await expect(mine.locator('.person-you')).toHaveText('you');
  // Changing your own role or status, and deleting your own account, are all
  // refused server-side — the way back from demoting the only admin is
  // another admin.
  await expect(mine.locator('select.person-role')).toHaveCount(0);
  await expect(mine.getByText('Remove')).toHaveCount(0);
  await expect(mine.getByText('Turn off access')).toHaveCount(0);

  // An invited account reads as invited, an active one as active.
  await expect(page.locator('.person', { hasText: LINUS.email }).locator('.person-status')).toHaveText('invited');
  await expect(page.locator('.person', { hasText: GRACE.email }).locator('.person-status')).toHaveText('active');
});

test('adding somebody surfaces the link when there is no mail to send it by', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA], mailSent: false });
  await open(page, '/#/admin');

  await page.fill('#newUserEmail', 'grace@example.test');
  await page.selectOption('#newUserRole', 'editor');
  await page.click('#createUserSubmit');

  // Without this the deployment has no way to onboard anybody at all.
  await expect(page.locator('#adminLinkOut')).toBeVisible();
  await expect(page.locator('#adminLinkValue')).toHaveValue(new RegExp(`#/set-password\\?token=${TOKEN}$`));
  await expect(page.locator('#adminLinkLede')).toContainText('grace@example.test');
  await expect(page.locator('#peopleList .person')).toHaveCount(2);

  const made = server.calls.find((c) => c.path === '/users' && c.method === 'POST');
  expect(made.body).toEqual({ email: 'grace@example.test', role: 'editor' });

  // Dismissed, and gone from the page with it.
  await page.click('#adminLinkDismiss');
  await expect(page.locator('#adminLinkOut')).toBeHidden();
  await expect(page.locator('#adminLinkValue')).toHaveValue('');
});

test('with mail configured the link is not shown to the admin at all', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA], mailSent: true });
  await open(page, '/#/admin');

  await page.fill('#newUserEmail', 'grace@example.test');
  await page.click('#createUserSubmit');

  await expect(page.locator('#adminMsg')).toContainText('on its way to grace@example.test');
  await expect(page.locator('#adminLinkOut')).toBeHidden();
});

test('every write carries the revision it was made against', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE] });
  await open(page, '/#/admin');

  const row = page.locator('.person', { hasText: GRACE.email });
  await row.locator('select.person-role').selectOption('admin');
  await expect(row.locator('select.person-role')).toHaveValue('admin');

  const patch = server.calls.find((c) => c.method === 'PATCH');
  expect(patch.body).toEqual({ revision: GRACE.revision, role: 'admin' });

  // The revision moved with the answer, so the next write is made against the
  // row as it now stands rather than as it was drawn.
  await row.getByText('Turn off access').click();
  await expect(row.locator('.person-status')).toHaveText('no access');
  const patches = server.calls.filter((c) => c.method === 'PATCH');
  expect(patches[1].body).toEqual({ revision: GRACE.revision + 1, status: 'disabled' });

  // A disabled account is not offered a link: the server refuses one, and
  // saying "turn access back on first" is more use than a refusal.
  await expect(row.getByText('Send a password link')).toHaveCount(0);
});

test('removing an account asks first and sends the revision in the query', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE] });
  await open(page, '/#/admin');

  page.on('dialog', (d) => d.accept().catch(() => {}));
  await page.locator('.person', { hasText: GRACE.email }).getByText('Remove').click();

  await expect(page.locator('#peopleList .person')).toHaveCount(1);
  await expect(page.locator('#adminMsg')).toContainText('Removed grace@example.test');
  const gone = server.calls.find((c) => c.method === 'DELETE');
  expect(gone.search).toBe(`?revision=${GRACE.revision}`);
});

test("another admin's change is adopted rather than overwritten", async ({ page }) => {
  const moved = { ...GRACE, role: 'viewer', revision: GRACE.revision + 5 };
  await mountAccounts(page, { session: ADA, users: [ADA, GRACE], stale: moved });
  await open(page, '/#/admin');

  const row = page.locator('.person', { hasText: GRACE.email });
  await row.locator('select.person-role').selectOption('admin');

  // The refusal carries the row as it now stands, so the list shows the truth
  // instead of this browser's guess — and says why it changed under them.
  await expect(row.locator('select.person-role')).toHaveValue('viewer');
  await expect(page.locator('#adminMsg')).toContainText('Somebody else changed that account first');
});

test('an account that is already gone comes off the list instead of refusing for ever', async ({ page }) => {
  const MARIE = { ...GRACE, id: 'a0000000-0000-4000-8000-000000000004', email: 'marie@example.test' };
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE, LINUS, MARIE] });
  await open(page, '/#/admin');
  await expect(page.locator('#peopleList .person')).toHaveCount(4);

  // Another admin, or this one in another tab, removed all three while this
  // screen still had them drawn. Every write to a row that is not there is
  // refused the same way, so "try again" is an instruction that cannot be
  // followed.
  server.users = server.users.filter((u) => u.id === ADA.id);

  await page.locator('.person', { hasText: GRACE.email }).getByText('Turn off access').click();
  await expect(page.locator('#adminMsg')).toContainText('no longer exists');
  await expect(page.locator('.person', { hasText: GRACE.email })).toHaveCount(0);

  // The same answer to the same question from the other two buttons a row
  // carries: a fresh link, and removing it.
  await page.locator('.person', { hasText: LINUS.email }).getByText('Send the link again').click();
  await expect(page.locator('.person', { hasText: LINUS.email })).toHaveCount(0);

  page.on('dialog', (d) => d.accept().catch(() => {}));
  await page.locator('.person', { hasText: MARIE.email }).getByText('Remove').click();
  await expect(page.locator('.person', { hasText: MARIE.email })).toHaveCount(0);
  await expect(page.locator('#peopleList .person')).toHaveCount(1);
});

test('a session that ended while the people screen was open opens the sign-in form', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE] });
  await open(page, '/#/admin');
  await expect(page.locator('#peopleList .person')).toHaveCount(2);

  // It expired, or somebody signed out in another tab. Saying "sign in again"
  // on a screen with nothing to sign in with is the failure the planner had
  // removed from it already.
  server.session = null;

  await page.locator('.person', { hasText: GRACE.email }).getByText('Turn off access').click();
  await expect(page.locator('#panelLogin')).toBeVisible();
  await expect(page.locator('#authLede')).toContainText('Your session has ended');
  await expect(page.locator('#panelAdmin')).toBeHidden();
});

/* ------------------------------------------------------------------
 * Activity
 * ------------------------------------------------------------------ */

test('an activity list that would not load offers something to try again with', async ({ page }) => {
  const server = await mountAccounts(page, {
    session: ADA, users: [ADA], activity: [ENTRY], activityFails: 1,
  });
  await open(page, '/#/activity');

  // Nothing is listed, so there is no "Show older" to press and no way back
  // to this screen from a screen it hides. Without a control here the only
  // way to ask again is to leave and come back.
  await expect(page.locator('#activityMsg')).toContainText('Could not load the activity');
  const again = page.locator('#activityMore');
  await expect(again).toBeVisible();
  await expect(again).toHaveText('Try again');

  await again.click();
  await expect(page.locator('#activityList .activity')).toHaveCount(1);
  // And the refusal goes with the load that worked, rather than staying under
  // the list it is no longer about.
  await expect(page.locator('#activityMsg')).toHaveText('');
  expect(server.calls.filter((c) => c.path.startsWith('/activity'))).toHaveLength(2);
});

test("an account with no password cannot be switched on, and the server's reason is shown", async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA, { ...LINUS, status: 'disabled', noPassword: true }] });
  await open(page, '/#/admin');

  await page.locator('.person', { hasText: LINUS.email }).getByText('Turn access back on').click();
  await expect(page.locator('#adminMsg')).toContainText('never set a password');
});

/* ------------------------------------------------------------------
 * Roles shape the page
 * ------------------------------------------------------------------ */

test('an editor is not offered the admin panel, and cannot reach it by URL either', async ({ page }) => {
  await mountAccounts(page, { session: GRACE, users: [ADA, GRACE] });
  await open(page);

  await expect(page.locator('#accountActs')).not.toContainText('People');
  await expect(page.locator('#accountActs')).toContainText('Your account');

  await page.goto('/#/admin');
  await expect(page.locator('#panelNote')).toBeVisible();
  await expect(page.locator('#authNote')).toContainText("Managing accounts is an admin's job");
  await expect(page.locator('#panelAdmin')).toBeHidden();
});

test('an editor keeps every control the ledger has', async ({ page }) => {
  await seedPlanner(page);
  await mountAccounts(page, { session: GRACE, users: [GRACE] });
  await open(page);

  await page.locator('#tab-budget').click();
  await expect(page.locator('#addBudgetRow')).toBeVisible();
  await expect(page.locator('#budgetBody tr').first().locator('textarea').first()).not.toHaveAttribute('readonly', '');
});

test('a viewer is shown the ledger and offered nothing to change it with', async ({ page }) => {
  await seedPlanner(page);
  await mountAccounts(page, { session: { ...LINUS, status: 'active' }, users: [LINUS] });
  await open(page);

  await expect(page.locator('body')).toHaveClass(/role-viewer/);
  await expect(page.locator('.account-role')).toHaveText('viewer, read-only');

  await page.locator('#tab-budget').click();
  // Hidden, not merely inert: a button that does nothing is worse than no
  // button.
  await expect(page.locator('#addBudgetRow')).toBeHidden();
  await expect(page.locator('#budgetBody .del-btn').first()).toBeHidden();
  await expect(page.locator('#importData')).toBeDisabled();

  // The figures are still readable, selectable and copyable — read-only
  // rather than disabled, which is the rule an archived ledger already
  // follows.
  const row = page.locator('#budgetBody tr').first();
  await expect(row.locator('textarea').first()).toHaveAttribute('readonly', '');
  await expect(row.locator('input').first()).toHaveAttribute('readonly', '');
  await expect(row.locator('button.by-btn')).toBeDisabled();
  await expect(row.locator('td').nth(3)).toHaveText('€2,500');

  // Export stays open. A ledger nobody can take a copy of is a worse ledger.
  await expect(page.locator('#exportData')).toBeEnabled();
});

test("a viewer's lock survives the page rebuilding its own rows", async ({ page }) => {
  await seedPlanner(page);
  await mountAccounts(page, { session: { ...LINUS, status: 'active' }, users: [LINUS] });
  await open(page);

  await page.locator('#tab-tasks').click();
  await expect(page.locator('#tasksBody tr')).toHaveCount(2);

  // Filtering rebuilds every row from scratch. Controls that did not exist
  // when the lock was applied have to come back locked, or a viewer gets an
  // editable ledger by pressing a filter.
  await page.locator('#taskFilters button[data-filter="done"]').click();
  await expect(page.locator('#tasksBody tr')).toHaveCount(1);

  const row = page.locator('#tasksBody tr').first();
  await expect(row.locator('input').first()).toHaveAttribute('readonly', '');
  await expect(row.locator('select.status-select')).toBeDisabled();
});

test('a deployment without passkeys does not offer them', async ({ page }) => {
  // The instance behind this spec sets no base URL, so the server derived no
  // relying party and mounted no passkey routes. The config block says so,
  // and the button that would 404 is never drawn.
  await mountAccounts(page, { users: [ADA] });
  await open(page, '/#/login');

  expect(await page.evaluate(() => JSON.parse(document.getElementById('soiree-config').textContent).passkeys)).toBe(false);
  await expect(page.locator('#loginPasskey')).toBeHidden();
});

/*
 * The accounts screens are not English-only.
 *
 * They were, for a release in which the planner behind them was already in
 * three languages — so the first screen somebody invited to a planner ever saw
 * was the one that was not in theirs. auth.js keeps its own table, and takes
 * the choice of language from the <html lang> app.js has already set, so the
 * two can never disagree about which language the page is in.
 */

/** The string table, read out of the source rather than copied into the test. */
function authStrings() {
  const src = fs.readFileSync(path.join(__dirname, '..', '..', 'web', 'src', 'auth.js'), 'utf8');
  const start = src.indexOf('var STRINGS = {');
  const end = src.indexOf('\n  };', start);
  expect(start, 'auth.js should hold a STRINGS table').toBeGreaterThan(-1);
  // A literal of single-quoted strings and nothing else, so it can simply be
  // evaluated; this is the repository's own source, not input.
  return new Function(`return ${src.slice(start + 'var STRINGS = '.length, end + '\n  }'.length)};`)();
}

test('every accounts string exists in all three languages, with the same blanks to fill', () => {
  const table = authStrings();
  expect(Object.keys(table).sort()).toEqual(['en', 'id', 'nl']);

  const blanks = (str) => (str.match(/\{\w+\}/g) || []).sort().join(' ');
  const problems = [];
  for (const key of Object.keys(table.en)) {
    for (const lang of ['nl', 'id']) {
      const str = table[lang][key];
      if (typeof str !== 'string' || !str.trim()) problems.push(`${lang} has no ${key}`);
      // "{email}" left out of a translation is a sentence with a hole in it,
      // and one spelt differently is a sentence with the braces still showing.
      else if (blanks(str) !== blanks(table.en[key])) problems.push(`${lang} ${key} fills different blanks`);
    }
  }
  for (const lang of ['nl', 'id']) {
    for (const key of Object.keys(table[lang])) {
      if (!(key in table.en)) problems.push(`${lang} has ${key}, which English does not`);
    }
  }
  expect(problems).toEqual([]);
});

test('every sentence the reminders switch can choose is one that was written, and the other way round', () => {
  // The switch picks its sentence by state and situation from a table of keys.
  // A key nobody wrote is drawn as the key; a sentence nothing picks is three
  // translations of something nobody will read.
  const src = fs.readFileSync(path.join(__dirname, '..', '..', 'web', 'src', 'auth.js'), 'utf8');
  const start = src.indexOf('var REMINDER_LINES = {');
  const end = src.indexOf('\n  };', start);
  expect(start, 'auth.js should hold a REMINDER_LINES table').toBeGreaterThan(-1);
  const chosen = Object.values(new Function(`return ${src.slice(start + 'var REMINDER_LINES = '.length, end + '\n  }'.length)};`)());

  const written = Object.keys(authStrings().en);
  expect(chosen.filter((key) => !written.includes(key))).toEqual([]);
  const sentences = written.filter((key) => /^rem\./.test(key) && !/^rem\.(title|body|turnon|turnoff|failed)$/.test(key));
  expect(sentences.filter((key) => !chosen.includes(key))).toEqual([]);
});

test('the sign-in screen is in Dutch when the page is', async ({ page }) => {
  await mountAccounts(page, { users: [ADA] });
  await open(page, '/?lang=nl#/login');

  await expect(page.locator('html')).toHaveAttribute('lang', 'nl');
  await expect(page.locator('#authTitle')).toHaveText('Aanmelden');
  await expect(page.locator('#authLede')).toHaveText('Meld je aan met het adres waarop je de uitnodiging hebt ontvangen.');
  await expect(page.locator('label[for="loginEmail"]')).toHaveText('E-mailadres');
  await expect(page.locator('label[for="loginPassword"]')).toHaveText('Wachtwoord');
  await expect(page.locator('#loginSubmit')).toHaveText('Aanmelden');
  await expect(page.locator('#loginForgot')).toHaveText('Mail me een link om een nieuw wachtwoord in te stellen');
  await expect(page.locator('#accountActs button')).toHaveText(['Aanmelden']);

  // What the page says by itself, and what it says about a refusal.
  await page.click('#loginSubmit');
  await expect(page.locator('#loginMsg')).toHaveText('Vul zowel je e-mailadres als je wachtwoord in.');
  await page.fill('#loginEmail', ADA.email);
  await page.fill('#loginPassword', 'not-the-password');
  await page.click('#loginSubmit');
  await expect(page.locator('#loginMsg')).toHaveText('Dat e-mailadres en wachtwoord horen niet bij een account.');
});

test('an invitation opened in Indonesian is in Indonesian from the first screen', async ({ page }) => {
  await mountAccounts(page, { users: [ADA] });
  // The link as the mail carries it, with the language an operator can add.
  await page.goto(`/?lang=id#/set-password?token=${TOKEN}`);

  await expect(page.locator('#authTitle')).toHaveText('Buat kata sandi');
  await expect(page.locator('label[for="newPassword"]')).toHaveText('Kata sandi baru');
  await expect(page.locator('#setPasswordSubmit')).toHaveText('Simpan kata sandi');
  await expect(page.locator('#showPassword')).toHaveText('Tampilkan kata sandi');
  expect(page.url()).not.toContain('token=');

  await page.fill('#newPassword', 'pendek');
  await page.fill('#newPassword2', 'pendek');
  await page.click('#setPasswordSubmit');
  await expect(page.locator('#setPasswordMsg')).toHaveText('Kata sandi harus minimal 12 karakter.');

  await page.fill('#newPassword', PASSWORD);
  await page.fill('#newPassword2', PASSWORD);
  await page.click('#setPasswordSubmit');
  // And the sign-in form it leads to, still saying what just happened.
  await expect(page.locator('#authTitle')).toHaveText('Masuk');
  await expect(page.locator('#authLede'))
    .toHaveText('Kata sandimu sudah tersimpan. Masuk dengan kata sandi itu untuk membuka perencana.');
});

test('no accounts screen leaves English, or a raw key, showing in another language', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA, GRACE, LINUS] });
  await open(page, '/?lang=id#/admin');
  await expect(page.locator('#peopleList .person')).toHaveCount(3);

  // Words that are the same in both languages are not evidence of anything,
  // so the check is for sentences and labels that could only be English.
  const english = [
    'Sign in', 'Sign out', 'Your account', 'People', 'Add person', 'Role',
    'Back to the planner', 'Send a password link', 'Send the link again',
    'Turn off access', 'Remove', 'added ', 'invited', 'active', 'Everyone with',
    'A viewer reads', 'Viewer', 'Editor', 'read-only', 'you',
  ];
  const rawKey = /(^|\s)[a-z]+(\.[a-z_-]+)+(\s|$)/;

  async function check(where) {
    // innerText: what is on screen, so a hidden panel's English is not counted
    // against the one that is showing.
    const text = await page.evaluate(() =>
      [document.getElementById('authScreen'), document.getElementById('accountBar')]
        .map((el) => (el ? el.innerText : '')).join('\n'));
    // Addresses are data, and they are made of lowercase words and dots.
    const prose = text.replace(/\S+@\S+/g, '');
    for (const phrase of english) {
      const re = new RegExp(`(^|[^A-Za-z])${phrase.trim()}([^A-Za-z]|$)`);
      expect(re.test(prose), `${where} still shows English: "${phrase.trim()}"\n${prose}`).toBe(false);
    }
    expect(rawKey.test(prose), `${where} shows a raw key\n${prose}`).toBe(false);
  }

  await expect(page.locator('#authTitle')).toHaveText('Anggota');
  await expect(page.locator('.person-you')).toHaveText('kamu');
  await expect(page.locator('.person', { hasText: LINUS.email }).locator('.person-status')).toHaveText('diundang');
  await check('the people screen');

  await page.goto('/?lang=id#/account');
  await expect(page.locator('#authTitle')).toHaveText('Akunmu');
  await expect(page.locator('#authLede')).toHaveText(`${ADA.email}, masuk sebagai admin.`);
  await expect(page.locator('#signOutBtn')).toHaveText('Keluar');
  await check('the account screen');
});

test('a refusal the server words in English is reworded, not passed through', async ({ page }) => {
  // no_password comes back with a message, in English, written for a person.
  // To somebody reading Indonesian it is a line of another language in the
  // middle of their screen; the code is what the page should act on.
  await mountAccounts(page, { session: ADA, users: [ADA, { ...LINUS, status: 'disabled', noPassword: true }] });
  await open(page, '/?lang=id#/admin');

  const row = page.locator('.person', { hasText: LINUS.email });
  await row.getByRole('button', { name: 'Aktifkan lagi akses' }).click();
  await expect(page.locator('#adminMsg')).toHaveText(
    'Akun itu belum pernah membuat kata sandi, jadi tidak bisa diaktifkan. Kirimi tautan saja.');
  await expect(page.locator('#adminMsg')).not.toContainText('never set a password');
});

/*
 * A deployment has one locale, and the people using it do not have one
 * language. The admin creating an account is the one person who knows what the
 * person they are inviting reads, so the language of the invitation is chosen
 * there — for that mail, and the screen its link opens. It is not stored: after
 * that first mail the page follows the reader's own browser, which is better
 * evidence than anything an admin typed once, and theirs to change.
 */
test('the invite form sends the language the admin chose, and nothing when they chose nothing', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA] });
  await open(page, '/#/admin');

  // The first choice says what "nothing chosen" means on this deployment.
  await expect(page.locator('#newUserLanguage option')).toHaveText([
    'Same as the planner (English)', 'English', 'Nederlands', 'Bahasa Indonesia',
  ]);

  await page.fill('#newUserEmail', 'grace@example.test');
  await page.selectOption('#newUserLanguage', 'nl');
  await page.click('#createUserSubmit');
  await expect(page.locator('#adminLinkOut')).toBeVisible();
  let made = server.calls.filter((c) => c.method === 'POST' && c.path === '/users').pop();
  expect(made.body).toEqual({ email: 'grace@example.test', role: 'editor', language: 'nl' });

  // Left alone, the key is not sent at all.
  await page.click('#adminLinkDismiss');
  await page.fill('#newUserEmail', 'linus@example.test');
  await page.selectOption('#newUserLanguage', '');
  await page.click('#createUserSubmit');
  await expect(page.locator('#adminLinkOut')).toBeVisible();
  made = server.calls.filter((c) => c.method === 'POST' && c.path === '/users').pop();
  expect(made.body).toEqual({ email: 'linus@example.test', role: 'editor' });
});

test('sending a link again takes the language chosen beside it, and choosing saves nothing', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE] });
  await open(page, '/#/admin');
  const row = page.locator('.person', { hasText: GRACE.email });
  const invites = () => server.calls.filter((c) => c.method === 'POST' && c.path === `/users/${GRACE.id}/invite`);

  await row.locator('.person-language').selectOption('id');
  await row.getByRole('button', { name: 'Send a password link' }).click();
  await expect.poll(() => invites().length).toBe(1);
  expect(invites()[0].body).toEqual({ language: 'id' });

  // The choice was about that mail. It is not a setting on the account, so
  // picking one writes nothing, and the list comes back with none picked.
  expect(server.calls.filter((c) => c.method === 'PATCH')).toEqual([]);
  await expect(page.locator('.person', { hasText: GRACE.email }).locator('.person-language')).toHaveValue('');

  await page.click('#adminLinkDismiss');
  await page.locator('.person', { hasText: GRACE.email }).getByRole('button', { name: 'Send a password link' }).click();
  await expect.poll(() => invites().length).toBe(2);
  expect(invites()[1].body).toEqual({});
});

test('asking for a reset link says which language the screen is being read in', async ({ page }) => {
  // Nobody else is involved in a reset, and the server has never seen this
  // browser: the page is the only one who can say.
  const server = await mountAccounts(page, { users: [ADA] });
  await open(page, '/?lang=nl#/login');
  await page.fill('#loginEmail', ADA.email);
  await page.click('#loginForgot');
  await expect(page.locator('#loginMsg')).toContainText('Als er een account bij dat adres hoort');
  const asked = server.calls.filter((c) => c.path === '/auth/password-reset').pop();
  expect(asked.body).toEqual({ email: ADA.email, language: 'nl' });
});

test('the switcher is there on the sign-in screen, and turns it too', async ({ page }) => {
  // Where it matters most: the first screen somebody invited here sees, and
  // the one they cannot get past if they cannot read it.
  await mountAccounts(page, { users: [ADA] });
  await open(page, '/#/login');
  await expect(page.locator('#authTitle')).toHaveText('Sign in');
  await expect(page.locator('#langSwitch')).toBeVisible();

  await page.getByRole('button', { name: 'Bahasa Indonesia' }).click();
  await expect(page.locator('#authTitle')).toHaveText('Masuk');
  await expect(page.locator('label[for="loginPassword"]')).toHaveText('Kata sandi');
  await expect(page.locator('#loginSubmit')).toHaveText('Masuk');
  await expect(page.locator('#accountActs button')).toHaveText(['Masuk']);
});

/* ------------------------------------------------------------------
 * Where focus is left
 * ------------------------------------------------------------------
 * The list is rebuilt wholesale rather than patched, and a rebuild takes the
 * focused element with it: the browser then has nowhere to put focus and
 * drops it on <body>, where a screen reader loses its place and the next Tab
 * starts at the language flags. Both cases below are keyboard journeys that
 * ended there.
 * ------------------------------------------------------------------ */

test('changing a role leaves focus on the select that changed it', async ({ page }) => {
  const server = await mountAccounts(page, { session: ADA, users: [ADA, GRACE, LINUS] });
  await open(page, '/#/admin');

  const role = page.locator('.person', { hasText: GRACE.email }).locator('select.person-role');
  await role.focus();
  await role.selectOption('admin');

  // The write went out and the list has been rebuilt with the answer, so the
  // select being asked about is the new node rather than the one pressed.
  await expect.poll(() => server.calls.filter((c) => c.method === 'PATCH').length).toBe(1);
  await expect(role).toHaveValue('admin');
  await expect(role).toBeFocused();
});

test('removing somebody lands on the row that took their place, never on a Remove', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA, GRACE, LINUS] });
  await open(page, '/#/admin');
  page.on('dialog', (d) => d.accept().catch(() => {}));

  await page.locator('.person', { hasText: GRACE.email }).getByRole('button', { name: 'Remove' }).click();
  await expect(page.locator('#peopleList .person')).toHaveCount(2);

  // Grace was the second row, so Linus is now. His first control, not his
  // last: somebody who has just removed one person should not be one
  // keystroke from removing the next.
  await expect(page.locator('.person', { hasText: LINUS.email }).locator('select.person-role')).toBeFocused();
  expect(await page.evaluate(() => document.activeElement.className)).not.toContain('danger');
});

/* ------------------------------------------------------------------
 * Being told the screen changed
 * ------------------------------------------------------------------
 * The planner and the accounts screens are two halves of one page, and the
 * button that swaps them is always in the half that goes away. Nothing moved,
 * nothing was said, and the tab kept its name, so for somebody who cannot see
 * the swap it did not happen.
 * ------------------------------------------------------------------ */

test('opening the accounts screen moves focus onto it, names the tab, and leaves one landmark', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA] });
  await open(page);

  await page.getByRole('button', { name: 'People' }).click();
  await expect(page.locator('#panelAdmin')).toBeVisible();

  await expect(page.locator('#authTitle')).toBeFocused();
  await expect(page).toHaveTitle('People · Rehearsal Dinner (e2e)');
  // The planner is display:none behind it, so exactly one main landmark is in
  // the accessibility tree and "skip to the content" has one destination.
  const main = page.locator('[role="main"]:visible');
  await expect(main).toHaveCount(1);
  await expect(main).toHaveAttribute('id', 'authScreen');
});

test('going back to the planner moves focus onto it, and gives the tab its name back', async ({ page }) => {
  await mountAccounts(page, { session: ADA, users: [ADA] });
  await open(page, '/#/admin');

  await page.getByRole('button', { name: 'Back to the planner' }).click();
  await expect(page.locator('#plannerWrap')).toBeVisible();

  await expect(page.locator('.masthead h1')).toBeFocused();
  await expect(page).toHaveTitle('Rehearsal Dinner (e2e)');
  const main = page.locator('[role="main"]:visible');
  await expect(main).toHaveCount(1);
  await expect(main).toHaveAttribute('id', 'plannerWrap');
});

test('the sentence a screen leads with is announced, not only drawn', async ({ page }) => {
  // The first flow an invited person goes through ends on the sign-in form
  // with "your password is saved" as its lede. Focus belongs in the email
  // field there, so that sentence has to reach a screen reader by itself.
  await mountAccounts(page, { users: [LINUS] });
  await page.goto(`/#/set-password?token=${TOKEN}`);
  await page.fill('#newPassword', PASSWORD);
  await page.fill('#newPassword2', PASSWORD);
  await page.click('#setPasswordSubmit');

  await expect(page.locator('#panelLogin')).toBeVisible();
  await expect(page.locator('#authLede')).toHaveAttribute('role', 'status');
  await expect(page.locator('#authLede')).toContainText('Your password is saved');
  await expect(page.locator('#loginEmail')).toBeFocused();
});
