/* soiree — collaborative event budget and task planner.
 *
 * Vanilla JS, no framework, no build-time transpile. The server injects
 * runtime configuration as a JSON data block in the document; nothing here is
 * specific to any one event.
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'soiree.v1';

  /* ------------------------------------------------------------------
   * CONFIGURATION
   * ------------------------------------------------------------------
   * Injected by the server from SOIREE_* environment variables. Read from a
   * <script type="application/json"> data block, which a strict script-src
   * CSP permits because it is never executed. Inlining it costs no extra
   * round trip, which matters when the origin is far away.
   * ------------------------------------------------------------------ */
  var CONFIG = (function () {
    var fallback = {
      eventName: 'A Celebration',
      tagline: '',
      eventDate: '',
      currency: 'EUR',
      locale: 'en-US',
      secondaryCurrency: '',
      secondaryLocale: 'en-US',
      ceiling: 0,
      demoData: false
    };
    try {
      var el = document.getElementById('soiree-config');
      if (!el) return fallback;
      var parsed = JSON.parse(el.textContent);
      for (var k in fallback) {
        if (!(k in parsed) || parsed[k] === null) parsed[k] = fallback[k];
      }
      return parsed;
    } catch (e) {
      return fallback;
    }
  })();

  // Parsed as an instant, not a local wall-clock time. Without an explicit
  // timezone the countdown silently differs by a day between viewers, which
  // is visible when the people planning an event are on different continents.
  var EVENT_DATE = CONFIG.eventDate ? new Date(CONFIG.eventDate) : null;
  if (EVENT_DATE && isNaN(EVENT_DATE.getTime())) EVENT_DATE = null;

  function uid(prefix) {
    return prefix + Math.random().toString(36).slice(2, 9);
  }

  // Every figure on the page is written through here, so a panel can drop an
  // element without the render path having to know about it.
  function setText(id, value) {
    var el = document.getElementById(id);
    if (el) el.textContent = value;
  }

  var DEFAULT_COL_WIDTHS = [230, 110, 70, 135, 110, 135, 135, 230, 40];

  function emptyState() {
    return {
      ceiling: Number(CONFIG.ceiling) || 0,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      colWidths: DEFAULT_COL_WIDTHS.slice(),
      rowHeights: {},
      sponsors: [],
      budgetItems: [],
      tasks: [],
      notes: []
    };
  }

  // Obviously-synthetic sample data, only when explicitly switched on. Exists
  // so a fresh instance and the screenshots have something to show.
  function demoState() {
    var s = emptyState();
    var ada = uid('s'), grace = uid('s'), linus = uid('s');
    s.inflationPct = 4;
    s.ceiling = s.ceiling || 10000;
    s.sponsors = [
      { id: ada, code: 'Rose', name: 'Ada' },
      { id: grace, code: 'Ivy', name: 'Grace' },
      { id: linus, code: 'Fern', name: 'Linus' }
    ];
    s.budgetItems = [
      { id: uid('b'), item: 'Venue deposit', unit: 2500, qty: 1, paid: 500, sponsors: [ada], note: 'Balance due one month before' },
      { id: uid('b'), item: 'Catering', unit: 45, qty: 40, paid: 0, sponsors: [ada, grace], note: 'Per head, shared cost' },
      { id: uid('b'), item: 'Flowers', unit: 300, qty: 1, paid: 300, sponsors: [grace], note: '' },
      { id: uid('b'), item: 'Photographer', unit: 900, qty: 1, paid: 0, sponsors: [linus], note: 'Four hours' },
      { id: uid('b'), item: 'Printed invitations', unit: 4, qty: 40, paid: 160, sponsors: [], note: 'Unassigned so far' }
    ];
    s.tasks = [
      { id: uid('t'), name: 'Confirm final guest count', owner: 'Ada', due: '', status: 'in-progress' },
      { id: uid('t'), name: 'Send invitations', owner: 'Grace', due: '', status: 'not-started' },
      { id: uid('t'), name: 'Book the photographer', owner: 'Linus', due: '', status: 'done' }
    ];
    s.notes = [
      { id: uid('n'), text: 'Venue balance is due a month out — do not let this slip.' }
    ];
    return s;
  }

  /* ------------------------------------------------------------------
   * PERSISTENCE ADAPTER
   * ------------------------------------------------------------------
   * Every read and write of planner data goes through Store. Nothing else in
   * this file touches localStorage. To move onto a shared backend, replace
   * the two methods below and leave the rest of the file alone.
   *
   *   Store.read()       -> state object, or null if nothing saved yet
   *   Store.write(state) -> persist the whole state object
   *
   * save() is debounced, so the write path is already async-shaped: making
   * these methods return promises does not require touching the ~24 call
   * sites that mutate state. Note there is no conflict resolution here —
   * last write wins — so a multi-user backend wants per-field writes or a
   * revision check.
   * ------------------------------------------------------------------ */
  var Store = {
    key: STORAGE_KEY,
    read: function () {
      try {
        var raw = localStorage.getItem(this.key);
        return raw ? JSON.parse(raw) : null;
      } catch (e) { return null; }
    },
    write: function (s) {
      try { localStorage.setItem(this.key, JSON.stringify(s)); } catch (e) { /* storage unavailable */ }
    }
  };

  var state;
  try {
    state = Store.read() || (CONFIG.demoData ? demoState() : emptyState());
  } catch (e) {
    state = emptyState();
  }

  // Normalise anything missing or malformed, whether from an older save or a
  // hand-edited import.
  (function normalise() {
    var base = emptyState();
    if (!Array.isArray(state.budgetItems)) state.budgetItems = [];
    if (!Array.isArray(state.tasks)) state.tasks = [];
    if (!Array.isArray(state.sponsors)) state.sponsors = [];
    if (!Array.isArray(state.notes)) state.notes = [];
    if (typeof state.ceiling !== 'number') state.ceiling = base.ceiling;
    if (typeof state.inflationPct !== 'number') state.inflationPct = 0;
    if (typeof state.fxRate !== 'number') state.fxRate = 0;
    if (typeof state.splitEvenly !== 'boolean') state.splitEvenly = false;
    if (!Array.isArray(state.colWidths) || state.colWidths.length !== 9) {
      state.colWidths = DEFAULT_COL_WIDTHS.slice();
    }
    if (!state.rowHeights || typeof state.rowHeights !== 'object') state.rowHeights = {};
    state.budgetItems.forEach(function (i) {
      if (!i.id) i.id = uid('b');
      if (!Array.isArray(i.sponsors)) i.sponsors = [];
      i.sponsors = i.sponsors.filter(function (id) {
        return state.sponsors.some(function (s) { return s.id === id; });
      });
    });
    state.notes.forEach(function (n) { if (!n.id) n.id = uid('n'); });
  })();

  /* ---------- Saving ----------
   * save() is called on every mutation, including each keystroke. Writing
   * synchronously on every one of those blocks the main thread on a full
   * JSON.stringify of the whole state. Debounce it, and flush on the way out
   * so nothing is lost if the tab closes inside the window.
   */
  var SAVE_DEBOUNCE_MS = 500;
  var saveTimer = null;

  function flushSave() {
    if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
    Store.write(state);
  }
  function save() {
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = setTimeout(flushSave, SAVE_DEBOUNCE_MS);
  }
  window.addEventListener('pagehide', flushSave);
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') flushSave();
  });

  /* ---------- Money formatting ----------
   * Formatters are built once. Intl.NumberFormat construction is expensive
   * and these are called per table cell on every render.
   */
  function makeFormat(locale, opts, fallbackFn) {
    var f = null;
    try { f = new Intl.NumberFormat(locale, opts); } catch (e) { f = null; }
    return function (n) {
      if (f) { try { return f.format(n); } catch (e) { /* fall through */ } }
      return fallbackFn(n);
    };
  }

  var CUR = CONFIG.currency || 'EUR';
  var LOC = CONFIG.locale || 'en-US';

  var fmtMoney = makeFormat(LOC, {
    style: 'currency', currency: CUR, maximumFractionDigits: 0
  }, function (n) { return CUR + ' ' + Math.round(Number(n) || 0).toLocaleString(); });

  // Compact notation is locale-aware, so this works for any currency rather
  // than hardcoding magnitude suffixes.
  // minimumFractionDigits has to be pinned to 0: for most currencies it
  // defaults to 2, and a maximum of 1 then clamps the minimum up to 1 rather
  // than down, which renders 960 as "960.0".
  var fmtShortImpl = makeFormat(LOC, {
    style: 'currency', currency: CUR, notation: 'compact',
    minimumFractionDigits: 0, maximumFractionDigits: 1
  }, function (n) {
    var a = Math.abs(Number(n) || 0);
    var sign = (Number(n) || 0) < 0 ? '-' : '';
    if (a >= 1e6) return sign + CUR + (a / 1e6).toFixed(1) + 'M';
    if (a >= 1e3) return sign + CUR + Math.round(a / 1e3) + 'k';
    return sign + CUR + Math.round(a);
  });

  function fmtCur(n) { return fmtMoney(Number(n) || 0); }
  function fmtShort(n) { return fmtShortImpl(Number(n) || 0); }

  var SECONDARY = (CONFIG.secondaryCurrency || '').trim();
  var fmtSecondaryImpl = SECONDARY ? makeFormat(CONFIG.secondaryLocale || LOC, {
    style: 'currency', currency: SECONDARY, maximumFractionDigits: 0
  }, function (v) { return SECONDARY + ' ' + Math.round(v).toLocaleString(); }) : null;

  // Converts using the user-supplied rate, expressed as primary units per one
  // secondary unit. Returns an em dash when no secondary currency is in play.
  function fmtSecondary(n) {
    if (!fmtSecondaryImpl) return '';
    var rate = Number(state.fxRate) || 0;
    if (!rate) return '–';
    return fmtSecondaryImpl((Number(n) || 0) / rate);
  }

  function lineTotal(i) { return (Number(i.unit) || 0) * (Number(i.qty) || 0); }

  function totals() {
    var t = 0, p = 0;
    state.budgetItems.forEach(function (i) {
      t += lineTotal(i);
      p += Number(i.paid) || 0;
    });
    var buffer = t * (1 + (Number(state.inflationPct) || 0) / 100);
    return { total: t, paid: p, owing: t - p, forecast: buffer, ceiling: Number(state.ceiling) || 0 };
  }

  /* ---------- Static labels driven by config ---------- */
  (function applyConfigLabels() {
    Array.prototype.forEach.call(document.querySelectorAll('.cur-code'), function (el) {
      el.textContent = CUR;
    });
    var rateField = document.getElementById('rateField');
    if (!SECONDARY) {
      if (rateField) rateField.style.display = 'none';
    } else {
      var lbl = document.getElementById('rateLabel');
      if (lbl) lbl.textContent = 'Exchange rate (' + CUR + ' per ' + SECONDARY + ')';
    }
    if (!SECONDARY) {
      ['mCommittedEur', 'mOutstandingEur'].forEach(function (id) {
        var el = document.getElementById(id);
        if (el) el.style.display = 'none';
      });
    }
  })();

  // ---------- Tabs ----------
  // Roving tabindex plus arrow keys: this is a tab widget, and a tab widget
  // that only responds to Tab is one a keyboard user has to walk through
  // linearly to reach the third panel.
  var tabBtns = document.querySelectorAll('.tab-btn');

  function selectTab(btn, focus) {
    Array.prototype.forEach.call(tabBtns, function (b) {
      var on = b === btn;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', on ? 'true' : 'false');
      b.tabIndex = on ? 0 : -1;
    });
    Array.prototype.forEach.call(document.querySelectorAll('.tab-panel'), function (p) {
      p.classList.remove('active');
    });
    document.getElementById('panel-' + btn.dataset.tab).classList.add('active');
    if (focus) btn.focus();
  }

  Array.prototype.forEach.call(tabBtns, function (btn, i) {
    btn.addEventListener('click', function () { selectTab(btn, false); });
    btn.addEventListener('keydown', function (e) {
      var step = e.key === 'ArrowRight' ? 1 : e.key === 'ArrowLeft' ? -1 : 0;
      if (step) {
        e.preventDefault();
        selectTab(tabBtns[(i + step + tabBtns.length) % tabBtns.length], true);
      } else if (e.key === 'Home' || e.key === 'End') {
        e.preventDefault();
        selectTab(tabBtns[e.key === 'Home' ? 0 : tabBtns.length - 1], true);
      }
    });
  });

  function showTab(name) {
    for (var i = 0; i < tabBtns.length; i++) {
      if (tabBtns[i].dataset.tab === name) { selectTab(tabBtns[i], false); return; }
    }
  }

  // ---------- Countdown ----------
  function renderCountdown() {
    if (!EVENT_DATE) {
      setText('daysNum', '–');
      setText('daysLabel', 'no date set');
      setText('statDaysLabel', '');
      return;
    }
    var today = new Date();
    today.setHours(0, 0, 0, 0);
    var diffDays = Math.ceil((EVENT_DATE - today) / 86400000);
    setText('daysNum', diffDays >= 0 ? diffDays : 0);
    setText('daysLabel', diffDays >= 0 ? 'days to go' : 'the day has passed');
    var when = '';
    try {
      when = new Intl.DateTimeFormat(LOC, {
        day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC'
      }).format(EVENT_DATE);
    } catch (e) { when = ''; }
    setText('statDaysLabel', when);
  }

  /* ---------- Empty state ----------
   * A fresh instance has nothing to reconcile, so the overview shows what to
   * do first instead of a grid of dashes. Called from every path that can add
   * or remove the first record, not just from renderOverview: adding a sponsor
   * does not touch the overview but does end the empty state.
   */
  function syncEmptyState() {
    var empty = !state.budgetItems.length && !state.tasks.length &&
      !state.sponsors.length && !state.notes.length;
    document.body.classList.toggle('is-empty', empty);
  }

  // ---------- Overview ----------
  function renderOverview() {
    var total = state.tasks.length;
    var done = state.tasks.filter(function (t) { return t.status === 'done'; }).length;
    var taskPct = total ? Math.round((done / total) * 100) : 0;
    setText('taskBarPct', taskPct + '%');
    setText('statTasks', done + ' of ' + total + ' done');
    document.getElementById('taskBarFill').style.width = taskPct + '%';

    var t = totals();

    // Exact figures, not compact ones. These four are the same numbers as the
    // budget table's totals row, and a page that shows "€5.7K" in one place
    // and "€5,660" in another is a page nobody trusts to reconcile against.
    setText('mCommitted', fmtCur(t.total));
    setText('mCommittedEur', fmtSecondary(t.total));
    setText('mForecast', fmtCur(Math.round(t.forecast)));
    setText('mForecastSub', '+' + (Number(state.inflationPct) || 0) + '% on quoted');
    setText('mPaid', fmtCur(t.paid));
    setText('mPaidSub', t.total ? Math.round((t.paid / t.total) * 100) + '% of committed' : '');
    setText('mOutstanding', fmtCur(t.owing));
    setText('mOutstandingEur', fmtSecondary(t.owing));

    var budgetStatEl = document.getElementById('statBudget');
    var budgetFillEl = document.getElementById('budgetBarFill');

    if (t.ceiling > 0) {
      var pct = Math.round((t.total / t.ceiling) * 100);
      budgetStatEl.textContent = pct + '%';
      budgetStatEl.classList.toggle('warn', pct > 100);
      setText('statBudgetSub', fmtShort(t.total) + ' of ' + fmtShort(t.ceiling) +
        ', leaving ' + fmtShort(t.ceiling - t.forecast) + ' after the buffer');
      budgetFillEl.style.width = Math.min(pct, 100) + '%';
      budgetFillEl.classList.toggle('over', pct > 100);
    } else {
      budgetStatEl.textContent = fmtCur(t.total);
      budgetStatEl.classList.remove('warn');
      setText('statBudgetSub', 'No ceiling set — set one in the Budget tab.');
      budgetFillEl.style.width = '0%';
      budgetFillEl.classList.remove('over');
    }

    var paidPct = t.total ? Math.round((t.paid / t.total) * 100) : 0;
    setText('paidBarPct', paidPct + '%');
    setText('paidBarSub', fmtShort(t.paid) + ' of ' + fmtShort(t.total));
    document.getElementById('paidBarFill').style.width = Math.min(paidPct, 100) + '%';

    var upcoming = state.tasks
      .filter(function (t2) { return t2.status !== 'done'; })
      .slice()
      .sort(function (a, b) {
        if (!a.due && !b.due) return 0;
        if (!a.due) return 1;
        if (!b.due) return -1;
        return a.due.localeCompare(b.due);
      })
      .slice(0, 5);

    var list = document.getElementById('upNextList');
    var emptyNote = document.getElementById('upNextEmpty');
    list.innerHTML = '';
    emptyNote.style.display = upcoming.length ? 'none' : 'block';
    upcoming.forEach(function (t3) {
      var li = document.createElement('li');
      li.appendChild(cell('span', '', t3.name || '(untitled task)'));
      li.appendChild(cell('span', 'who', t3.owner || ''));
      li.appendChild(cell('span', 'due', t3.due || ''));
      list.appendChild(li);
    });
    syncEmptyState();
  }

  // Small helper for the two-and-three column list rows, which exist so the
  // owner and the date line up down the page instead of being run together
  // into one string.
  function cell(tag, cls, text) {
    var el = document.createElement(tag);
    if (cls) el.className = cls;
    el.textContent = text;
    return el;
  }

  // ---------- Watch list ----------
  function renderNotes() {
    var host = document.getElementById('watchList');
    var empty = document.getElementById('watchEmpty');
    host.innerHTML = '';
    empty.style.display = state.notes.length ? 'none' : 'block';

    state.notes.forEach(function (note, idx) {
      var row = document.createElement('div');
      row.className = 'flag';

      var ta = document.createElement('textarea');
      ta.rows = 2;
      ta.value = note.text || '';
      ta.placeholder = 'Something to keep an eye on…';
      ta.addEventListener('input', function () {
        state.notes[idx].text = ta.value;
        save();
      });

      var del = delButton('Remove note', function () {
        state.notes.splice(idx, 1);
        save();
        renderNotes();
      });

      row.appendChild(ta);
      row.appendChild(del);
      host.appendChild(row);
    });
    syncEmptyState();
  }

  // "×" is a fine mark to look at and a useless one to hear, so every delete
  // carries a real accessible name.
  function delButton(label, onClick) {
    var b = document.createElement('button');
    b.className = 'del-btn';
    b.type = 'button';
    b.textContent = '×';
    b.title = label;
    b.setAttribute('aria-label', label);
    b.addEventListener('click', onClick);
    return b;
  }

  document.getElementById('addWatch').addEventListener('click', function () {
    state.notes.push({ id: uid('n'), text: '' });
    save();
    renderNotes();
  });

  // ---------- Budget settings ----------
  var ceilingInput = document.getElementById('ceilingInput');
  var inflationInput = document.getElementById('inflationInput');
  var rateInput = document.getElementById('rateInput');

  ceilingInput.addEventListener('input', function () {
    state.ceiling = Number(ceilingInput.value) || 0;
    save(); renderBudgetTotals(); renderOverview();
  });
  inflationInput.addEventListener('input', function () {
    state.inflationPct = Number(inflationInput.value) || 0;
    save(); renderBudgetTotals(); renderOverview();
  });
  rateInput.addEventListener('input', function () {
    state.fxRate = Number(rateInput.value) || 0;
    save(); renderBudgetTotals(); renderOverview();
  });

  function renderBudgetTotals() {
    var t = totals();
    setText('sumTotal', fmtCur(t.total));
    setText('sumPaid', fmtCur(t.paid));
    setText('sumOwing', fmtCur(t.owing));
    // Repeated below the table, which is the copy that survives on a phone
    // once the grid has been scrolled sideways.
    setText('sumTotalAlt', fmtCur(t.total));
    setText('sumOwingAlt', fmtCur(t.owing));
    setText('sumForecast', fmtCur(Math.round(t.forecast)));
    setText('sumHeadroom', t.ceiling > 0 ? fmtCur(Math.round(t.ceiling - t.forecast)) : '–');

    // Rust means outstanding, so it only appears while something is.
    var owingEls = [document.getElementById('sumOwing'), document.getElementById('sumOwingAlt')];
    owingEls.forEach(function (el) { if (el) el.classList.toggle('owing', t.owing > 0); });
    renderSplit();
  }

  // ---------- Sponsors ----------
  function sponsorById(id) {
    for (var i = 0; i < state.sponsors.length; i++) {
      if (state.sponsors[i].id === id) return state.sponsors[i];
    }
    return null;
  }
  function sponsorCode(s) { return (s && s.code ? s.code : '').trim() || 'Untitled'; }
  function sponsorLabel(s) {
    if (!s) return 'Unassigned';
    var n = (s.name || '').trim();
    return n ? sponsorCode(s) + ' — ' + n : sponsorCode(s);
  }
  function itemCodes(item) {
    return (item.sponsors || []).map(function (id) {
      return sponsorCode(sponsorById(id));
    });
  }

  function renderSponsors() {
    var grid = document.getElementById('sponsorGrid');
    grid.innerHTML = '';
    state.sponsors.forEach(function (sp, idx) {
      var row = document.createElement('div');
      row.className = 'sponsor-row';

      var code = document.createElement('input');
      code.type = 'text';
      code.className = 'code-input';
      code.placeholder = 'Callsign';
      code.value = sp.code || '';
      code.addEventListener('input', function () {
        state.sponsors[idx].code = code.value;
        save();
        renderBudgetTable();
        renderSplit();
      });

      var name = document.createElement('input');
      name.type = 'text';
      name.className = 'name-input';
      name.placeholder = 'Who this is';
      name.value = sp.name || '';
      name.addEventListener('input', function () {
        state.sponsors[idx].name = name.value;
        save();
        renderSplit();
      });

      var amt = document.createElement('span');
      amt.className = 'sp-amt';
      amt.textContent = fmtCur(sponsorShare(sp.id));

      var del = delButton('Remove sponsor', function () {
        var id = state.sponsors[idx].id;
        state.sponsors.splice(idx, 1);
        state.budgetItems.forEach(function (i) {
          i.sponsors = (i.sponsors || []).filter(function (x) { return x !== id; });
        });
        save();
        renderSponsors();
        renderBudgetTable();
        renderSplit();
      });

      row.appendChild(code);
      row.appendChild(name);
      row.appendChild(amt);
      row.appendChild(del);
      grid.appendChild(row);
    });
    syncEmptyState();
  }

  // Amount attributed to one sponsor, shared lines divided evenly.
  function sponsorShare(id) {
    var sum = 0;
    state.budgetItems.forEach(function (i) {
      var ids = i.sponsors || [];
      if (ids.indexOf(id) !== -1) sum += lineTotal(i) / ids.length;
    });
    return Math.round(sum);
  }

  document.getElementById('addSponsor').addEventListener('click', function () {
    state.sponsors.push({ id: uid('s'), code: '', name: '' });
    save();
    renderSponsors();
    renderBudgetTable();
    renderSplit();
  });

  var splitToggle = document.getElementById('splitEvenly');
  splitToggle.addEventListener('change', function () {
    state.splitEvenly = splitToggle.checked;
    save();
    renderSplit();
  });

  function renderSplit() {
    var groups = {};
    var order = [];
    function add(key, amount) {
      if (!(key in groups)) { groups[key] = 0; order.push(key); }
      groups[key] += amount;
    }

    if (state.splitEvenly) {
      state.sponsors.forEach(function (sp) { add(sponsorLabel(sp), 0); });
      state.budgetItems.forEach(function (i) {
        var ids = i.sponsors || [];
        var tot = lineTotal(i);
        if (!ids.length) { add('Unassigned', tot); return; }
        ids.forEach(function (id) { add(sponsorLabel(sponsorById(id)), tot / ids.length); });
      });
    } else {
      state.budgetItems.forEach(function (i) {
        var codes = itemCodes(i);
        var key = codes.length ? codes.join(' + ') + (codes.length > 1 ? ' (shared)' : '') : 'Unassigned';
        add(key, lineTotal(i));
      });
    }

    var list = document.getElementById('splitList');
    list.innerHTML = '';
    var grand = totals().total;
    order.sort(function (a, b) { return groups[b] - groups[a]; }).forEach(function (k) {
      var li = document.createElement('li');
      var share = grand ? Math.round((groups[k] / grand) * 100) : 0;
      li.appendChild(cell('span', '', k));
      li.appendChild(cell('span', 'amt', fmtCur(Math.round(groups[k]))));
      li.appendChild(cell('span', 'pct', share + '%'));
      list.appendChild(li);
    });

    // keep the per-sponsor amounts in the editor in step
    var rows = document.querySelectorAll('#sponsorGrid .sponsor-row');
    state.sponsors.forEach(function (sp, idx) {
      if (rows[idx]) rows[idx].querySelector('.sp-amt').textContent = fmtCur(sponsorShare(sp.id));
    });
  }

  /* ---------- Cost-by picker ----------
   * The popup is appended to the body, so without moving focus into it a
   * keyboard user opening the picker would land on the next cell instead and
   * have to tab the rest of the page to reach it. Assigning a cost to a
   * sponsor is a data operation, not a display preference, so it gets the
   * full treatment: focus in on open, Escape to close back onto the button,
   * and tabbing out dismisses it.
   */
  var openPop = null;
  var openBtn = null;

  function closePop(returnFocus) {
    if (!openPop) return;
    openPop.remove();
    openPop = null;
    if (openBtn) {
      openBtn.setAttribute('aria-expanded', 'false');
      if (returnFocus) openBtn.focus();
    }
    openBtn = null;
  }
  document.addEventListener('click', function (e) {
    if (openPop && !openPop.contains(e.target) && !e.target.classList.contains('by-btn')) closePop();
  });
  window.addEventListener('resize', function () { closePop(); });

  function openPicker(btn, item) {
    closePop();
    var pop = document.createElement('div');
    pop.className = 'sp-pop';

    if (!state.sponsors.length) {
      var none = document.createElement('div');
      none.className = 'pop-note';
      none.style.borderTop = 'none';
      none.textContent = 'No sponsors yet — add one above.';
      pop.appendChild(none);
    }

    state.sponsors.forEach(function (sp) {
      var label = document.createElement('label');
      var cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = (item.sponsors || []).indexOf(sp.id) !== -1;
      cb.addEventListener('change', function () {
        if (!Array.isArray(item.sponsors)) item.sponsors = [];
        var pos = item.sponsors.indexOf(sp.id);
        if (cb.checked && pos === -1) item.sponsors.push(sp.id);
        if (!cb.checked && pos !== -1) item.sponsors.splice(pos, 1);
        save();
        setByLabel(btn, item);
        renderSplit();
      });
      var txt = document.createElement('span');
      txt.textContent = sponsorLabel(sp);
      label.appendChild(cb);
      label.appendChild(txt);
      pop.appendChild(label);
    });

    var note = document.createElement('div');
    note.className = 'pop-note';
    note.textContent = 'Tick more than one for a shared cost.';
    pop.appendChild(note);

    pop.tabIndex = -1;
    pop.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { e.stopPropagation(); closePop(true); }
    });
    // Tabbing past the last checkbox dismisses rather than stranding the
    // popup open behind the rest of the page.
    pop.addEventListener('focusout', function (e) {
      if (!pop.contains(e.relatedTarget)) closePop(false);
    });

    document.body.appendChild(pop);
    var r = btn.getBoundingClientRect();
    var top = r.bottom + 4;
    if (top + pop.offsetHeight > window.innerHeight - 8) {
      top = Math.max(8, r.top - pop.offsetHeight - 4);
    }
    var left = Math.min(r.left, window.innerWidth - pop.offsetWidth - 8);
    pop.style.top = top + 'px';
    pop.style.left = Math.max(8, left) + 'px';
    openPop = pop;
    openBtn = btn;
    btn.setAttribute('aria-expanded', 'true');
    var firstBox = pop.querySelector('input[type="checkbox"]');
    (firstBox || pop).focus();
  }

  function setByLabel(btn, item) {
    var codes = itemCodes(item);
    btn.textContent = codes.length ? codes.join(' · ') : 'Unassigned';
    btn.classList.toggle('none', !codes.length);
  }

  // ---------- Resizing the budget grid ----------
  var MIN_COL = 48;
  var MIN_ROW = 34;

  function applyColWidths() {
    var cols = document.getElementById('budgetCols');
    cols.innerHTML = '';
    var total = 0;
    state.colWidths.forEach(function (w) {
      var col = document.createElement('col');
      col.style.width = w + 'px';
      cols.appendChild(col);
      total += w;
    });
    document.getElementById('budgetTable').style.width = total + 'px';
  }

  function initColGrips() {
    var ths = document.querySelectorAll('#budgetTable thead th');
    Array.prototype.forEach.call(ths, function (th, i) {
      if (i >= state.colWidths.length - 1) return; // no handle on the delete column
      if (th.querySelector('.col-grip')) return;
      var grip = document.createElement('div');
      grip.className = 'col-grip';
      grip.title = 'Drag to resize column';
      grip.addEventListener('pointerdown', function (e) {
        e.preventDefault();
        e.stopPropagation();
        grip.setPointerCapture(e.pointerId);
        grip.classList.add('active');
        var startX = e.clientX;
        var startW = state.colWidths[i];

        function move(ev) {
          state.colWidths[i] = Math.max(MIN_COL, Math.round(startW + (ev.clientX - startX)));
          applyColWidths();
        }
        function up() {
          grip.classList.remove('active');
          grip.removeEventListener('pointermove', move);
          grip.removeEventListener('pointerup', up);
          grip.removeEventListener('pointercancel', up);
          save();
        }
        grip.addEventListener('pointermove', move);
        grip.addEventListener('pointerup', up);
        grip.addEventListener('pointercancel', up);
      });
      th.appendChild(grip);
    });
  }

  function attachRowGrip(td, tr, item) {
    var grip = document.createElement('div');
    grip.className = 'row-grip';
    grip.title = 'Drag to resize row';
    grip.addEventListener('pointerdown', function (e) {
      e.preventDefault();
      e.stopPropagation();
      grip.setPointerCapture(e.pointerId);
      grip.classList.add('active');
      var startY = e.clientY;
      var startH = tr.getBoundingClientRect().height;

      function move(ev) {
        var h = Math.max(MIN_ROW, Math.round(startH + (ev.clientY - startY)));
        tr.style.height = h + 'px';
        state.rowHeights[item.id] = h;
      }
      function up() {
        grip.classList.remove('active');
        grip.removeEventListener('pointermove', move);
        grip.removeEventListener('pointerup', up);
        grip.removeEventListener('pointercancel', up);
        save();
      }
      grip.addEventListener('pointermove', move);
      grip.addEventListener('pointerup', up);
      grip.addEventListener('pointercancel', up);
    });
    td.appendChild(grip);
  }

  document.getElementById('resetSizes').addEventListener('click', function () {
    state.colWidths = DEFAULT_COL_WIDTHS.slice();
    state.rowHeights = {};
    save();
    applyColWidths();
    renderBudgetTable();
  });

  // A table header over nothing reads as broken, so an empty grid says so in
  // the space the first row will occupy.
  function emptyRow(body, span, text) {
    var tr = document.createElement('tr');
    var td = document.createElement('td');
    td.className = 'empty-cell';
    td.colSpan = span;
    td.textContent = text;
    tr.appendChild(td);
    body.appendChild(tr);
  }

  function renderBudgetTable() {
    var body = document.getElementById('budgetBody');
    body.innerHTML = '';
    if (!state.budgetItems.length) {
      emptyRow(body, 9, 'No budget lines yet. Add the first one below.');
    }
    state.budgetItems.forEach(function (item, idx) {
      var tr = document.createElement('tr');

      function textCell(key, cls) {
        var td = document.createElement('td');
        if (cls) td.className = cls;
        var inp = document.createElement('textarea');
        inp.rows = 1;
        inp.value = item[key] || '';
        inp.addEventListener('input', function () {
          state.budgetItems[idx][key] = inp.value;
          save();
        });
        td.appendChild(inp);
        return td;
      }

      function numCell(key, step) {
        var td = document.createElement('td');
        td.className = 'num-cell';
        var inp = document.createElement('input');
        inp.type = 'number';
        inp.min = '0';
        if (step) inp.step = step;
        inp.value = Number(item[key]) || 0;
        inp.addEventListener('input', function () {
          state.budgetItems[idx][key] = Number(inp.value) || 0;
          save();
          refreshRow(tr, state.budgetItems[idx]);
          renderBudgetTotals();
          renderOverview();
        });
        td.appendChild(inp);
        return td;
      }

      var tdItem = textCell('item', 'item-cell');
      var tdUnit = numCell('unit', '1');
      var tdQty = numCell('qty', '1');

      var tdTotal = document.createElement('td');
      tdTotal.className = 'calc strong';

      var tdPaid = numCell('paid', '1');

      var tdOwing = document.createElement('td');
      tdOwing.className = 'calc';

      var tdBy = document.createElement('td');
      var byBtn = document.createElement('button');
      byBtn.className = 'by-btn';
      byBtn.type = 'button';
      byBtn.setAttribute('aria-haspopup', 'true');
      byBtn.setAttribute('aria-expanded', 'false');
      setByLabel(byBtn, item);
      byBtn.addEventListener('click', function (ev) {
        ev.stopPropagation();
        openPicker(byBtn, state.budgetItems[idx]);
      });
      tdBy.appendChild(byBtn);

      var tdNote = textCell('note', 'note-cell');

      var tdDel = document.createElement('td');
      tdDel.className = 'del-cell';
      tdDel.appendChild(delButton('Remove budget line', function () {
        state.budgetItems.splice(idx, 1);
        save();
        renderBudgetTable();
        renderBudgetTotals();
        renderOverview();
      }));

      tr.appendChild(tdItem);
      tr.appendChild(tdUnit);
      tr.appendChild(tdQty);
      tr.appendChild(tdTotal);
      tr.appendChild(tdPaid);
      tr.appendChild(tdOwing);
      tr.appendChild(tdBy);
      tr.appendChild(tdNote);
      tr.appendChild(tdDel);
      body.appendChild(tr);

      if (state.rowHeights[item.id]) tr.style.height = state.rowHeights[item.id] + 'px';
      attachRowGrip(tdItem, tr, item);
      refreshRow(tr, item);
    });
    syncEmptyState();
  }

  function refreshRow(tr, item) {
    var tot = lineTotal(item);
    var owing = tot - (Number(item.paid) || 0);
    tr.children[3].textContent = fmtCur(tot);
    tr.children[5].textContent = fmtCur(owing);
    tr.children[5].classList.toggle('owing', owing > 0);
  }

  document.getElementById('addBudgetRow').addEventListener('click', function () {
    state.budgetItems.push({ id: uid('b'), item: '', unit: 0, qty: 1, paid: 0, sponsors: [], note: '' });
    save();
    renderBudgetTable();
    renderBudgetTotals();
    renderOverview();
  });

  // ---------- Tasks ----------
  var currentFilter = 'all';
  var STATUS_LABELS = { 'not-started': 'Not started', 'in-progress': 'In progress', 'done': 'Done' };

  function setFilter(name) {
    currentFilter = name;
    Array.prototype.forEach.call(document.querySelectorAll('#taskFilters .pill'), function (b) {
      var on = b.dataset.filter === name;
      b.classList.toggle('active', on);
      b.setAttribute('aria-pressed', on ? 'true' : 'false');
    });
    renderTasksTable();
  }

  Array.prototype.forEach.call(document.querySelectorAll('#taskFilters .pill'), function (btn) {
    btn.addEventListener('click', function () { setFilter(btn.dataset.filter); });
  });

  function renderTasksTable() {
    var body = document.getElementById('tasksBody');
    body.innerHTML = '';
    var shown = 0;
    state.tasks.forEach(function (task, idx) {
      if (currentFilter !== 'all' && task.status !== currentFilter) return;
      shown++;
      var tr = document.createElement('tr');

      var tdName = document.createElement('td');
      var nameInput = document.createElement('input');
      nameInput.type = 'text';
      nameInput.value = task.name;
      nameInput.addEventListener('input', function () {
        state.tasks[idx].name = nameInput.value;
        save();
      });
      tdName.appendChild(nameInput);

      var tdOwner = document.createElement('td');
      var ownerInput = document.createElement('input');
      ownerInput.type = 'text';
      ownerInput.value = task.owner || '';
      ownerInput.addEventListener('input', function () {
        state.tasks[idx].owner = ownerInput.value;
        save();
        renderOverview();
      });
      tdOwner.appendChild(ownerInput);

      var tdDue = document.createElement('td');
      var dueInput = document.createElement('input');
      dueInput.type = 'date';
      dueInput.value = task.due || '';
      dueInput.addEventListener('input', function () {
        state.tasks[idx].due = dueInput.value;
        save();
        renderOverview();
      });
      tdDue.appendChild(dueInput);

      var tdStatus = document.createElement('td');
      var statusSelect = document.createElement('select');
      statusSelect.className = 'status-select';
      Object.keys(STATUS_LABELS).forEach(function (key) {
        var opt = document.createElement('option');
        opt.value = key;
        opt.textContent = STATUS_LABELS[key];
        if (task.status === key) opt.selected = true;
        statusSelect.appendChild(opt);
      });
      statusSelect.addEventListener('change', function () {
        state.tasks[idx].status = statusSelect.value;
        save();
        renderOverview();
        if (currentFilter !== 'all') renderTasksTable();
      });
      tdStatus.appendChild(statusSelect);

      var tdDel = document.createElement('td');
      tdDel.className = 'del-cell';
      tdDel.appendChild(delButton('Remove task', function () {
        state.tasks.splice(idx, 1);
        save();
        renderTasksTable();
        renderOverview();
      }));

      tr.appendChild(tdName);
      tr.appendChild(tdOwner);
      tr.appendChild(tdDue);
      tr.appendChild(tdStatus);
      tr.appendChild(tdDel);
      body.appendChild(tr);
    });
    if (!shown) {
      emptyRow(body, 5, state.tasks.length
        ? 'No tasks with that status.'
        : 'No tasks yet. Add the first one below.');
    }
    syncEmptyState();
  }

  document.getElementById('addTaskRow').addEventListener('click', function () {
    state.tasks.push({ id: uid('t'), name: '', owner: '', due: '', status: 'not-started' });
    save();
    setFilter('all');
    renderOverview();
  });

  /* ---------- First run ----------
   * Each step does the thing it names rather than explaining where to find
   * it: the button moves to the Budget tab and starts the record.
   */
  function startStep(id, fn) {
    var btn = document.getElementById(id);
    if (btn) btn.addEventListener('click', fn);
  }
  startStep('startCeiling', function () {
    showTab('budget');
    ceilingInput.focus();
    ceilingInput.select();
  });
  startStep('startSponsor', function () {
    showTab('budget');
    document.getElementById('addSponsor').click();
    var first = document.querySelector('#sponsorGrid .code-input');
    if (first) first.focus();
  });
  startStep('startLine', function () {
    showTab('budget');
    document.getElementById('addBudgetRow').click();
    var first = document.querySelector('#budgetBody textarea');
    if (first) first.focus();
  });

  // ---------- Data portability ----------
  function renderAll() {
    ceilingInput.value = state.ceiling || '';
    inflationInput.value = state.inflationPct;
    rateInput.value = state.fxRate || '';
    splitToggle.checked = !!state.splitEvenly;
    syncEmptyState();
    renderSponsors();
    applyColWidths();
    renderBudgetTable();
    renderBudgetTotals();
    renderTasksTable();
    renderNotes();
    renderOverview();
  }

  function flash(msg) {
    var el = document.getElementById('dataMsg');
    el.textContent = msg;
    setTimeout(function () { if (el.textContent === msg) el.textContent = ''; }, 6000);
  }

  document.getElementById('exportData').addEventListener('click', function () {
    flushSave();
    var json = JSON.stringify(state, null, 2);
    var filename = 'soiree-' + new Date().toISOString().slice(0, 10) + '.json';
    try {
      var blob = new Blob([json], { type: 'application/json' });
      var url = URL.createObjectURL(blob);
      var a = document.createElement('a');
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
      flash('Exported ' + filename);
    } catch (e) {
      flash('Could not export automatically — copy the JSON from the console instead.');
      console.log(json);
    }
  });

  var importFile = document.getElementById('importFile');
  document.getElementById('importData').addEventListener('click', function () {
    importFile.click();
  });
  importFile.addEventListener('change', function () {
    var f = importFile.files && importFile.files[0];
    if (!f) return;
    var reader = new FileReader();
    reader.onload = function () {
      try {
        var incoming = JSON.parse(reader.result);
        if (!incoming || !Array.isArray(incoming.budgetItems) || !Array.isArray(incoming.tasks)) {
          flash('That file does not look like planner data.');
          return;
        }
        if (!window.confirm('Replace everything currently in this planner with the imported data?')) return;
        state = incoming;
        if (!Array.isArray(state.sponsors)) state.sponsors = [];
        if (!Array.isArray(state.notes)) state.notes = [];
        if (!Array.isArray(state.colWidths) || state.colWidths.length !== 9) state.colWidths = DEFAULT_COL_WIDTHS.slice();
        if (!state.rowHeights || typeof state.rowHeights !== 'object') state.rowHeights = {};
        if (typeof state.fxRate !== 'number') state.fxRate = Number(state.eurRate) || 0;
        flushSave();
        renderAll();
        flash('Imported ' + f.name);
      } catch (e) {
        flash('Could not read that file.');
      }
      importFile.value = '';
    };
    reader.readAsText(f);
  });

  // ---------- Init ----------
  renderCountdown();
  initColGrips();
  renderAll();

  // Offline shell. Registered last so it never delays first paint.
  if ('serviceWorker' in navigator) {
    window.addEventListener('load', function () {
      navigator.serviceWorker.register('/sw.js').catch(function () { /* non-fatal */ });
    });
  }
})();
