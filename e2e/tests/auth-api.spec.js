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
const { API_URL, adoptSession, apiSignIn } = require('./helpers');

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
 * What Chrome's authenticator is not: an iPhone.
 *
 * "Works with Windows Hello, not on an Apple device" was a real report, and
 * the test above could not have caught it, because everything about it is the
 * easy case — a device-bound key, a browser that answers only what it was
 * asked, a prompt that opens whenever it is called. Each test below takes one
 * of those away, as far as it can be taken away without the hardware: the
 * authenticator is told to behave like a synced one, and the browser is
 * shimmed to behave like WebKit where WebKit differs. A shim is a model of the
 * other browser and not the other browser; what these prove is that the page
 * and the server hold up their end of each rule.
 *
 * None of them signs in with the password. The run shares one session,
 * replayed as a cookie, because the login limiters are real — and "signing
 * out" here is losing the cookie, so that shared session survives for
 * whoever is next.
 */
async function passkeysOn(page) {
  await page.goto('/');
  return page.evaluate(() =>
    JSON.parse(document.getElementById('soiree-config').textContent).passkeys);
}

async function addAuthenticator(page, extra = {}) {
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
      ...extra,
    },
  });
  return { cdp, authenticatorId };
}

// Bounded, and not an assertion. A page that never fetches ahead is what the
// test of the tap is for, and it should fail there, on the prompt it was
// refused, rather than here on a wait.
const begun = (page, ceremony) =>
  page.waitForResponse((r) => r.url().endsWith(`/api/v1/auth/passkeys/${ceremony}/begin`), { timeout: 3000 })
    .catch(() => null);

/**
 * Adds a passkey and then signs in with it from cold, the way a person would:
 * the screen is up, and has been for a moment, before anything is tapped. That
 * moment is part of the model — it is when the page fetches the challenge, so
 * that the tap itself has nothing left to wait for.
 */
async function registerThenSignIn(page, testInfo, label) {
  const who = admin(testInfo);
  await adoptSession(page, await apiSignIn(page.request, API_URL), AUTH_URL);

  // Reloaded, here and below, because a move between two #/ routes is not a
  // page load, and the page asks who it is once per load.
  let ready = begun(page, 'register');
  await page.goto('/#/account');
  await page.reload();
  await expect(page.locator('#passkeySection')).toBeVisible();
  await ready;
  await page.fill('#passkeyLabel', label);
  await page.click('#passkeyAdd');
  await expect(page.locator('#passkeyMsg')).toContainText('Passkey added');
  await expect(page.locator('.passkey-label', { hasText: label })).toHaveCount(1);

  await page.context().clearCookies();
  ready = begun(page, 'login');
  await page.goto('/#/login');
  await page.reload();
  await expect(page.locator('#loginPasskey')).toBeVisible();
  await ready;
  await page.click('#loginPasskey');
  await expect(page.locator('.account-email')).toHaveText(who.email);
}

/** Takes the test's passkey off the shared account again. */
async function removePasskey(page, label) {
  page.on('dialog', (d) => d.accept().catch(() => {}));
  await page.goto('/#/account');
  const row = page.locator('.passkey', { hasText: label });
  if (await row.count()) {
    await row.getByText('Remove').click();
    await expect(row).toHaveCount(0);
  }
}

test('a synced passkey, the kind an Apple device makes, registers and signs in', async ({ page }) => {
  test.skip(!(await passkeysOn(page)), 'this instance derived no relying party, so passkeys are off');

  // Backup-eligible and backed up: a passkey in iCloud Keychain. The server
  // has to store the first flag at registration and find it unchanged in every
  // assertion after, or the library refuses the login.
  const { cdp, authenticatorId } = await addAuthenticator(page, {
    defaultBackupEligibility: true,
    defaultBackupState: true,
  });
  try {
    await registerThenSignIn(page, test.info(), 'Synced phone');
    const stored = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
    expect(stored.credentials).toHaveLength(1);
    expect(stored.credentials[0].backupEligibility, 'the authenticator really made a synced credential').toBe(true);
    expect(stored.credentials[0].backupState).toBe(true);
    await removePasskey(page, 'Synced phone');
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {});
  }
});

test('an extension output nobody asked for does not cost somebody their sign-in', async ({ page }) => {
  // WebKit reports `appid: false` on assertions from a security key whether or
  // not appid was requested. The signature is good; the server used to refuse
  // the login over the extra member.
  await page.addInitScript(() => {
    const real = PublicKeyCredential.prototype.toJSON;
    PublicKeyCredential.prototype.toJSON = function toJSON() {
      const json = real.call(this);
      if (this.response instanceof AuthenticatorAssertionResponse) {
        json.clientExtensionResults = { ...json.clientExtensionResults, appid: false };
      }
      return json;
    };
  });
  // After the shim, because a shim is installed by the next page load.
  test.skip(!(await passkeysOn(page)), 'this instance derived no relying party, so passkeys are off');

  const { cdp, authenticatorId } = await addAuthenticator(page);
  try {
    const sent = page.waitForRequest((r) => r.url().endsWith('/api/v1/auth/passkeys/login/finish'));
    await registerThenSignIn(page, test.info(), 'Key on Safari');
    expect((await sent).postDataJSON().credential.clientExtensionResults,
      'the shim is in the way, so this is the case it says it is').toEqual({ appid: false });
    await removePasskey(page, 'Key on Safari');
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {});
  }
});

