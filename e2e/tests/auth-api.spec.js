// @ts-check
/*
 * The accounts interface against the real server.
 *
 * auth-ui.spec.js proves what the page sends and draws, against a scripted
 * server. This proves the two halves actually fit: a real Argon2 login, a real
 * session cookie, a real single-use link redeemed against a real token, and a
 * real WebAuthn ceremony verified by the library. Nothing here is stubbed.
 *
 * A SECOND SPEC THAT TOUCHES THE API, WHICH api.spec.js SAYS NOT TO WRITE.
 * That rule exists because the *plan* is shared state with no isolation to be
 * had, and two files editing it concurrently would produce failures that look
 * like application bugs. This file never touches the plan: it reads and writes
 * accounts, sessions and passkeys, which api.spec.js never looks at, and it
 * makes each account it needs under an address of its own. It also takes its
 * browsers out of the plan altogether — see isolateFromPlan below, and read it
 * before assuming that is belt and braces: it is not, and the failure it
 * prevents lands on the other file. The two files share a database and are
 * independent in it. If that ever stops being true, merge this into
 * api.spec.js rather than loosening the rule.
 *
 * Serial for the same reason api.spec.js is: the accounts table is shared
 * between the tests in this file, and the server's rate limiters are shared
 * with every other request in the run. Both are reasons to go one at a time
 * and to keep the number of real sign-ins small — the per-account budget is
 * ten in fifteen minutes, which a chatty spec would spend on its own.
 *
 * With no Docker the launcher starts this instance without a database; there
 * is then no accounts surface at all and every test here skips itself, exactly
 * as the API specs do.
 */
const { test, expect } = require('@playwright/test');

const { AUTH_URL } = require('../servers');

// Set in playwright.config.js, which is also where the server is told about
// them. Read from metadata rather than repeated, so there is one place to
// change.
const admin = (testInfo) => ({
  email: String(testInfo.config.metadata.adminEmail),
  password: String(testInfo.config.metadata.adminPassword),
});

// Every account this file makes gets an address nobody else will, so a rerun
// against a database that kept the last one does not collide.
const stamp = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 6);

test.use({ baseURL: AUTH_URL });
test.describe.configure({ mode: 'serial' });

/**
 * Takes a browser context out of the shared plan.
 *
 * The planner half of the page asks /api/v1/plan at startup and, if it is
 * holding a cached planner that the server does not have, seeds the server
 * with it — which is the right behaviour when a database is added to a
 * deployment that was running without one, and a menace here. api.spec.js
 * empties the plan between its tests; a page of ours that cached it a moment
 * earlier would put those rows back, in another file, under another worker,
 * and the failure would land on a test that did nothing wrong. It has
 * happened: two budget items and a tag that no longer resolved.
 *
 * Answering the planner's own probe 404 is exactly what that page sees on a
 * deployment with no database, so it stays local and writes nothing. Every
 * accounts route still goes to the real server, which is what this file is
 * about.
 */
