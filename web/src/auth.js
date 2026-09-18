/* soiree — accounts: signing in, choosing a password, and who has an account.
 *
 * A second script alongside app.js rather than part of it. The planner and the
 * accounts surface are two different things that share a page: one is a ledger
 * held in a browser, the other is a conversation with the server about who you
 * are. Keeping them in separate files keeps the planner loadable and testable
 * on a deployment that has no accounts at all, which is a real deployment —
 * `docker run` with no database serves exactly that.
 *
 * Three rules this file follows throughout.
 *
 *  - The session is an HttpOnly cookie. Nothing here reads it, writes it, or
 *    could. Every request goes out with credentials: 'same-origin' and the
 *    browser attaches it; asking whether we are signed in means asking the
 *    server, which is the only party that knows.
 *  - 401 from /api/v1/auth/session is the ordinary answer for a visitor who
 *    has not signed in. It is not an error, is never logged as one, and is the
 *    signal that the sign-in door should be drawn. 404 is a different answer
 *    again: this deployment has no accounts, so there is no door to draw.
 *  - No secret is written anywhere it could be read back. The token in a
 *    set-password link is taken out of the URL before anything else happens,
 *    lives in one closure variable, goes out in a request body and is dropped.
 *    It is never logged, never stored, never rendered.
 *
 * The one deliberate exception to that last rule is the set-password link the
 * admin panel shows after creating an account on a deployment with no SMTP.
 * The server hands it back precisely because there is no other way for it to
 * reach the person it belongs to; showing it to the admin who just asked for
 * it is the documented degraded mode, and it is shown once, in the page, and
 * kept nowhere.
 *
 * What this file does NOT do is enforce anything. The server does that:
 * routeAPI in internal/httpd/api.go puts the whole plan subtree behind
 * RequireWrite, so nobody reads the plan without a session and a viewer writes
 * nothing, whatever this page offers them. Roles here shape what the interface
 * shows — a locked planner for a viewer, an accounts screen for an admin — and
 * a page that got that wrong would be confusing, not unsafe.
 *
 * It does tell the planner when the session changes, on `soiree:session`, and
 * answers `soiree:session-check` when the planner has reason to doubt it. See
 * "A session that ends while the page is open" in app.js.
 */