test('the passkey prompt is asked for inside the tap, not after a round trip', async ({ page }) => {
  // Safari's rule, made strict: navigator.credentials works only in the same
  // task as the event that asked for it. A fetch resolves in a later one, so a
  // page that asks the server for a challenge between the tap and the call is
  // refused, exactly as WebKit refuses it: NotAllowedError.
  await page.addInitScript(() => {
    let tapping = false;
    for (const type of ['click', 'keydown', 'pointerup', 'touchend', 'submit']) {
      window.addEventListener(type, () => {
        tapping = true;
        setTimeout(() => { tapping = false; }, 0);
      }, true);
    }
    const container = navigator.credentials;
    for (const name of ['create', 'get']) {
      const real = container[name].bind(container);
      container[name] = (options) => (tapping
        ? real(options)
        : Promise.reject(new DOMException('called outside a user gesture', 'NotAllowedError')));
    }
  });
  // After the shim, because a shim is installed by the next page load.
  test.skip(!(await passkeysOn(page)), 'this instance derived no relying party, so passkeys are off');

  const { cdp, authenticatorId } = await addAuthenticator(page);
  try {
    await registerThenSignIn(page, test.info(), 'Strict browser');
    await removePasskey(page, 'Strict browser');
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {});
  }
});

test('a browser whose own JSON helpers are missing or broken still gets through', async ({ page }) => {
  // Older Safari has neither parse*FromJSON nor toJSON, and a password
  // manager's extension can leave a toJSON that throws. Both land on the
  // hand-written translation, which no other test reaches: Chrome has the
  // helpers, so the code that runs on the failing phones never ran here.
  await page.addInitScript(() => {
    delete PublicKeyCredential.parseCreationOptionsFromJSON;
    delete PublicKeyCredential.parseRequestOptionsFromJSON;
    PublicKeyCredential.prototype.toJSON = function toJSON() {
      throw new TypeError('Can only call PublicKeyCredential.toJSON on instances of PublicKeyCredential');
    };
  });
  // After the shim, because a shim is installed by the next page load.
  test.skip(!(await passkeysOn(page)), 'this instance derived no relying party, so passkeys are off');

  const { cdp, authenticatorId } = await addAuthenticator(page);
  try {
    await page.goto('/');
    expect(await page.evaluate(() => typeof PublicKeyCredential.parseRequestOptionsFromJSON)).toBe('undefined');
    await registerThenSignIn(page, test.info(), 'Older browser');
    await removePasskey(page, 'Older browser');
  } finally {
    await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId }).catch(() => {});
  }
});

test('a ceremony the browser refuses says what kind of refusal it was', async ({ page }) => {
  // The challenge is canned and nothing here reaches the server: this is about
  // what the page says, and the server has nothing to add to it.
  await page.route('**/api/v1/auth/passkeys/login/begin', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ publicKey: { challenge: 'c29pcmVlLWUyZS1jaGFsbGVuZ2U', rpId: 'localhost', userVerification: 'preferred' } }),
  }));
  await page.addInitScript(() => {
    window.refuseWith = { name: 'NotAllowedError', after: 0 };
    navigator.credentials.get = () => new Promise((resolve, reject) => {
      const { name, after } = window.refuseWith;
      setTimeout(() => reject(new DOMException('the browser\'s own words, which are not shown', name)), after);
    });
  });
  // After the shim, because a shim is installed by the next page load.
  test.skip(!(await passkeysOn(page)), 'this instance derived no relying party, so passkeys are off');

  await page.goto('/#/login');
  const msg = page.locator('#loginMsg');
  const tap = async (name, after) => {
    await page.evaluate((next) => { window.refuseWith = next; }, { name, after });
    await page.click('#loginPasskey');
  };

  // At once: no person dismissed that, so the browser did. It used to be
  // swallowed whole, and the button simply did nothing.
  await tap('NotAllowedError', 0);
  await expect(msg).toHaveText(/refused to show the passkey prompt.*\(NotAllowedError\)$/);
  await expect(msg).toHaveClass(/is-refusal/);

  // After a while: somebody closed it. Said, but not as an error.
  await tap('NotAllowedError', 1200);
  await expect(msg).toHaveText(/closed or timed out.*\(NotAllowedError\)$/);
  await expect(msg).not.toHaveClass(/is-refusal/);

  await tap('SecurityError', 0);
  await expect(msg).toHaveText(/address does not match.*\(SecurityError\)$/);

  // A kind with no sentence of its own still names itself.
  await tap('UnknownError', 0);
  await expect(msg).toHaveText(/did not complete the sign-in.*\(UnknownError\)$/);
  await expect(msg).not.toContainText('own words');
  await expect(page.locator('#loginPasskey')).toBeEnabled();
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
  // Chosen for that mail, and kept nowhere: the list has nothing picked.
  await expect(page.locator('.person', { hasText: invited }).locator('.person-language')).toHaveValue('');

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

    // The link was in Dutch because the admin said so, and the link said so in
    // its address — which beat this browser's own English. Nothing was pinned:
    // a later visit with no ?lang= is back to what the browser asks for.
    await them.goto('/');
    await expect(them.locator('.account-email')).toHaveText(invited);
    await expect(them.locator('html')).toHaveAttribute('lang', 'en');
    await expect(them.locator('#accountActs')).toContainText('Sign out');
  } finally {
    await theirs.close();
  }
});