async function isolateFromPlan(context) {
  await context.route('**/api/v1/plan', (route) =>
    route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not_found"}' }));
}

test.beforeEach(async ({ page, request }) => {
  await isolateFromPlan(page.context());
  // 404 means the routes are not mounted: no database, so no accounts.
  // 401 means there are accounts and this request has no session, which is the
  // ordinary answer and the one we want.
  const res = await request.get(`${AUTH_URL}/api/v1/auth/session`);
  test.skip(res.status() === 404, 'this instance has no database — Docker was not available');
  expect(res.status(), 'an unauthenticated session probe').toBe(401);
});

/** Signs in through the form, the way a person does. */
async function signIn(page, who) {
  await page.goto('/#/login');
  await expect(page.locator('#panelLogin')).toBeVisible();
  await page.fill('#loginEmail', who.email);
  await page.fill('#loginPassword', who.password);
  await page.click('#loginSubmit');
  await expect(page.locator('.account-email')).toHaveText(who.email);
}

test('a real sign-in produces a session the browser keeps and cannot read', async ({ page }, testInfo) => {
  const who = admin(testInfo);
  await signIn(page, who);
  await expect(page.locator('.account-role')).toHaveText('admin');

  // The cookie is HttpOnly, so the page cannot see it — which is what stops an
  // XSS anywhere on this origin from becoming a stolen session.
  const cookies = await page.context().cookies();
  const session = cookies.find((c) => c.name === 'soiree_session');
  expect(session, 'the server set a session cookie').toBeTruthy();
  expect(session.httpOnly).toBe(true);
  expect(session.sameSite).toBe('Lax');
  expect(await page.evaluate(() => document.cookie)).not.toContain('soiree_session');

  // It survives a reload, which is the whole point of a session: the page asks
  // the server who it is and is told.
  await page.reload();
  await expect(page.locator('.account-email')).toHaveText(who.email);

  // And signing out ends it at the server, not just in this tab.
  await page.goto('/#/account');
  await page.click('#signOutBtn');
  await expect(page.locator('#accountActs button')).toHaveText(['Sign in']);
  expect((await page.context().request.get(`${AUTH_URL}/api/v1/auth/session`)).status()).toBe(401);
});

test('an admin invites somebody, and the link in the invitation is what lets them in', async ({ page, browser }, testInfo) => {
  await signIn(page, admin(testInfo));

  const invited = `linus-${stamp()}@example.test`;
  await page.goto('/#/admin');
  await expect(page.locator('#peopleList .person')).not.toHaveCount(0);
  await page.fill('#newUserEmail', invited);
  await page.selectOption('#newUserRole', 'viewer');
  await page.click('#createUserSubmit');

  // This deployment has no SMTP, so the server hands the link back rather than
  // mailing it, and the admin passes it on. Without this there is no way to
  // onboard anybody at all.
  await expect(page.locator('#adminLinkOut')).toBeVisible();
  const link = await page.inputValue('#adminLinkValue');
  expect(link).toContain(`${AUTH_URL}/#/set-password?token=`);
  await expect(page.locator('.person', { hasText: invited }).locator('.person-status')).toHaveText('invited');

  // A browser that has never been here follows the link out of the mail.
  const theirs = await browser.newContext({ baseURL: AUTH_URL, serviceWorkers: 'block' });
  await isolateFromPlan(theirs);
  const them = await theirs.newPage();
  try {
    await them.goto(link);
    await expect(them.locator('#panelSetPassword')).toBeVisible();
    // The token was in the fragment so that it reached no log; it is out of
    // the URL before the form is even filled in.
    expect(them.url()).not.toContain('token=');

    const chosen = 'a-long-enough-password';
    await them.fill('#newPassword', chosen);
    await them.fill('#newPassword2', chosen);
    await them.click('#setPasswordSubmit');
    await expect(them.locator('#authNote')).toContainText('Your password is saved');

    // The link works once. A second browser presenting the same one is
    // refused, which is what makes a leaked link survivable.
    const again = await browser.newContext({ baseURL: AUTH_URL, serviceWorkers: 'block' });
    await isolateFromPlan(again);
    const second = await again.newPage();
    try {
      await second.goto(link);
      await second.fill('#newPassword', 'another-long-password');
      await second.fill('#newPassword2', 'another-long-password');
      await second.click('#setPasswordSubmit');
      await expect(second.locator('#setPasswordMsg')).toContainText('expired or has already been used');
    } finally {
      await again.close();
    }

    // And the password they chose signs them in, as the viewer they were made.
    await them.click('#authNoteAct');
    await them.fill('#loginEmail', invited);
    await them.fill('#loginPassword', chosen);
    await them.click('#loginSubmit');
    await expect(them.locator('.account-email')).toHaveText(invited);
    await expect(them.locator('.account-role')).toHaveText('viewer, read-only');

    // A viewer is offered nothing that would be refused. The admin panel is
    // genuinely enforced — the same session is refused by the server, not just
    // by the page.
    await expect(them.locator('#accountActs')).not.toContainText('People');
    await them.goto('/#/admin');
    await expect(them.locator('#authNote')).toContainText("Managing accounts is an admin's job");
    expect((await them.context().request.get(`${AUTH_URL}/api/v1/users`)).status()).toBe(403);

    // The ledger is readable and has nothing to edit it with. Read-only rather
    // than disabled, so the figures stay selectable and copyable.
    await them.goto('/');
    await them.locator('#tab-budget').click();
    await expect(them.locator('#ceilingInput')).toHaveAttribute('readonly', '');
    await expect(them.locator('#addBudgetRow')).toBeHidden();
    await expect(them.locator('#importData')).toBeDisabled();
    await expect(them.locator('#exportData')).toBeEnabled();
  } finally {
    await theirs.close();
  }

  // Tidy up after itself: the account was this test's, and leaving it behind
  // makes the next run's list longer for no reason.
  //
  // Reloaded rather than navigated: the admin has been sitting on this list
  // while somebody else set their password, so the revision it was drawn
  // against has moved. A write against the old one is refused — correctly —
  // and this is a cleanup, not the test of that path.
  page.on('dialog', (d) => d.accept().catch(() => {}));
  await page.reload();
  await expect(page.locator('.person', { hasText: invited }).locator('.person-status')).toHaveText('active');
  await page.locator('.person', { hasText: invited }).getByText('Remove').click();
  await expect(page.locator('.person', { hasText: invited })).toHaveCount(0);
});

/*
 * Passkeys, driven by Chrome's virtual authenticator.
 *
 * There is no way to test this with real hardware in CI, so the authenticator
 * is a software one the browser provides over the DevTools protocol. What it
 * is not is a substitute for a phone: it exercises the ceremony, the encoding
 * at the boundary and the server's verification, and it says nothing about how
 * any particular authenticator behaves.
 *
 * `hasResidentKey` is the setting that matters. This server registers only
 * discoverable credentials, and a discoverable assertion is the one that
 * carries a user handle — which is how the server finds the account without
 * being told an address first.
 */
test('a passkey can be registered from a signed-in session and then signs you in on its own', async ({ page }, testInfo) => {
  // The config block is the server telling the browser whether the passkey
  // routes are mounted at all, so it is also the honest thing to skip on.
  await page.goto('/');
  const passkeys = await page.evaluate(() =>
    JSON.parse(document.getElementById('soiree-config').textContent).passkeys);
  test.skip(!passkeys, 'this instance derived no relying party, so passkeys are off');

  const who = admin(testInfo);
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('WebAuthn.enable');
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });

  try {
    await signIn(page, who);

    await page.goto('/#/account');
    await expect(page.locator('#passkeySection')).toBeVisible();
    await expect(page.locator('#passkeyEmpty')).toBeVisible();

    await page.fill('#passkeyLabel', 'Kitchen drawer key');
    await page.click('#passkeyAdd');

    await expect(page.locator('#passkeyList .passkey')).toHaveCount(1);
    await expect(page.locator('.passkey-label')).toHaveText('Kitchen drawer key');
    await expect(page.locator('#passkeyMsg')).toContainText('Passkey added');
    // The credential really reached the authenticator, rather than the page
    // merely saying so.
    const stored = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
    expect(stored.credentials).toHaveLength(1);
    expect(stored.credentials[0].isResidentCredential).toBe(true);

    // Now the part this exists for: no address typed, no password, no list of
    // credentials published by the server. The authenticator finds its own.
    await page.goto('/#/account');
    await page.click('#signOutBtn');
    await expect(page.locator('#accountActs button')).toHaveText(['Sign in']);

    await page.goto('/#/login');
    await expect(page.locator('#loginPasskey')).toBeVisible();
    await page.click('#loginPasskey');
    await expect(page.locator('.account-email')).toHaveText(who.email);

    // It is the person's own list to manage, and removing one takes the way in
    // with it.
    await page.goto('/#/account');
    await expect(page.locator('#passkeyList .passkey')).toHaveCount(1);
    await expect(page.locator('.passkey-used')).toContainText('last used');
    page.on('dialog', (d) => d.accept().catch(() => {}));
    await page.locator('.passkey').getByText('Remove').click();
    await expect(page.locator('#passkeyList .passkey')).toHaveCount(0);
    await expect(page.locator('#passkeyEmpty')).toBeVisible();
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {});
  }
});