(function () {
  'use strict';

  var API = '/api/v1';

  /* Read from the same data block app.js reads. Only one key matters here:
   * whether this deployment mounted the passkey routes at all. Offering a
   * button whose endpoint is not routed is a 404 dressed up as a feature. */
  var CONFIG = (function () {
    try {
      var el = document.getElementById('soiree-config');
      return el ? JSON.parse(el.textContent) || {} : {};
    } catch (e) {
      return {};
    }
  })();

  var PASSKEYS_OFFERED = !!CONFIG.passkeys &&
    typeof window.PublicKeyCredential === 'function' &&
    !!(navigator.credentials && navigator.credentials.create);

  /* ------------------------------------------------------------------
   * Small helpers
   * ------------------------------------------------------------------ */

  function byId(id) { return document.getElementById(id); }

  function each(list, fn) { Array.prototype.forEach.call(list, fn); }

  function show(el, on) {
    if (!el) return;
    if (on) el.removeAttribute('hidden');
    else el.setAttribute('hidden', '');
  }

  function setText(el, text) { if (el) el.textContent = text == null ? '' : String(text); }

  /* A line about what just happened. `bad` is the difference between "follow
   * the prompt from your device" and "that link has expired": one is the
   * interface narrating itself, the other is a refusal, and colouring them
   * alike makes every message look like something went wrong. */
  function say(id, text, bad) {
    var el = typeof id === 'string' ? byId(id) : id;
    if (!el) return;
    setText(el, text);
    if (el.classList) el.classList.toggle('is-refusal', !!bad);
  }

  function make(tag, cls, text) {
    var el = document.createElement(tag);
    if (cls) el.className = cls;
    if (text != null) el.textContent = String(text);
    return el;
  }

  /* A date as a person reads it, in the locale the deployment is configured
   * with. Never a raw timestamp: "2026-03-04T11:52:09Z" in a list of devices
   * is machine exhaust, not information. */
  function day(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    try {
      return d.toLocaleDateString(CONFIG.locale || undefined, {
        year: 'numeric', month: 'long', day: 'numeric'
      });
    } catch (e) {
      return d.toISOString().slice(0, 10);
    }
  }

  /* ---------- Requests ----------
   * Never rejects, for the same reason app.js's api() does not: an unreachable
   * origin is an answer this has to act on, not an exception to escape
   * through. status 0 means "no answer at all".
   */
  function request(method, path, body) {
    var init = {
      method: method,
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { Accept: 'application/json' }
    };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    return fetch(API + path, init).then(function (res) {
      if (res.status === 204) return { status: 204, body: null };
      return res.text().then(function (txt) {
        var parsed = null;
        try { parsed = txt ? JSON.parse(txt) : null; } catch (e) { parsed = null; }
        return { status: res.status, body: parsed };
      });
    }, function () {
      return { status: 0, body: null };
    });
  }

  /* What to say when a request did not go the way it was meant to.
   *
   * `map` names the codes this particular call has something specific to say
   * about. Anything else falls through to the server's own message, which is
   * written for a person, and then to a last resort. Nothing here ever prints
   * a status code at somebody. */
  function problem(res, map) {
    if (res.status === 0) return 'No answer from the server. Check your connection and try again.';
    if (res.status === 429) return 'Too many attempts just now. Wait a minute, then try again.';
    var code = res.body && res.body.error;
    if (map && code && map[code]) return map[code];
    if (res.body && res.body.message) return res.body.message;
    if (res.status === 403) return 'Your account is not allowed to do that.';
    if (res.status === 401) return 'Sign in again — your session has ended.';
    return 'That did not work. Try again.';
  }

  /* ---------- base64url ----------
   * The whole WebAuthn boundary is base64url, unpadded on the way out of the
   * server and either way on the way in. These two are the entire contract,
   * and they exist because the browser deals in ArrayBuffers and JSON does
   * not.
   */
  function fromB64url(s) {
    var str = String(s).replace(/-/g, '+').replace(/_/g, '/');
    while (str.length % 4) str += '=';
    var bin = atob(str);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i += 1) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }

  function toB64url(buf) {
    var bytes = new Uint8Array(buf);
    var bin = '';
    for (var i = 0; i < bytes.length; i += 1) bin += String.fromCharCode(bytes[i]);
    // Unpadded: what the server emits, and what it reads back without
    // complaint either way.
    return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }

  /* ------------------------------------------------------------------
   * The route, read before anything else
   * ------------------------------------------------------------------
   * A set-password link arrives as {BASE_URL}/#/set-password?token=… . The
   * token is in the fragment on purpose: a fragment is never sent to a server,
   * so it is in no access log, no proxy's log, and no Referer header. That
   * property is this file's to keep — it is taken out of the URL immediately,
   * held in one variable, and the URL is rewritten without it before the page
   * has painted anything.
   * ------------------------------------------------------------------ */

  var AUTH_ROUTES = { login: true, 'set-password': true, admin: true, account: true };

  var pendingToken = '';

  function readRoute() {
    var raw = String(location.hash || '');
    if (raw.charAt(0) === '#') raw = raw.slice(1);
    var query = '';
    var q = raw.indexOf('?');
    if (q >= 0) {
      query = raw.slice(q + 1);
      raw = raw.slice(0, q);
    }
    return { path: raw.replace(/^\/+/, '').replace(/\/+$/, ''), query: query };
  }

  function queryValue(query, key) {
    var parts = String(query).split('&');
    for (var i = 0; i < parts.length; i += 1) {
      var eq = parts[i].indexOf('=');
      if (eq < 0) continue;
      if (parts[i].slice(0, eq) !== key) continue;
      try {
        return decodeURIComponent(parts[i].slice(eq + 1).replace(/\+/g, ' '));
      } catch (e) {
        return '';
      }
    }
    return '';
  }

  /* Takes the token out of the URL and keeps it in memory.
   *
   * replaceState rather than assigning location.hash: assigning would push a
   * history entry carrying the token, which is the thing being removed. It
   * also fires no hashchange, so the router is not re-entered here. */
  function captureToken(query) {
    var token = queryValue(query, 'token');
    if (!token) return;
    pendingToken = token;
    var clean = location.pathname + location.search + '#/set-password';
    if (window.history && history.replaceState) {
      history.replaceState(null, '', clean);
    } else {
      // Older browsers: the token leaves the address bar a moment later than
      // it should, which is still better than leaving it there.
      location.hash = '/set-password';
    }
  }

  // Synchronously, before the session probe and before first paint: the route
  // decides whether the planner or the accounts screen is the page, and a
  // set-password arrival must not flash the ledger on its way to the form.
  (function () {
    var r = readRoute();
    if (r.path === 'set-password') captureToken(r.query);
    if (AUTH_ROUTES[r.path]) document.body.classList.add('showing-auth');
  })();

  /* ------------------------------------------------------------------
   * Session state
   * ------------------------------------------------------------------
   * Four states, and the difference between the last three is the whole
   * design of this file:
   *
   *   unknown  — the probe has not answered yet. Draw nothing; a sign-in
   *              button that appears and then vanishes is worse than one that
   *              arrives a beat late.
   *   none     — 404: this deployment has no accounts surface. No bar, no
   *              screens, no sign-in. Not an error.
   *   out      — 401: there are accounts, and this browser has no session.
   *              Also not an error, and the only state in which "Sign in"
   *              means anything.
   *   in       — 200: `user` is who the server says we are.
   * ------------------------------------------------------------------ */

  var state = 'unknown';
  var user = null;

  function isAdmin() { return state === 'in' && user && user.role === 'admin'; }

  function setSession(next, who) {
    state = next;
    user = who || null;

    var cls = document.body.classList;
    cls.remove('accounts-none', 'signed-out', 'signed-in');
    cls.remove('role-admin', 'role-editor', 'role-viewer');
    if (next === 'none') cls.add('accounts-none');
    if (next === 'out') cls.add('signed-out');
    if (next === 'in') {
      cls.add('signed-in');
      if (user && user.role) cls.add('role-' + user.role);
    }

    lockPlanner(next === 'in' && !!user && user.role === 'viewer');
    renderAccountBar();
    render();

    // Tell the planner. Its own probe is refused with a 401 until somebody is
    // signed in, and it deliberately does not fall back to localStorage on
    // that answer — edits that looked saved but reached nobody would be worse
    // than none. Announced from here because this is the one place the session
    // changes, so a sign-out reaches it too.
    document.dispatchEvent(new CustomEvent('soiree:session', {
      detail: { signedIn: next === 'in', role: user && user.role }
    }));
  }

  /* Is there an API behind this page at all?
   *
   * app.js asks the same question at startup, of /api/v1/plan, and its answer
   * settles this one: no database means no accounts, because the routes are
   * mounted together or not at all. Two scripts asking it separately is one
   * request more than a deployment with no database should ever pay.
   *
   * The contract with app.js, which owns that request (announceAPI there):
   *
   *     window.soiree = window.soiree || {};
   *     window.soiree.apiAvailable = <true | false>;   // latched in connect()
   *     document.dispatchEvent(new CustomEvent('soiree:api',
   *       { detail: { available: <true | false> } }));
   *
   * `null` or absent means "not answered yet". The global is for a listener
   * that arrives late, the event for one that arrives early; either alone
   * leaves a race.
   *
   * While it reads as "not answered yet" the probe goes out anyway, so nothing
   * here depends on which script the browser happened to finish first. When
   * the answer is already in, a deployment with no database is not asked a
   * second question it has already answered.
   */
  function apiKnownAbsent() {
    return !!(window.soiree && window.soiree.apiAvailable === false);
  }

  document.addEventListener('soiree:api', function (ev) {
    if (!ev || !ev.detail || ev.detail.available !== false) return;
    // Arrived after the probe went out: nothing to undo, the answer stands.
    if (state !== 'unknown') return;
    setSession('none');
  });

  function probeSession() {
    if (apiKnownAbsent()) {
      setSession('none');
      return Promise.resolve();
    }
    return request('GET', '/auth/session').then(function (res) {
      if (res.status === 200 && res.body) {
        setSession('in', res.body);
        return;
      }
      // 404: the routes are not mounted, so this deployment has no accounts.
      // 401: there are accounts and nobody is signed in. Both are ordinary.
      // Anything else — a 500, or no answer at all — is treated as signed out
      // rather than as a reason to break the page: the planner works either
      // way, and the sign-in door is the right thing to offer when the answer
      // is not "you are Ada".
      setSession(res.status === 404 ? 'none' : 'out');
    });
  }

  /* The planner doubts the session.
   *
   * app.js raises this when a request of its own comes back 401, or when its
   * event stream is refused and it cannot tell why. The answer goes back the
   * way every other change does, through setSession — so the planner learns
   * about an ended session from the same event it learns about a sign-out.
   *
   * Three answers, and only one of them changes anything:
   *
   *   200 — still signed in. The stream was refused for some other reason and
   *         app.js is already waiting that out. Saying "in" again would make
   *         it refetch the plan for nothing, so nothing is said.
   *   401 — the session is gone. Sign-out, and straight to the sign-in screen
   *         with a line saying why: somebody mid-edit who is shown a login
   *         form with no explanation assumes they did something wrong.
   *   anything else — an outage is not a sign-out. probeSession() reads a 500
   *         as "out" because at load there is nothing to lose by offering the
   *         door; here there is a signed-in person to wrongly throw out.
   */
  var checking = false;
  var endedNotice = false;

  document.addEventListener('soiree:session-check', function () {
    if (checking || state === 'unknown' || state === 'none') return;
    checking = true;
    request('GET', '/auth/session').then(function (res) {
      checking = false;
      if (res.status === 200 && res.body) {
        if (state !== 'in') setSession('in', res.body);
        return;
      }
      if (res.status !== 401 || state !== 'in') return;
      endedNotice = true;
      setSession('out');
      goto('login');
    }, function () { checking = false; });
  });

  /* ------------------------------------------------------------------
   * A viewer's planner
   * ------------------------------------------------------------------
   * Read-only rather than disabled wherever the control allows it, which is
   * the rule app.js already applies to an archived ledger: a figure somebody
   * cannot change is still a figure they have to be able to read, select and
   * copy. Selects and checkboxes have no readonly, so those are disabled.
   *
   * The rows are rebuilt from scratch on every render app.js performs, so the
   * lock has to be re-applied to controls that did not exist when it was set.
   * A MutationObserver on the planner's subtree is what notices; it watches
   * childList only, so setting `readOnly` — which reflects to an attribute —
   * cannot feed itself.
   * ------------------------------------------------------------------ */

  var plannerLocked = false;
  var lockObserver = null;
  var lockQueued = false;

  function plannerWrap() { return byId('plannerWrap'); }

  function applyLock() {
    var wrap = plannerWrap();
    if (!wrap) return;
    var on = plannerLocked;
    each(wrap.querySelectorAll('input, textarea, select'), function (el) {
      // The file input is how an import is chosen; app.js disables the button
      // in front of it rather than the input itself, and so do we.
      if (el.id === 'importFile') return;
      if (el.tagName === 'SELECT' || el.type === 'checkbox') {
        if (el.disabled !== on) el.disabled = on;
      } else if (el.readOnly !== on) {
        el.readOnly = on;
      }
    });
    each(wrap.querySelectorAll('.by-btn'), function (b) {
      if (b.disabled !== on) b.disabled = on;
    });
    // Export stays open to everybody — a ledger nobody can take a copy of is
    // a worse ledger. Import replaces the whole planner, so it does not.
    var imp = byId('importData');
    if (imp && imp.disabled !== on) imp.disabled = on;
  }

  function lockPlanner(on) {
    if (on === plannerLocked) return;
    // Unlocking an archived ledger would undo app.js's own lock, which was set
    // for a different reason and is still true. Leave it be; app.js re-applies
    // its own on the next render either way.
    if (!on && document.body.classList.contains('is-archived')) {
      plannerLocked = false;
      if (lockObserver) { lockObserver.disconnect(); lockObserver = null; }
      return;
    }
    plannerLocked = on;
    applyLock();

    if (on && !lockObserver && window.MutationObserver && plannerWrap()) {
      lockObserver = new MutationObserver(function () {
        if (!plannerLocked || lockQueued) return;
        lockQueued = true;
        // After the render that provoked it, not during: app.js rebuilds a
        // table in several mutations and re-locking each one is wasted work.
        setTimeout(function () { lockQueued = false; applyLock(); }, 0);
      });
      lockObserver.observe(plannerWrap(), { childList: true, subtree: true });
    }
    if (!on && lockObserver) { lockObserver.disconnect(); lockObserver = null; }
  }

  /* ------------------------------------------------------------------
   * The account bar
   * ------------------------------------------------------------------
   * One quiet line above the masthead: who you are, and the two or three
   * places you can go from there. Removed entirely when this deployment has
   * no accounts.
   * ------------------------------------------------------------------ */

  /* Moves between the planner and the accounts screens.
   *
   * render() is called explicitly rather than left to the hashchange event:
   * assigning a hash that is already the current one fires nothing, and
   * "sign in, land on the page you asked for" goes through exactly that case.
   * render() is idempotent, so the extra call a real hashchange also makes
   * costs nothing. */
  function goto(path) {
    var next = path ? '#/' + path : '';
    if (next) {
      if (location.hash !== next) location.hash = next;
    } else if (location.hash) {
      // Assigning '' leaves a bare '#' in the address bar; replaceState does
      // not, and going back to the planner is not a history entry worth
      // keeping.
      if (window.history && history.replaceState) {
        history.replaceState(null, '', location.pathname + location.search);
      } else {
        location.hash = '';
      }
    }
    render();
  }

  function barButton(label, cls, path) {
    var b = make('button', cls, label);
    b.type = 'button';
    b.addEventListener('click', function () { goto(path); });
    return b;
  }

  function renderAccountBar() {
    var bar = byId('accountBar');
    var who = byId('accountWho');
    var acts = byId('accountActs');
    if (!bar || !who || !acts) return;

    show(bar, state === 'out' || state === 'in');
    while (acts.firstChild) acts.removeChild(acts.firstChild);
    who.textContent = '';

    if (state === 'out') {
      acts.appendChild(barButton('Sign in', 'link-btn', 'login'));
      return;
    }
    if (state !== 'in' || !user) return;

    who.appendChild(make('span', 'account-email', user.email));
    who.appendChild(make('span', 'account-role', roleWord(user.role)));

    if (isAdmin()) acts.appendChild(barButton('People', 'link-btn', 'admin'));
    acts.appendChild(barButton('Your account', 'link-btn', 'account'));
    var out = make('button', 'link-btn', 'Sign out');
    out.type = 'button';
    out.addEventListener('click', signOut);
    acts.appendChild(out);
  }

  function roleWord(role) {
    if (role === 'admin') return 'admin';
    if (role === 'editor') return 'editor';
    if (role === 'viewer') return 'viewer, read-only';
    return '';
  }

  function signOut() {
    request('POST', '/auth/logout').then(function () {
      // 204 either way, and a cookie the server has already cleared. Ask again
      // rather than assuming: the answer decides what the bar draws next.
      setSession('out');
      goto('');
    });
  }

  /* ------------------------------------------------------------------
   * The router
   * ------------------------------------------------------------------ */

  var PANEL_IDS = ['panelLogin', 'panelSetPassword', 'panelAdmin', 'panelAccount', 'panelNote'];

  function showPanel(id, title, lede) {
    each(PANEL_IDS, function (p) { show(byId(p), p === id); });
    setText(byId('authTitle'), title);
    setText(byId('authLede'), lede || '');
  }

  function showNote(title, lede, line) {
    showPanel('panelNote', title, lede);
    setText(byId('authNote'), line || '');
    show(byId('authNoteAct'), false);
  }

  // Where to land after signing in: back where you were headed.
  var afterLogin = '';

  function render() {
    var r = readRoute();
    var path = AUTH_ROUTES[r.path] ? r.path : '';

    // A deployment with no accounts has nothing behind these routes.
    if (state === 'none' && path) path = '';

    // Already signed in and asked for the sign-in screen: there is nothing
    // there to do.
    if (path === 'login' && state === 'in') path = '';

    document.body.classList.toggle('showing-auth', !!path);
    show(byId('authScreen'), !!path);
    if (!path) {
      afterLogin = '';
      return;
    }

    // Neither of these has to wait for the probe. A set-password link is
    // redeemed with a token, not a session, and somebody who asked for the
    // sign-in screen should get the form rather than a held breath.
    if (path === 'set-password') { renderSetPassword(); return; }
    if (path === 'login') {
      afterLogin = '';
      renderLogin(endedNotice
        ? 'Your session has ended. Sign in again — what you changed is still here, and is sent as soon as you are back.'
        : 'Sign in with the address your invitation was sent to.');
      return;
    }

    if (state === 'unknown') {
      showNote('One moment', '', 'Checking whether you are signed in.');
      return;
    }
    if (state === 'out') {
      // Asked for somewhere that needs an account. Sign in first, then land
      // where they were going rather than back at the planner.
      afterLogin = path;
      renderLogin(path === 'admin'
        ? 'Sign in with an admin account to manage who has access.'
        : 'Sign in to reach your account.');
      return;
    }
    if (path === 'admin') { renderAdmin(); return; }
    if (path === 'account') { renderAccount(); return; }
    renderLogin('');
  }

  window.addEventListener('hashchange', function () {
    var r = readRoute();
    if (r.path === 'set-password') captureToken(r.query);
    render();
  });

  /* ------------------------------------------------------------------
   * Signing in
   * ------------------------------------------------------------------ */

  function renderLogin(lede) {
    showPanel('panelLogin', 'Sign in', lede);
    show(byId('loginPasskey'), PASSKEYS_OFFERED);
    say(byId('loginMsg'), '');
    var email = byId('loginEmail');
    if (email && !email.value) email.focus();
  }

  function loginSucceeded(who) {
    var next = afterLogin;
    afterLogin = '';
    endedNotice = false;
    var pw = byId('loginPassword');
    if (pw) pw.value = '';       // out of the DOM the moment it is spent
    setSession('in', who);
    goto(next || '');
  }

  function bindLogin() {
    var form = byId('loginForm');
    var msg = byId('loginMsg');
    var submit = byId('loginSubmit');
    if (!form) return;

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var email = (byId('loginEmail').value || '').trim();
      var password = byId('loginPassword').value || '';
      if (!email || !password) {
        say(msg, 'Fill in both your email and your password.', true);
        return;
      }
      submit.disabled = true;
      say(msg, '');
      request('POST', '/auth/login', { email: email, password: password }).then(function (res) {
        submit.disabled = false;
        if (res.status === 200 && res.body) {
          loginSucceeded(res.body);
          return;
        }
        // The server answers identically for an unknown address, a wrong
        // password and a disabled account, on purpose. Saying more here than
        // it said would undo that.
        say(msg, problem(res, {
          invalid_credentials: 'That email and password do not match an account.'
        }), true);
      });
    });

    var forgot = byId('loginForgot');
    if (forgot) {
      forgot.addEventListener('click', function () {
        var field = byId('loginEmail');
        var email = (field.value || '').trim();
        if (!email) {
          say(msg, 'Enter your email first, then ask for a link.', true);
          field.focus();
          return;
        }
        forgot.disabled = true;
        request('POST', '/auth/password-reset', { email: email }).then(function (res) {
          forgot.disabled = false;
          if (res.status === 202) {
            // Deliberately the same sentence whether or not that address has
            // an account. The server is careful not to say; neither is this.
            say(msg, 'If that address has an account, a link is on its way. ' +
              'It works once and expires in 24 hours.');
            return;
          }
          say(msg, problem(res, {}), true);
        });
      });
    }

    var passkey = byId('loginPasskey');
    if (passkey) passkey.addEventListener('click', passkeyLogin);
  }

  /* ------------------------------------------------------------------
   * Choosing a password
   * ------------------------------------------------------------------ */

  function renderSetPassword() {
    showPanel('panelSetPassword', 'Choose a password',
      'This link works once. Once it is used, every session on this account is signed out.');
    var msg = byId('setPasswordMsg');
    if (!pendingToken) {
      say(msg, 'This link is incomplete. Open the whole link from your email, or ask an admin for a new one.', true);
      byId('setPasswordSubmit').disabled = true;
      return;
    }
    byId('setPasswordSubmit').disabled = false;
    say(msg, '');
    var first = byId('newPassword');
    if (first) first.focus();
  }

  function bindSetPassword() {
    var form = byId('setPasswordForm');
    if (!form) return;
    var msg = byId('setPasswordMsg');
    var submit = byId('setPasswordSubmit');

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var a = byId('newPassword').value || '';
      var b = byId('newPassword2').value || '';
      if (a !== b) {
        say(msg, 'The two passwords are not the same.', true);
        return;
      }
      if (a.length < 12) {
        // Checked here as well as at the server so a short password costs a
        // round trip rather than one of the attempts the link is allowed.
        say(msg, 'A password needs at least 12 characters.', true);
        return;
      }
      if (!pendingToken) {
        say(msg, 'This link is incomplete. Ask an admin for a new one.', true);
        return;
      }
      submit.disabled = true;
      say(msg, '');
      // The token goes in the body, never the query string: a query string
      // lands in access logs and browser history, and this is a credential.
      request('POST', '/auth/set-password', { token: pendingToken, password: a })
        .then(function (res) {
          submit.disabled = false;
          if (res.status === 204) {
            pendingToken = '';
            byId('newPassword').value = '';
            byId('newPassword2').value = '';
            // Redeeming revoked every session this account had, including
            // this browser's if it had one.
            setSession('out');
            showNote('Password set', '',
              'Your password is saved. Sign in with it to reach the planner.');
            var b2 = byId('authNoteAct');
            show(b2, true);
            setText(b2, 'Sign in');
            return;
          }
          say(msg, problem(res, {
            invalid_token: 'This link has expired or has already been used. Ask an admin for a new one.',
            weak_password: (res.body && res.body.message) || 'Choose a longer password.'
          }), true);
        });
    });
  }

  /* ------------------------------------------------------------------
   * The admin panel
   * ------------------------------------------------------------------
   * Every route behind this is admin-only at the server, checked per request
   * against the role as it stands in the database. What follows is the part
   * that saves an admin from finding that out by being refused.
   * ------------------------------------------------------------------ */

  var people = [];

  function renderAdmin() {
    if (!isAdmin()) {
      showNote('People', '',
        'Managing accounts is an admin\'s job. Ask an admin to make the change for you.');
      return;
    }
    showPanel('panelAdmin', 'People', 'Everyone with an account on this planner.');
    // Whatever link was last handed over goes out of the page with the panel
    // it was shown in. "Shown once, and kept nowhere" has to survive somebody
    // navigating away and back, or it is not true.
    show(byId('adminLinkOut'), false);
    byId('adminLinkValue').value = '';
    setText(byId('adminLinkCopy'), 'Copy link');
    loadPeople();
  }

  function loadPeople() {
    request('GET', '/users').then(function (res) {
      if (res.status !== 200 || !res.body) {
        say(byId('adminMsg'), problem(res, {}), true);
        return;
      }
      people = res.body.users || [];
      drawPeople();
    });
  }

  function drawPeople() {
    var list = byId('peopleList');
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    show(byId('peopleEmpty'), people.length === 0);
    people.forEach(function (p) { list.appendChild(personRow(p)); });
  }

  // Replaces one account in the list with the version the server just
  // reported, and redraws. This is also the reconcile path for a 409: the
  // refusal carries the row as it now stands, so a concurrent edit costs a
  // redraw rather than a reload.
  function adoptPerson(next) {
    for (var i = 0; i < people.length; i += 1) {
      if (people[i].id === next.id) { people[i] = next; drawPeople(); return; }
    }
    people.push(next);
    drawPeople();
  }

  function dropPerson(id) {
    people = people.filter(function (p) { return p.id !== id; });
    drawPeople();
  }

  var STATUS_WORD = { invited: 'invited', active: 'active', disabled: 'no access' };

  function personRow(p) {
    var self = !!(user && user.id === p.id);
    var li = make('li', 'person');

    var head = make('div', 'person-head');
    head.appendChild(make('span', 'person-email', p.email));
    if (self) head.appendChild(make('span', 'person-you', 'you'));
    li.appendChild(head);

    var meta = make('div', 'person-meta');
    var status = make('span', 'person-status status-' + p.status, STATUS_WORD[p.status] || p.status);
    meta.appendChild(status);
    meta.appendChild(make('span', 'person-added', 'added ' + day(p.createdAt)));
    li.appendChild(meta);

    var acts = make('div', 'person-acts');

    /* Role. A select rather than three buttons: it is one value with three
     * settings, and the current one should be readable without counting which
     * button is lit. Your own is shown and not offered — the server refuses a
     * self-change, because the way back from demoting the only admin is
     * another admin. */
    if (self) {
      acts.appendChild(make('span', 'person-role-fixed', roleWord(p.role)));
    } else {
      var sel = make('select', 'person-role');
      sel.setAttribute('aria-label', 'Role for ' + p.email);
      [['viewer', 'Viewer'], ['editor', 'Editor'], ['admin', 'Admin']].forEach(function (opt) {
        var o = document.createElement('option');
        o.value = opt[0];
        o.textContent = opt[1];
        if (p.role === opt[0]) o.selected = true;
        sel.appendChild(o);
      });
      sel.addEventListener('change', function () {
        patchPerson(p, { role: sel.value }, sel);
      });
      acts.appendChild(sel);
    }

    /* A fresh link. Issuing one supersedes whatever was outstanding, so this
     * is also how a link that went astray is revoked. Not offered for an
     * account with no access: the server refuses it, and telling somebody to
     * turn access back on first is more use than a refusal. */
    if (p.status !== 'disabled') {
      acts.appendChild(actButton(
        p.status === 'invited' ? 'Send the link again' : 'Send a password link',
        'link-btn',
        function (btn) { invite(p, btn); }
      ));
    }

    if (!self) {
      acts.appendChild(actButton(
        p.status === 'disabled' ? 'Turn access back on' : 'Turn off access',
        'link-btn',
        function (btn) {
          patchPerson(p, { status: p.status === 'disabled' ? 'active' : 'disabled' }, btn);
        }
      ));
      acts.appendChild(actButton('Remove', 'link-btn danger', function (btn) {
        if (!window.confirm('Remove the account for ' + p.email + '? This cannot be undone.')) return;
        removePerson(p, btn);
      }));
    }

    li.appendChild(acts);
    return li;
  }

  function actButton(label, cls, fn) {
    var b = make('button', cls, label);
    b.type = 'button';
    b.addEventListener('click', function () { fn(b); });
    return b;
  }

  function adminSays(text, bad) { say(byId('adminMsg'), text || '', bad); }

  /* Every write carries the revision the browser last saw, and the server
   * refuses the write if another admin got there first — handing back the row
   * as it now stands, which is what gets adopted here. */
  function patchPerson(p, change, control) {
    var body = { revision: p.revision };
    for (var k in change) if (Object.prototype.hasOwnProperty.call(change, k)) body[k] = change[k];
    if (control) control.disabled = true;
    adminSays('');
    request('PATCH', '/users/' + p.id, body).then(function (res) {
      if (control) control.disabled = false;
      if (res.status === 200 && res.body) {
        adoptPerson(res.body);
        return;
      }
      if (res.status === 409 && res.body && res.body.current) {
        adoptPerson(res.body.current);
        adminSays('Somebody else changed that account first. The list now shows where it stands.', true);
        return;
      }
      adminSays(problem(res, {
        no_password: 'That account has never set a password, so it cannot be made active. Send it a link instead.',
        self_change: 'Change your own role or status from another admin\'s account.',
        email_taken: 'That address already has an account.'
      }), true);
      drawPeople(); // put the select back to what the server still believes
    });
  }

  function removePerson(p, control) {
    if (control) control.disabled = true;
    adminSays('');
    request('DELETE', '/users/' + p.id + '?revision=' + encodeURIComponent(p.revision))
      .then(function (res) {
        if (control) control.disabled = false;
        if (res.status === 204) {
          dropPerson(p.id);
          adminSays('Removed ' + p.email + '.');
          return;
        }
        if (res.status === 409 && res.body && res.body.current) {
          adoptPerson(res.body.current);
          adminSays('Somebody else changed that account first. Check it and try again.', true);
          return;
        }
        adminSays(problem(res, {}), true);
      });
  }

  function invite(p, control) {
    if (control) control.disabled = true;
    adminSays('');
    request('POST', '/users/' + p.id + '/invite', {}).then(function (res) {
      if (control) control.disabled = false;
      if (res.status === 200 && res.body) {
        if (res.body.user) adoptPerson(res.body.user);
        afterInvite(res.body, p.email);
        return;
      }
      adminSays(problem(res, {
        account_disabled: 'Turn access back on before sending a link.'
      }), true);
    });
  }

  /* What happens after an account is created or re-invited.
   *
   * With SMTP configured the link goes to the person it belongs to and the
   * admin never sees it, which is the shape that keeps a credential in one
   * pair of hands. Without SMTP the server hands the link back instead,
   * because the alternative is a deployment that cannot onboard anybody at
   * all. It is shown here, once, and kept nowhere. */
  function afterInvite(body, email) {
    var out = byId('adminLinkOut');
    if (body.mailSent || !body.setPasswordUrl) {
      show(out, false);
      adminSays('A set-password link is on its way to ' + email + '.');
      return;
    }
    adminSays('');
    setText(byId('adminLinkLede'),
      'No mail is configured on this deployment, so send this link to ' + email +
      ' yourself. It works once, expires in 24 hours, and is not shown again.');
    var field = byId('adminLinkValue');
    field.value = body.setPasswordUrl;
    show(out, true);
    field.focus();
    field.select();
    // Selecting leaves the caret at the far end, which scrolls a long URL out
    // of its own field. Wind it back, so what is on screen is the start of the
    // link somebody is about to check before they send it on.
    field.scrollLeft = 0;
  }

  function bindAdmin() {
    var form = byId('createUserForm');
    if (!form) return;

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var emailField = byId('newUserEmail');
      var email = (emailField.value || '').trim();
      var role = byId('newUserRole').value;
      if (!email) {
        adminSays('Enter the address the invitation should go to.', true);
        emailField.focus();
        return;
      }
      var submit = byId('createUserSubmit');
      submit.disabled = true;
      adminSays('');
      request('POST', '/users', { email: email, role: role }).then(function (res) {
        submit.disabled = false;
        if (res.status === 201 && res.body && res.body.user) {
          emailField.value = '';
          adoptPerson(res.body.user);
          afterInvite(res.body, res.body.user.email);
          return;
        }
        adminSays(problem(res, {
          email_taken: 'That address already has an account.',
          invalid_email: 'That is not an address the server will accept.',
          link_failed: (res.body && res.body.message) || 'The account was created but no link could be issued. Try sending one again.'
        }), true);
        if (res.status === 409) loadPeople();
      });
    });

    var copy = byId('adminLinkCopy');
    if (copy) {
      copy.addEventListener('click', function () {
        var field = byId('adminLinkValue');
        field.focus();
        field.select();
        var done = function (ok) { setText(copy, ok ? 'Copied' : 'Press Ctrl+C to copy'); };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(field.value).then(function () { done(true); }, function () { done(false); });
        } else {
          done(false);
        }
      });
    }
    var dismiss = byId('adminLinkDismiss');
    if (dismiss) {
      dismiss.addEventListener('click', function () {
        // Out of the page as soon as it has been passed on.
        byId('adminLinkValue').value = '';
        setText(byId('adminLinkCopy'), 'Copy link');
        show(byId('adminLinkOut'), false);
      });
    }
  }

  /* ------------------------------------------------------------------
   * Your account, and its passkeys
   * ------------------------------------------------------------------ */

  function renderAccount() {
    showPanel('panelAccount', 'Your account',
      user ? user.email + ', signed in as ' + roleWord(user.role) + '.' : '');
    show(byId('passkeySection'), PASSKEYS_OFFERED);
    say(byId('passkeyMsg'), '');
    if (PASSKEYS_OFFERED) loadPasskeys();
  }

  var passkeys = [];

  function loadPasskeys() {
    request('GET', '/auth/passkeys').then(function (res) {
      if (res.status !== 200 || !res.body) {
        say(byId('passkeyMsg'), problem(res, {}), true);
        return;
      }
      passkeys = res.body.passkeys || [];
      drawPasskeys();
    });
  }

  // "internal", "hybrid", "usb" is the protocol's vocabulary, not a person's.
  var TRANSPORT_WORD = {
    internal: 'built into a device',
    hybrid: 'a phone or tablet',
    usb: 'a security key',
    nfc: 'a tapped key',
    ble: 'a Bluetooth key',
    'smart-card': 'a smart card',
    cable: 'a cabled key'
  };

  function transportWords(list) {
    var out = [];
    (list || []).forEach(function (t) {
      var word = TRANSPORT_WORD[t];
      if (word && out.indexOf(word) < 0) out.push(word);
    });
    return out.join(', ');
  }

  function drawPasskeys() {
    var list = byId('passkeyList');
    if (!list) return;
    while (list.firstChild) list.removeChild(list.firstChild);
    show(byId('passkeyEmpty'), passkeys.length === 0);

    passkeys.forEach(function (k) {
      var li = make('li', 'passkey');
      li.appendChild(make('span', 'passkey-label', k.label || 'Unnamed passkey'));

      var where = transportWords(k.transports);
      var line = 'added ' + day(k.createdAt);
      if (where) line = where + ', ' + line;
      li.appendChild(make('span', 'passkey-meta', line));
      li.appendChild(make('span', 'passkey-used',
        k.lastUsedAt ? 'last used ' + day(k.lastUsedAt) : 'not used yet'));

      li.appendChild(actButton('Remove', 'link-btn danger', function (btn) {
        if (!window.confirm('Remove ' + (k.label || 'this passkey') + '? Signing in with that device will stop working.')) return;
        btn.disabled = true;
        request('DELETE', '/auth/passkeys/' + k.id).then(function (res) {
          btn.disabled = false;
          if (res.status === 204) {
            passkeys = passkeys.filter(function (x) { return x.id !== k.id; });
            drawPasskeys();
            say(byId('passkeyMsg'), 'Removed.');
            return;
          }
          say(byId('passkeyMsg'), problem(res, {}), true);
        });
      }));
      list.appendChild(li);
    });
  }

  /* ---------- The WebAuthn boundary ----------
   *
   * Every field the browser hands back is an ArrayBuffer and every field the
   * server speaks is unpadded base64url, so something has to translate. Where
   * the browser offers to do it — parseCreationOptionsFromJSON, toJSON — it
   * is used, because its version of the rule is the one that stays right as
   * the specification grows fields. The manual path underneath is the same
   * rule written out, for browsers that do not have those yet.
   *
   * The one place this is easy to get wrong: `id` is already a base64url
   * string as the browser reports it, and `rawId` is the same bytes as a
   * buffer. Encoding `id` a second time produces a value that does not match
   * its own rawId, and the server's parser refuses the pair — loudly, which
   * is the good outcome, but only if it never ships.
   */

  function creationOptions(json) {
    if (typeof window.PublicKeyCredential.parseCreationOptionsFromJSON === 'function') {
      return window.PublicKeyCredential.parseCreationOptionsFromJSON(json);
    }
    var opts = {};
    for (var k in json) if (Object.prototype.hasOwnProperty.call(json, k)) opts[k] = json[k];
    opts.challenge = fromB64url(json.challenge);
    opts.user = {
      id: fromB64url(json.user.id),
      name: json.user.name,
      displayName: json.user.displayName
    };
    opts.excludeCredentials = (json.excludeCredentials || []).map(function (c) {
      return { type: c.type, id: fromB64url(c.id), transports: c.transports };
    });
    return opts;
  }

  function requestOptions(json) {
    if (typeof window.PublicKeyCredential.parseRequestOptionsFromJSON === 'function') {
      return window.PublicKeyCredential.parseRequestOptionsFromJSON(json);
    }
    var opts = {};
    for (var k in json) if (Object.prototype.hasOwnProperty.call(json, k)) opts[k] = json[k];
    opts.challenge = fromB64url(json.challenge);
    // allowCredentials is deliberately absent from this server's answer:
    // credentials are discoverable, so the authenticator offers its own
    // account picker and no list has to be published. If one ever arrives,
    // decode it rather than handing the browser strings.
    if (json.allowCredentials) {
      opts.allowCredentials = json.allowCredentials.map(function (c) {
        return { type: c.type, id: fromB64url(c.id), transports: c.transports };
      });
    }
    return opts;
  }

  function registrationJSON(cred) {
    if (typeof cred.toJSON === 'function') return cred.toJSON();
    var r = cred.response;
    return {
      id: cred.id,                                  // already base64url
      rawId: toB64url(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
      response: {
        clientDataJSON: toB64url(r.clientDataJSON),
        attestationObject: toB64url(r.attestationObject),
        transports: r.getTransports ? r.getTransports() : []
      }
    };
  }

  function assertionJSON(cred) {
    if (typeof cred.toJSON === 'function') return cred.toJSON();
    var r = cred.response;
    return {
      id: cred.id,                                  // already base64url
      rawId: toB64url(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
      response: {
        clientDataJSON: toB64url(r.clientDataJSON),
        authenticatorData: toB64url(r.authenticatorData),
        signature: toB64url(r.signature),
        // Present for a discoverable credential, which is the only kind this
        // server registers — it is how the assertion names the account.
        userHandle: r.userHandle ? toB64url(r.userHandle) : null
      }
    };
  }

  // A ceremony the person waved away is not a failure worth shouting about.
  function ceremonyMessage(err, fallback) {
    var name = err && err.name;
    if (name === 'NotAllowedError') return '';
    if (name === 'InvalidStateError') return 'This device already has a passkey for this account.';
    if (name === 'SecurityError') return 'This page\'s address does not match the one passkeys were set up for.';
    return fallback;
  }

  function bindPasskeys() {
    var form = byId('passkeyForm');
    if (!form) return;
    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      registerPasskey();
    });
  }

  function registerPasskey() {
    var msg = byId('passkeyMsg');
    var add = byId('passkeyAdd');
    var labelField = byId('passkeyLabel');
    var label = (labelField.value || '').trim();

    add.disabled = true;
    say(msg, 'Follow the prompt from your device.');

    request('POST', '/auth/passkeys/register/begin').then(function (res) {
      if (res.status !== 200 || !res.body || !res.body.publicKey) {
        add.disabled = false;
        say(msg, problem(res, {
          too_many_passkeys: (res.body && res.body.message) || 'This account already holds as many passkeys as it may.'
        }), true);
        return null;
      }
      var options;
      try {
        options = creationOptions(res.body.publicKey);
      } catch (e) {
        add.disabled = false;
        say(msg, 'This browser could not read the setup this server offered.', true);
        return null;
      }
      return navigator.credentials.create({ publicKey: options })
        .then(function (cred) {
          return request('POST', '/auth/passkeys/register/finish', {
            label: label,
            credential: registrationJSON(cred)
          });
        })
        .then(function (fin) {
          add.disabled = false;
          if (fin.status === 201 && fin.body) {
            labelField.value = '';
            passkeys.push(fin.body);
            drawPasskeys();
            say(msg, 'Passkey added. You can sign in with it from now on.');
            return;
          }
          say(msg, problem(fin, {
            passkey_exists: 'That passkey is already registered.',
            invalid_challenge: 'That took too long. Try adding it again.',
            invalid_passkey: 'That registration could not be verified. Try again.'
          }), true);
        }, function (err) {
          add.disabled = false;
          say(msg, ceremonyMessage(err, 'Your device did not complete the setup. Try again.'), true);
        });
    });
  }

  function passkeyLogin() {
    var msg = byId('loginMsg');
    var btn = byId('loginPasskey');
    btn.disabled = true;
    say(msg, 'Follow the prompt from your device.');

    // No email is sent and none is needed: the server never looks one up, and
    // a list of credentials for an address — full for a real account, empty
    // for a stranger — is the account enumeration this whole surface avoids.
    // The authenticator shows its own picker.
    request('POST', '/auth/passkeys/login/begin').then(function (res) {
      if (res.status !== 200 || !res.body || !res.body.publicKey) {
        btn.disabled = false;
        say(msg, problem(res, {}), true);
        return null;
      }
      var options;
      try {
        options = requestOptions(res.body.publicKey);
      } catch (e) {
        btn.disabled = false;
        say(msg, 'This browser could not read the challenge this server offered.', true);
        return null;
      }
      return navigator.credentials.get({ publicKey: options })
        .then(function (cred) {
          return request('POST', '/auth/passkeys/login/finish', { credential: assertionJSON(cred) });
        })
        .then(function (fin) {
          btn.disabled = false;
          if (fin.status === 200 && fin.body) {
            loginSucceeded(fin.body);
            return;
          }
          say(msg, problem(fin, {
            invalid_credentials: 'That passkey did not sign you in. Try your password instead.'
          }), true);
        }, function (err) {
          btn.disabled = false;
          say(msg, ceremonyMessage(err, 'Your device did not complete the sign-in. Try again.'), true);
        });
    });
  }

  /* ------------------------------------------------------------------
   * Wiring
   * ------------------------------------------------------------------ */

  bindLogin();
  bindSetPassword();
  bindAdmin();
  bindPasskeys();

  var signOutBtn = byId('signOutBtn');
  if (signOutBtn) signOutBtn.addEventListener('click', signOut);

  each(document.querySelectorAll('[data-auth-goto]'), function (el) {
    el.addEventListener('click', function () { goto(el.getAttribute('data-auth-goto')); });
  });

  var noteAct = byId('authNoteAct');
  if (noteAct) noteAct.addEventListener('click', function () { goto('login'); });

  render();
  probeSession();
})();