/*
 * The reason a person has a language at all, against the real server: the
 * deployment speaks one language, the person being invited reads another, and
 * the admin says so. What they are then sent has to open in it — and that is a
 * claim about the link the *server* builds, which a scripted server cannot
 * make.
 */
test('somebody invited in Dutch is met in Dutch, from the link onwards', async ({ page, browser }, testInfo) => {
  await signIn(page, admin(testInfo));

  const invited = `saskia-${stamp()}@example.test`;
  await page.goto('/#/admin');
  await expect(page.locator('#peopleList .person')).not.toHaveCount(0);
  await page.fill('#newUserEmail', invited);
  await page.selectOption('#newUserRole', 'editor');
  await page.selectOption('#newUserLanguage', 'nl');
  await page.click('#createUserSubmit');

  await expect(page.locator('#adminLinkOut')).toBeVisible();
  const link = await page.inputValue('#adminLinkValue');
  // The language rides in the query string, which is sent to the server and is
  // only a language. The secret is still behind the #, which never is.
  expect(link).toContain(`${AUTH_URL}/?lang=nl#/set-password?token=`);
  await expect(page.locator('.person', { hasText: invited }).locator('.person-language')).toHaveValue('nl');

  const theirs = await browser.newContext({ baseURL: AUTH_URL, serviceWorkers: 'block' });
  await isolateFromPlan(theirs);
  const them = await theirs.newPage();
  try {
    await them.goto(link);
    await expect(them.locator('html')).toHaveAttribute('lang', 'nl');
    await expect(them.locator('#authTitle')).toHaveText('Kies een wachtwoord');
    expect(them.url()).not.toContain('token=');
    expect(them.url()).toContain('lang=nl');

    const chosen = 'een-lang-genoeg-wachtwoord';
    await them.fill('#newPassword', chosen);
    await them.fill('#newPassword2', chosen);
    await them.click('#setPasswordSubmit');
    await expect(them.locator('#authNote')).toContainText('Je wachtwoord is opgeslagen');

    await them.click('#authNoteAct');
    await them.fill('#loginEmail', invited);
    await them.fill('#loginPassword', chosen);
    await them.click('#loginSubmit');
    await expect(them.locator('.account-role')).toHaveText('bewerker');

    // A later visit with no ?lang= at all — a bookmark, the address typed in —
    // is still in Dutch: the account says so, and this device remembers.
    await them.goto('/');
    await expect(them.locator('.account-email')).toHaveText(invited);
    await expect(them.locator('html')).toHaveAttribute('lang', 'nl');
    await expect(them.locator('#accountActs')).toContainText('Afmelden');
  } finally {
    await theirs.close();
  }
});
