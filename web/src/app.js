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
      demoData: false,
      // Read if the server ever sends it. Nothing in the Go config emits this
      // key today, so the interface language falls back to the locale below —
      // which already carries the language tag an operator has configured.
      language: ''
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

  /* ------------------------------------------------------------------
   * INTERFACE LANGUAGE
   * ------------------------------------------------------------------
   * One table, one lookup, no dependency. English is the fallback in both
   * directions: an unknown language falls back to it, and so does any single
   * key a translation has not reached yet — so a half-finished language ships
   * a mixed page rather than a page full of raw ids.
   *
   * Resolution order, first hit wins:
   *   1. ?lang= in the URL   — makes a link shareable in one language, and is
   *                            deliberately not persisted: it is a view of the
   *                            page, not a setting on the planner.
   *   2. config.language     — read if the server ever sends it.
   *   3. config.locale       — what an operator already configures, and it
   *                            carries the language tag: nl-NL -> nl.
   *   4. 'en'
   *
   * Currency and date formatting stay on config.locale throughout. Language is
   * what the interface is written in; locale is how numbers are spelled, and
   * an event priced in one country can be read by people in another.
   * ------------------------------------------------------------------ */
  var LANGS = ['en', 'nl', 'id'];

  var STRINGS = {
    en: {
      'nav.sections': 'Planner sections',
      'tab.overview': 'Overview',
      'tab.budget': 'Budget',
      'tab.tasks': 'Tasks',
      'cd.togo': 'days to go',
      'cd.togo1': 'day to go',
      'cd.none': 'no date set',
      'cd.passed': 'the day has passed',
      'cd.closed': 'the ledger is closed',
      'fr.title': 'Nothing in the ledger yet',
      'fr.body': 'This planner holds one event’s money and jobs in one place: what each thing costs, who is covering it, what has been paid and what is still owed. Everything stays in this browser until you export it.',
      'fr.s1': 'Set the ceiling',
      'fr.n1': 'The number the total should not cross.',
      'fr.s2': 'Name who is paying',
      'fr.n2': 'Short callsigns you can tag each cost with.',
      'fr.b2': 'Add a sponsor',
      'fr.s3': 'Enter the first cost',
      'fr.n3': 'One line per quote, with what has been paid so far.',
      'fr.b3': 'Add a budget line',
      'f.committed': 'Committed',
      'f.buffer': 'With buffer',
      'f.paid': 'Paid',
      'f.outstanding': 'Outstanding',
      'f.headroom': 'Headroom',
      'aria.money': 'Money at a glance',
      'aria.totals': 'Budget totals',
      'pr.title': 'Progress',
      'pr.ceiling': 'Committed against ceiling',
      'pr.paid': 'Paid against committed',
      'pr.tasks': 'Tasks done',
      'pr.tasksub': '{a} of {b} done',
      'pr.noceiling': 'No ceiling set — set one in the Budget tab.',
      'pr.of': '{a} of {b}',
      'pr.ceilsub': '{a} of {b}, leaving {c} after the buffer',
      'pr.quoted': '+{a}% on quoted',
      'pr.ofcommitted': '{a}% of committed',
      'w.title': 'Watch list',
      'w.note': 'things to keep an eye on',
      'w.empty': 'Nothing flagged yet.',
      'w.add': 'Add note',
      'w.ph': 'Something to keep an eye on…',
      'w.del': 'Remove note',
      'un.title': 'Up next',
      'un.empty': 'No open tasks — add some in the Tasks tab.',
      'un.untitled': '(untitled task)',
      'b.ceiling': 'Ceiling',
      'b.inflation': 'Inflation buffer (%)',
      'b.rate': 'Exchange rate',
      'b.rate2': 'Exchange rate ({a} per {b})',
      'sp.title': 'Sponsors',
      'sp.note': 'callsigns used in the “cost by” column',
      'sp.add': 'Add sponsor',
      'sp.code': 'Callsign',
      'sp.who': 'Who this is',
      'sp.del': 'Remove sponsor',
      'sp.untitled': 'Untitled',
      'sp.unassigned': 'Unassigned',
      'sp.shared': '(shared)',
      'sp.none': 'No sponsors yet — add one above.',
      'sp.multi': 'Tick more than one for a shared cost.',
      'c.item': 'Item',
      'c.unit': 'Unit',
      'c.qty': 'Qty',
      'c.by': 'Cost by',
      'c.remarks': 'Remarks',
      'c.remove': 'Remove',
      't.totals': 'Totals',
      'b.empty': 'No budget lines yet. Add the first one below.',
      'b.add': 'Add budget line',
      'b.reset': 'Reset column & row sizes',
      'b.del': 'Remove budget line',
      'b.gripcol': 'Drag to resize column',
      'b.griprow': 'Drag to resize row',
      'sl.title': 'Who’s covering what',
      'sl.even': 'Split shared lines evenly between their sponsors',
      'k.task': 'Task',
      'k.owner': 'Owner',
      'k.due': 'Due date',
      'k.status': 'Status',
      'k.all': 'All',
      'k.notstarted': 'Not started',
      'k.inprogress': 'In progress',
      'k.done': 'Done',
      'k.add': 'Add task',
      'k.empty': 'No tasks yet. Add the first one below.',
      'k.nofilter': 'No tasks with that status.',
      'k.del': 'Remove task',
      'aria.filter': 'Filter tasks by status',
      'd.export': 'Export data (JSON)',
      'd.import': 'Import data…',
      'd.exported': 'Exported {a}',
      'd.exportfail': 'Could not export automatically — copy the JSON from the console instead.',
      'd.imported': 'Imported {a}',
      'd.notplanner': 'That file does not look like planner data.',
      'd.unreadable': 'Could not read that file.',
      'd.confirm': 'Replace everything currently in this planner with the imported data?',
      'd.merged': 'Someone else was editing the same line. Both sets of changes have been kept.',
      'd.overtaken': 'Someone else had just changed the line you removed. It is gone.',
      'd.gone': 'Someone else removed the line you were editing. Your copy is still here, but only in this browser.',
      'd.offline': 'Your changes are not reaching the server. Still trying — they are safe in this browser meanwhile.',
      'd.online': 'Back in touch with the server. Everything is saved.',
      'd.refused': 'The server would not accept one of your changes. It is still here, but only in this browser — export the planner if it matters.',
      'ar.closed': 'This event has passed. The planner is closed, and the figures below are the final reckoning.',
      'ar.reopen': 'Reopen for editing',
      'ar.open': 'Reopened for editing. Close it again once everything is settled.',
      'ar.close': 'Close the planner',
      'ar.settle': 'The final reckoning',
      'ar.settlenote': 'what each person covered',
      'ar.nothing': 'Nothing was recorded.'
    },
    nl: {
      'nav.sections': 'Onderdelen van de planner',
      'tab.overview': 'Overzicht',
      'tab.budget': 'Budget',
      'tab.tasks': 'Taken',
      'cd.togo': 'dagen te gaan',
      'cd.togo1': 'dag te gaan',
      'cd.none': 'geen datum ingesteld',
      'cd.passed': 'de dag is geweest',
      'cd.closed': 'het kasboek is gesloten',
      'fr.title': 'Nog niets in het kasboek',
      'fr.body': 'Deze planner houdt het geld en de klussen van één feest bij elkaar: wat alles kost, wie het betaalt, wat er al betaald is en wat er nog openstaat. Alles blijft in deze browser totdat je het exporteert.',
      'fr.s1': 'Stel het plafond in',
      'fr.n1': 'Het bedrag waar het totaal onder moet blijven.',
      'fr.s2': 'Noteer wie meebetaalt',
      'fr.n2': 'Korte roepnamen om elke post aan te hangen.',
      'fr.b2': 'Bijdrager toevoegen',
      'fr.s3': 'Voer de eerste post in',
      'fr.n3': 'Eén regel per offerte, met wat er tot nu toe betaald is.',
      'fr.b3': 'Post toevoegen',
      'f.committed': 'Vastgelegd',
      'f.buffer': 'Met buffer',
      'f.paid': 'Betaald',
      'f.outstanding': 'Openstaand',
      'f.headroom': 'Ruimte over',
      'aria.money': 'Het geld in één oogopslag',
      'aria.totals': 'Budgettotalen',
      'pr.title': 'Voortgang',
      'pr.ceiling': 'Vastgelegd ten opzichte van het plafond',
      'pr.paid': 'Betaald ten opzichte van vastgelegd',
      'pr.tasks': 'Taken afgerond',
      'pr.tasksub': '{a} van {b} afgerond',
      'pr.noceiling': 'Geen plafond ingesteld — stel er een in bij Budget.',
      'pr.of': '{a} van {b}',
      'pr.ceilsub': '{a} van {b}, na de buffer blijft {c} over',
      'pr.quoted': '+{a}% op de offerte',
      'pr.ofcommitted': '{a}% van vastgelegd',
      'w.title': 'Aandachtspunten',
      'w.note': 'dingen om in de gaten te houden',
      'w.empty': 'Nog niets aangemerkt.',
      'w.add': 'Notitie toevoegen',
      'w.ph': 'Iets om in de gaten te houden…',
      'w.del': 'Notitie verwijderen',
      'un.title': 'Eerstvolgende taken',
      'un.empty': 'Geen openstaande taken — voeg ze toe bij Taken.',
      'un.untitled': '(taak zonder naam)',
      'b.ceiling': 'Plafond',
      'b.inflation': 'Inflatiebuffer (%)',
      'b.rate': 'Wisselkoers',
      'b.rate2': 'Wisselkoers ({a} per {b})',
      'sp.title': 'Bijdragers',
      'sp.note': 'roepnamen voor de kolom “rekening van”',
      'sp.add': 'Bijdrager toevoegen',
      'sp.code': 'Roepnaam',
      'sp.who': 'Wie dit is',
      'sp.del': 'Bijdrager verwijderen',
      'sp.untitled': 'Naamloos',
      'sp.unassigned': 'Niet toegewezen',
      'sp.shared': '(gedeeld)',
      'sp.none': 'Nog geen bijdragers — voeg er hierboven een toe.',
      'sp.multi': 'Vink er meer dan één aan voor een gedeelde post.',
      'c.item': 'Post',
      'c.unit': 'Stukprijs',
      'c.qty': 'Aantal',
      'c.by': 'Rekening van',
      'c.remarks': 'Opmerkingen',
      'c.remove': 'Verwijderen',
      't.totals': 'Totaal',
      'b.empty': 'Nog geen posten. Voeg hieronder de eerste toe.',
      'b.add': 'Post toevoegen',
      'b.reset': 'Kolom- en rijafmetingen herstellen',
      'b.del': 'Post verwijderen',
      'b.gripcol': 'Sleep om de kolom breder te maken',
      'b.griprow': 'Sleep om de rij hoger te maken',
      'sl.title': 'Wie betaalt wat',
      'sl.even': 'Gedeelde posten gelijk verdelen over de bijdragers',
      'k.task': 'Taak',
      'k.owner': 'Wie',
      'k.due': 'Deadline',
      'k.status': 'Status',
      'k.all': 'Alles',
      'k.notstarted': 'Nog niet begonnen',
      'k.inprogress': 'Bezig',
      'k.done': 'Klaar',
      'k.add': 'Taak toevoegen',
      'k.empty': 'Nog geen taken. Voeg hieronder de eerste toe.',
      'k.nofilter': 'Geen taken met die status.',
      'k.del': 'Taak verwijderen',
      'aria.filter': 'Taken filteren op status',
      'd.export': 'Gegevens exporteren (JSON)',
      'd.import': 'Gegevens importeren…',
      'd.exported': '{a} geëxporteerd',
      'd.exportfail': 'Automatisch exporteren lukte niet — kopieer de JSON uit de console.',
      'd.imported': '{a} geïmporteerd',
      'd.notplanner': 'Dat bestand lijkt geen plannergegevens te bevatten.',
      'd.unreadable': 'Dat bestand kon niet gelezen worden.',
      'd.confirm': 'Alles wat nu in deze planner staat vervangen door de geïmporteerde gegevens?',
      'd.merged': 'Iemand anders bewerkte dezelfde regel. Beide wijzigingen zijn bewaard.',
      'd.overtaken': 'Iemand anders had de regel die je verwijderde net gewijzigd. Hij is nu weg.',
      'd.gone': 'Iemand anders heeft de regel die jij aan het bewerken was verwijderd. Jouw versie staat er nog, maar alleen in deze browser.',
      'd.offline': 'Je wijzigingen bereiken de server niet. Er wordt opnieuw geprobeerd — ondertussen staan ze veilig in deze browser.',
      'd.online': 'Weer verbinding met de server. Alles is opgeslagen.',
      'd.refused': 'De server accepteerde een van je wijzigingen niet. Hij staat er nog wel, maar alleen in deze browser — exporteer de planner als het belangrijk is.',
      'ar.closed': 'Dit feest is geweest. De planner is gesloten; de cijfers hieronder zijn de eindafrekening.',
      'ar.reopen': 'Heropenen om te bewerken',
      'ar.open': 'Weer opengesteld. Sluit de planner zodra alles is afgerekend.',
      'ar.close': 'Planner sluiten',
      'ar.settle': 'De eindafrekening',
      'ar.settlenote': 'wat ieder heeft betaald',
      'ar.nothing': 'Er is niets vastgelegd.'
    },
    id: {
      'nav.sections': 'Bagian perencana',
      'tab.overview': 'Ringkasan',
      'tab.budget': 'Anggaran',
      'tab.tasks': 'Tugas',
      'cd.togo': 'hari lagi',
      'cd.togo1': 'hari lagi',
      'cd.none': 'tanggal belum diatur',
      'cd.passed': 'harinya sudah lewat',
      'cd.closed': 'buku kas sudah ditutup',
      'fr.title': 'Buku kas masih kosong',
      'fr.body': 'Perencana ini menyatukan uang dan pekerjaan untuk satu acara: berapa biaya setiap hal, siapa yang menanggungnya, apa yang sudah dibayar dan apa yang masih tersisa. Semuanya tersimpan di browser ini sampai kamu mengekspornya.',
      'fr.s1': 'Tentukan batas anggaran',
      'fr.n1': 'Angka yang tidak boleh dilewati oleh total.',
      'fr.s2': 'Catat siapa yang ikut membayar',
      'fr.n2': 'Nama panggilan singkat untuk menandai setiap biaya.',
      'fr.b2': 'Tambah penyumbang',
      'fr.s3': 'Masukkan biaya pertama',
      'fr.n3': 'Satu baris per penawaran, dengan jumlah yang sudah dibayar.',
      'fr.b3': 'Tambah baris anggaran',
      'f.committed': 'Total biaya',
      'f.buffer': 'Dengan cadangan',
      'f.paid': 'Dibayar',
      'f.outstanding': 'Sisa bayar',
      'f.headroom': 'Sisa anggaran',
      'aria.money': 'Ringkasan uang',
      'aria.totals': 'Total anggaran',
      'pr.title': 'Kemajuan',
      'pr.ceiling': 'Total biaya terhadap batas anggaran',
      'pr.paid': 'Dibayar terhadap total biaya',
      'pr.tasks': 'Tugas selesai',
      'pr.tasksub': '{a} dari {b} selesai',
      'pr.noceiling': 'Batas anggaran belum diatur — atur di tab Anggaran.',
      'pr.of': '{a} dari {b}',
      'pr.ceilsub': '{a} dari {b}, tersisa {c} setelah cadangan',
      'pr.quoted': '+{a}% dari penawaran',
      'pr.ofcommitted': '{a}% dari total biaya',
      'w.title': 'Perlu diperhatikan',
      'w.note': 'hal yang perlu diawasi',
      'w.empty': 'Belum ada catatan.',
      'w.add': 'Tambah catatan',
      'w.ph': 'Sesuatu yang perlu diawasi…',
      'w.del': 'Hapus catatan',
      'un.title': 'Berikutnya',
      'un.empty': 'Tidak ada tugas terbuka — tambahkan di tab Tugas.',
      'un.untitled': '(tugas tanpa nama)',
      'b.ceiling': 'Batas anggaran',
      'b.inflation': 'Cadangan inflasi (%)',
      'b.rate': 'Kurs',
      'b.rate2': 'Kurs ({a} per {b})',
      'sp.title': 'Penyumbang',
      'sp.note': 'nama panggilan untuk kolom “ditanggung”',
      'sp.add': 'Tambah penyumbang',
      'sp.code': 'Nama panggilan',
      'sp.who': 'Siapa ini',
      'sp.del': 'Hapus penyumbang',
      'sp.untitled': 'Tanpa nama',
      'sp.unassigned': 'Belum ditentukan',
      'sp.shared': '(patungan)',
      'sp.none': 'Belum ada penyumbang — tambahkan di atas.',
      'sp.multi': 'Centang lebih dari satu untuk biaya patungan.',
      'c.unit': 'Harga satuan',
      'c.qty': 'Jumlah',
      'c.by': 'Ditanggung',
      'c.remarks': 'Catatan',
      'c.remove': 'Hapus',
      't.totals': 'Total',
      'b.empty': 'Belum ada baris anggaran. Tambahkan yang pertama di bawah.',
      'b.add': 'Tambah baris anggaran',
      'b.reset': 'Atur ulang ukuran kolom & baris',
      'b.del': 'Hapus baris anggaran',
      'b.gripcol': 'Seret untuk mengubah lebar kolom',
      'b.griprow': 'Seret untuk mengubah tinggi baris',
      'sl.title': 'Siapa menanggung apa',
      'sl.even': 'Bagi rata biaya patungan di antara penyumbangnya',
      'k.task': 'Tugas',
      'k.owner': 'Siapa',
      'k.due': 'Tenggat',
      'k.status': 'Status',
      'k.all': 'Semua',
      'k.notstarted': 'Belum mulai',
      'k.inprogress': 'Sedang dikerjakan',
      'k.done': 'Selesai',
      'k.add': 'Tambah tugas',
      'k.empty': 'Belum ada tugas. Tambahkan yang pertama di bawah.',
      'k.nofilter': 'Tidak ada tugas dengan status itu.',
      'k.del': 'Hapus tugas',
      'aria.filter': 'Saring tugas menurut status',
      'd.export': 'Ekspor data (JSON)',
      'd.import': 'Impor data…',
      'd.exported': '{a} diekspor',
      'd.exportfail': 'Ekspor otomatis gagal — salin JSON dari konsol.',
      'd.imported': '{a} diimpor',
      'd.notplanner': 'Berkas itu sepertinya bukan data perencana.',
      'd.unreadable': 'Berkas itu tidak bisa dibaca.',
      'd.confirm': 'Ganti semua isi perencana ini dengan data yang diimpor?',
      'd.merged': 'Orang lain sedang mengubah baris yang sama. Kedua perubahan tetap tersimpan.',
      'd.overtaken': 'Orang lain baru saja mengubah baris yang kamu hapus. Baris itu sudah hilang.',
      'd.gone': 'Orang lain menghapus baris yang sedang kamu ubah. Salinanmu masih ada, tetapi hanya di browser ini.',
      'd.offline': 'Perubahanmu belum sampai ke server. Masih dicoba lagi — sementara ini aman tersimpan di browser.',
      'd.online': 'Terhubung lagi dengan server. Semuanya tersimpan.',
      'd.refused': 'Server menolak salah satu perubahanmu. Perubahan itu masih ada, tetapi hanya di browser ini — ekspor perencana kalau ini penting.',
      'ar.closed': 'Acara ini sudah lewat. Perencana ditutup dan angka di bawah adalah perhitungan akhir.',
      'ar.reopen': 'Buka lagi untuk diubah',
      'ar.open': 'Dibuka lagi untuk diubah. Tutup lagi setelah semuanya beres.',
      'ar.close': 'Tutup perencana',
      'ar.settle': 'Perhitungan akhir',
      'ar.settlenote': 'berapa yang ditanggung tiap orang',
      'ar.nothing': 'Tidak ada yang tercatat.'
    }
  };

  var LANG = (function () {
    function pick(v) {
      var tag = String(v || '').toLowerCase().replace(/_/g, '-').split('-')[0];
      return LANGS.indexOf(tag) !== -1 ? tag : '';
    }
    var q = '';
    try {
      q = (location.search.match(/[?&]lang=([^&]*)/) || [])[1] || '';
      q = decodeURIComponent(q);
    } catch (e) { q = ''; }
    return pick(q) || pick(CONFIG.language) || pick(CONFIG.locale) || 'en';
  })();

  // Drives screen-reader pronunciation and hyphenation, so it has to be the
  // language the page is actually written in rather than the one the server
  // guessed at template time.
  document.documentElement.lang = LANG;

  function t(key, vars) {
    var s = (STRINGS[LANG] || {})[key];
    if (s === undefined) s = STRINGS.en[key];
    if (s === undefined) return key;
    if (!vars) return s;
    return s.replace(/\{(\w)\}/g, function (m, k) {
      return k in vars ? vars[k] : m;
    });
  }

  /* Static markup carries its own key, so index.html stays readable English
   * and the table stays the single place a translator works. */
  function applyStrings() {
    function each(sel, fn) {
      Array.prototype.forEach.call(document.querySelectorAll(sel), fn);
    }
    each('[data-i18n]', function (el) {
      el.textContent = t(el.getAttribute('data-i18n'));
    });
    each('[data-i18n-aria]', function (el) {
      el.setAttribute('aria-label', t(el.getAttribute('data-i18n-aria')));
    });
    each('[data-i18n-label]', function (el) {
      el.setAttribute('data-label', t(el.getAttribute('data-i18n-label')));
    });
  }

  function uid(prefix) {
    return prefix + Math.random().toString(36).slice(2, 9);
  }

  // Every figure on the page is written through here, so a panel can drop an
  // element without the render path having to know about it.
  function setText(id, value) {
    var el = document.getElementById(id);
    if (el) el.textContent = value;
  }

  // Unit is the widest heading once translated — "Stukprijs (EUR)", "Harga
  // satuan (EUR)" — and a header that ellipses away its currency code is a
  // header that has stopped saying what the column holds. The width comes off
  // remarks, which wraps rather than truncating. Total unchanged.
  var DEFAULT_COL_WIDTHS = [230, 140, 70, 135, 110, 135, 135, 200, 40];

  function emptyState() {
    return {
      ceiling: Number(CONFIG.ceiling) || 0,
      inflationPct: 0,
      fxRate: 0,
      splitEvenly: false,
      // Set once, by hand, from the archive banner. Kept in the planner rather
      // than in a per-browser preference because reopening a settled event is
      // a decision about the event, not about the device looking at it.
      reopened: false,
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
   * MONEY ARITHMETIC
   * ------------------------------------------------------------------
   * The page works in major units as plain numbers, because that is what an
   * <input type="number"> gives back. Arithmetic does not: every sum below
   * accumulates whole minor units as integers and converts once at the end.
   * Adding major-unit floats drifts — 45.33 × 40 is 1813.1999999999998 — and a
   * budget is the one place a cent per line is not acceptable. It also rides
   * into the export, which is the copy people keep.
   *
   * MINOR_UNIT_EXPONENT mirrors internal/store/money.go and must stay
   * mirrored. It is NOT ISO 4217 and it is not what Intl reports: IDR is
   * treated as zero-decimal here, because the sen has not circulated in
   * decades and no Indonesian price carries one. Intl says 2. Deriving the
   * exponent from Intl would therefore disagree with the server on precisely
   * the currency this is deployed with, and every figure would be out by a
   * factor of a hundred the moment it crossed the wire.
   * ------------------------------------------------------------------ */
  var MINOR_UNIT_EXPONENT = {
    IDR: 0, // see above — deliberately not the ISO 4217 value

    // Zero-decimal per ISO 4217.
    BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0,
    KMF: 0, KRW: 0, PYG: 0, RWF: 0, UGX: 0, UYI: 0,
    VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,

    // Three-decimal per ISO 4217.
    BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,

    // Four-decimal per ISO 4217.
    CLF: 4
  };

  var MONEY_EXP = (function () {
    var code = String(CONFIG.currency || 'EUR').trim().toUpperCase();
    // hasOwnProperty rather than a truthiness test: an exponent of 0 is the
    // interesting case, and `||` would send every zero-decimal currency back
    // to the two-decimal default.
    return Object.prototype.hasOwnProperty.call(MINOR_UNIT_EXPONENT, code)
      ? MINOR_UNIT_EXPONENT[code]
      : 2;
  })();
  var MINOR = Math.pow(10, MONEY_EXP);

  function toMinor(n) {
    var v = Number(n);
    if (!isFinite(v)) return 0;
    return Math.round(v * MINOR);
  }
  function toMajor(minor) { return minor / MINOR; }

  /* Money on the wire is a decimal string in *major* units with exactly the
   * currency's number of places — "250.50", "750000". A JSON number is a 400,
   * and so is a string with more places than the currency has.
   *
   * Formatted from the integer rather than with String(n) or toFixed on the
   * float: String(45.33 * 1) is "45.330000000000005", which is rejected, and
   * toFixed would round a value that had already drifted. These digits came
   * out of an integer and go back into one, which is the whole point of
   * counting in minor units in the first place. Mirrors store.FormatMajor. */
  function formatMinor(minor) {
    if (MONEY_EXP === 0) return String(minor);
    var sign = minor < 0 ? '-' : '';
    var mag = Math.abs(minor);
    var frac = String(mag % MINOR);
    while (frac.length < MONEY_EXP) frac = '0' + frac;
    return sign + String(Math.floor(mag / MINOR)) + '.' + frac;
  }

  /* The other direction, and exact for the same reason: the digits go into an
   * integer before anything divides. Mirrors store.ParseMajor, except that a
   * value this cannot read is 0 rather than an error — the server is the one
   * that gets to refuse, and a figure that fails to arrive must not take the
   * page down with it. */
  function readMajor(v) {
    if (typeof v === 'number') return isFinite(v) ? v : 0;
    var str = String(v == null ? '' : v).trim();
    if (!/^[+-]?\d+(\.\d+)?$/.test(str)) return 0;
    var neg = str.charAt(0) === '-';
    if (neg || str.charAt(0) === '+') str = str.slice(1);
    var parts = str.split('.');
    var frac = (parts[1] || '').slice(0, MONEY_EXP);
    while (frac.length < MONEY_EXP) frac += '0';
    var minor = Number(parts[0] + frac);
    return toMajor(neg ? -minor : minor);
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
   * save() is debounced, so the write path is already async-shaped: that is
   * what lets the shared backend below hang off Store.write without touching
   * any of the ~28 call sites that mutate state.
   *
   * Two modes, decided once at startup by asking the origin whether it has a
   * database (see connect()):
   *
   *   no API — localStorage is the planner. One browser, one copy, no network
   *            after the probe. A self-hoster without Postgres, and `docker
   *            run` with no arguments, both land here and both work.
   *   API    — the server is the planner. localStorage stays as the cached
   *            copy that paints before the plan arrives, plus the handful of
   *            fields that have no column behind them.
   *
   * localStorage is written synchronously in both modes and first in both
   * modes. pagehide has no time to wait on a promise, and the whole point of
   * a deferred write is that the round trip is not on the interaction path.
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
      // A no-op until the probe has found an API, so the local-only
      // deployment never touches the network again after it.
      Sync.push();
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
    if (typeof state.reopened !== 'boolean') state.reopened = false;
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

  /* ==================================================================
   * THE SHARED BACKEND
   * ==================================================================
   * Everything from here to "Saving" is the API half of Store. It is inert
   * until connect() finds a database, and it never reshapes `state` — the
   * page renders from the same object either way, which is what keeps the
   * export, the import and every render path mode-agnostic.
   *
   * The model is a shadow: a private copy of every row as the server last
   * confirmed it. A write is the difference between `state` and the shadow,
   * which is how 28 mutation sites that say nothing about what they changed
   * still turn into per-field PATCHes carrying a revision. It also makes the
   * retry free — a write that fails simply does not advance the shadow, so
   * the next pass computes the same difference again.
   * ================================================================== */

  var API_BASE = '/api/v1';

  var apiMode = false;     // the origin answered /plan: there is a database
  var dirty = false;       // something was edited before the plan arrived
  var shadow = null;       // rows as the server last confirmed them
  var idMap = {};          // this browser's optimistic ids -> the server's uuids
  var blocked = {};        // writes the server refused, parked until they change

  // Backoff for a write that got no answer. Doubling from a second, capped,
  // because the common cause is a tunnel or a train and neither is helped by
  // hammering.
  var RETRY_BASE_MS = 1000;
  var RETRY_MAX_MS = 30000;
  // One failed write is a blip. Two in a row is worth telling somebody about,
  // because from here on their edits exist in one browser only.
  var FAILURES_BEFORE_NOTICE = 2;
  // A ceiling on reconcile-and-retry rounds. The three-way merge below
  // terminates on its own; this is only here so that a server disagreeing
  // with us in a way we did not anticipate stops rather than spins.
  var MAX_PASSES = 8;

  /* ---------- Fields ----------
   * Each field knows three things: how to compare it (canon), how to put it on
   * the wire, and how to read one off it. Comparison is on the canonical form
   * rather than the raw value so that 45.3 and "45.30" are the same money and
   * two sponsor lists in different orders are the same attribution.
   */
  function textField(name) {
    return {
      name: name,
      canon: function (r) { return String(r[name] == null ? '' : r[name]); },
      wire: function (r) { return String(r[name] == null ? '' : r[name]); },
      read: function (v) { return v == null ? '' : String(v); },
      copy: function (v) { return v; }
    };
  }
  function moneyField(name) {
    return {
      name: name,
      canon: function (r) { return toMinor(r[name]); },
      wire: function (r) { return formatMinor(toMinor(r[name])); },
      read: function (v) { return readMajor(v); },
      copy: function (v) { return v; }
    };
  }
  function numberField(name) {
    return {
      name: name,
      canon: function (r) { return Number(r[name]) || 0; },
      wire: function (r) { return Number(r[name]) || 0; },
      read: function (v) { return Number(v) || 0; },
      copy: function (v) { return v; }
    };
  }
  function boolField(name) {
    return {
      name: name,
      canon: function (r) { return !!r[name]; },
      wire: function (r) { return !!r[name]; },
      read: function (v) { return !!v; },
      copy: function (v) { return v; }
    };
  }
  // A plain calendar date, never an instant: the column is a `date` and the
  // API wants "2030-01-15". An empty field goes as null rather than "", which
  // is not a date and is a 400.
  function dateField(name) {
    return {
      name: name,
      canon: function (r) { return r[name] || ''; },
      wire: function (r) { return r[name] || null; },
      read: function (v) { return v == null ? '' : String(v); },
      copy: function (v) { return v; }
    };
  }
  // A list of ids, order-insensitive, read through the optimistic-id map so a
  // row still holding a local sponsor id compares equal to the shadow entry
  // that already holds the server's uuid for it.
  function idsField(name) {
    return {
      name: name,
      canon: function (r) { return (r[name] || []).map(mapId).slice().sort().join(','); },
      wire: function (r) { return (r[name] || []).map(mapId); },
      read: function (v) { return Array.isArray(v) ? v.slice() : []; },
      copy: function (v) { return (v || []).slice(); }
    };
  }

  function mapId(id) {
    return Object.prototype.hasOwnProperty.call(idMap, id) ? idMap[id] : id;
  }

  /* ---------- Collections ----------
   * The order matters and is the send order: a budget line tagged with a
   * sponsor that was added in the same debounce window has to reach a server
   * that already knows that sponsor, or the attribution is a foreign key
   * violation and the whole line is a 400.
   *
   * `phases` and `programme` are in the plan and are not here. This page has
   * no interface for either, and a client must not delete rows it cannot draw.
   */
  var COLLECTIONS = [
    {
      key: 'sponsors', route: 'sponsors', container: 'sponsorGrid',
      fields: [textField('code'), textField('name')],
      render: function () { renderSponsors(); renderBudgetTable(); renderSplit(); }
    },
    {
      key: 'budgetItems', route: 'budget-items', container: 'budgetBody',
      fields: [
        textField('item'), moneyField('unit'), numberField('qty'),
        moneyField('paid'), textField('note'), idsField('sponsors')
      ],
      render: function () { renderBudgetTable(); renderBudgetTotals(); renderOverview(); }
    },
    {
      key: 'tasks', route: 'tasks', container: 'tasksBody',
      fields: [textField('name'), textField('owner'), dateField('due'), textField('status')],
      render: function () { renderTasksTable(); renderOverview(); }
    },
    {
      key: 'notes', route: 'notes', container: 'watchList',
      fields: [textField('text')],
      render: function () { renderNotes(); }
    }
  ];

  /* The plan-wide knobs. A singleton: one row behind a boolean primary key,
   * so there is no id in its URL, nothing to POST and nothing to DELETE. The
   * fields sit at the top of `state` rather than in a row of their own, which
   * is why this descriptor is not in the list above — everything else about
   * it, revision included, is the same. */
  var SETTINGS = {
    key: 'settings', route: 'settings', singleton: true, container: 'panel-budget',
    fields: [
      moneyField('ceiling'), numberField('inflationPct'),
      numberField('fxRate'), boolField('splitEvenly')
    ],
    render: function () { renderSettingsInputs(); renderBudgetTotals(); renderOverview(); }
  };

  function fieldNamed(coll, name) {
    for (var i = 0; i < coll.fields.length; i++) {
      if (coll.fields[i].name === name) return coll.fields[i];
    }
    return null;
  }

  function findRow(list, id) {
    for (var i = 0; i < (list || []).length; i++) {
      if (list[i] && list[i].id === id) return list[i];
    }
    return null;
  }

  /* ---------- Wire <-> page ---------- */

  function rowFromWire(coll, w) {
    var row = {};
    if (w && w.id) row.id = w.id;
    if (w && typeof w.position === 'number') row.position = w.position;
    coll.fields.forEach(function (f) { row[f.name] = f.read(w ? w[f.name] : null); });
    return row;
  }

  function cloneRow(coll, row) {
    var out = { id: row.id, position: row.position };
    coll.fields.forEach(function (f) { out[f.name] = f.copy(row[f.name]); });
    return out;
  }

  function stateFromPlan(plan) {
    var s = emptyState();
    var wire = plan.settings || {};
    SETTINGS.fields.forEach(function (f) { s[f.name] = f.read(wire[f.name]); });
    COLLECTIONS.forEach(function (c) {
      s[c.key] = (plan[c.key] || []).map(function (w) { return rowFromWire(c, w); });
    });
    return s;
  }

  function shadowFromPlan(plan) {
    var wire = plan.settings || {};
    var sh = { settings: { row: rowFromWire(SETTINGS, wire), revision: Number(wire.revision) || 0 } };
    COLLECTIONS.forEach(function (c) {
      var byId = {};
      (plan[c.key] || []).forEach(function (w) {
        if (!w || !w.id) return;
        byId[w.id] = { row: rowFromWire(c, w), revision: Number(w.revision) || 0 };
      });
      sh[c.key] = byId;
    });
    return sh;
  }

  // The server's answer to a write is the row as it now stands, so it is also
  // the new agreed version. Stored as its own object: a shadow that shared
  // references with `state` would diff to nothing forever, because every edit
  // site mutates its row in place.
  function shadowPut(coll, wireRow) {
    var entry = { row: rowFromWire(coll, wireRow), revision: Number(wireRow.revision) || 0 };
    if (coll.singleton) shadow.settings = entry;
    else shadow[coll.key][wireRow.id] = entry;
  }

  function shadowEntry(coll, id) {
    return coll.singleton ? shadow.settings : shadow[coll.key][id];
  }

  function liveRow(coll, id) {
    return coll.singleton ? state : findRow(state[coll.key], id);
  }

  /* ---------- The difference ---------- */

  function changedFields(coll, row, base) {
    var out = [];
    coll.fields.forEach(function (f) {
      if (f.canon(row) !== f.canon(base)) out.push(f.name);
    });
    return out;
  }

  function opKey(op) { return op.coll.key + ':' + op.id + ':' + op.kind; }

  // What a refused write looked like, so that repeating it is recognisable and
  // changing it afterwards is too.
  function opSignature(op) {
    if (op.kind === 'delete') return 'delete';
    var row = liveRow(op.coll, op.id);
    if (!row) return 'gone';
    return op.coll.fields.map(function (f) { return String(f.canon(row)); }).join('');
  }

  function planOps() {
    var creates = [], updates = [], deletes = [];

    COLLECTIONS.forEach(function (c) {
      var present = {};
      (state[c.key] || []).forEach(function (row) {
        if (!row || !row.id) return;
        present[row.id] = true;
        var known = shadow[c.key][row.id];
        if (!known) {
          creates.push({ kind: 'create', coll: c, id: row.id });
          return;
        }
        var changed = changedFields(c, row, known.row);
        if (changed.length) updates.push({ kind: 'update', coll: c, id: row.id, fields: changed });
      });
      // A row the shadow has and the page does not was removed here. There is
      // no third possibility: adopt() guarantees the page starts holding every
      // row the server had, so "absent" can only mean "deleted", never "not
      // fetched yet".
      Object.keys(shadow[c.key]).forEach(function (id) {
        if (!present[id]) deletes.push({ kind: 'delete', coll: c, id: id });
      });
    });

    var settings = changedFields(SETTINGS, state, shadow.settings.row);
    if (settings.length) {
      updates.push({ kind: 'update', coll: SETTINGS, id: null, fields: settings });
    }

    return creates.concat(updates).concat(deletes).filter(function (op) {
      return blocked[opKey(op)] !== opSignature(op);
    });
  }

  // POST does not allocate one, and an omitted position is 0 — which puts
  // every new row at the top of the list, in the order they were typed.
  function nextPosition(coll) {
    var max = -1;
    Object.keys(shadow[coll.key]).forEach(function (id) {
      var p = shadow[coll.key][id].row.position;
      if (typeof p === 'number' && p > max) max = p;
    });
    return max + 1;
  }

  /* ---------- Requests ----------
   * Never rejects: a refusal and an unreachable origin are both answers this
   * has to act on, and only one of them is worth retrying. status 0 is "no
   * answer at all".
   */
  function api(method, path, body) {
    var init = {
      method: method,
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      // Only while the page is going away, where a normal request is killed
      // with the document and the last edit before a tab closes never lands.
      // Read per request rather than latched, so a page that comes back does
      // not keep spending the keepalive budget.
      keepalive: document.visibilityState === 'hidden'
    };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    return fetch(API_BASE + path, init).then(function (res) {
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

  /* ---------- Writes ---------- */

  function sendCreate(op) {
    var c = op.coll;
    var row = liveRow(c, op.id);
    // Added and removed again inside one debounce window: it never existed on
    // the server, so there is nothing to create and nothing to delete either.
    if (!row) return Promise.resolve(true);

    var body = {};
    c.fields.forEach(function (f) { body[f.name] = f.wire(row); });
    body.position = nextPosition(c);

    return api('POST', '/' + c.route, body).then(function (res) {
      if (res.status === 201 && res.body && res.body.id) {
        adoptServerId(c, op.id, res.body.id);
        shadowPut(c, res.body);
        return true;
      }
      return writeFailed(op, res);
    });
  }

  function sendUpdate(op) {
    var c = op.coll;
    var row = liveRow(c, op.id);
    var known = shadowEntry(c, op.id);
    if (!row || !known) return Promise.resolve(true);

    // Only the fields that actually differ, plus the revision they were read
    // at. Sending the whole row would overwrite every column somebody else
    // touched, which is the behaviour the revision exists to prevent.
    var body = { revision: known.revision };
    op.fields.forEach(function (name) {
      var f = fieldNamed(c, name);
      if (f) body[name] = f.wire(row);
    });

    var path = c.singleton ? '/' + c.route : '/' + c.route + '/' + op.id;
    return api('PATCH', path, body).then(function (res) {
      if (res.status === 200 && res.body) { shadowPut(c, res.body); return true; }
      return writeFailed(op, res);
    });
  }

  function sendDelete(op) {
    var c = op.coll;
    var known = shadowEntry(c, op.id);
    if (!known) return Promise.resolve(true);

    // The revision travels as a query parameter here rather than in a body,
    // which is the API's choice and not a detail worth papering over.
    return api('DELETE', '/' + c.route + '/' + op.id + '?revision=' + known.revision).then(function (res) {
      // Already gone is the outcome that was asked for.
      if (res.status === 204 || res.status === 404) {
        delete shadow[c.key][op.id];
        return true;
      }
      if (res.status === 409 && res.body && res.body.current) {
        // Somebody edited the row this browser is removing. The removal is
        // still what its user asked for, so it goes again against the revision
        // that now stands — but they are told, because the edit that got
        // overtaken was not theirs. The retry is the next pass, not a
        // recursive call, so a row being edited continuously cannot spin here.
        known.revision = Number(res.body.current.revision) || known.revision;
        known.row = rowFromWire(c, res.body.current);
        flash(t('d.overtaken'));
        return true;
      }
      return writeFailed(op, res);
    });
  }

  function sendOp(op) {
    if (op.kind === 'create') return sendCreate(op);
    if (op.kind === 'update') return sendUpdate(op);
    return sendDelete(op);
  }

  // false stops the pass and books a retry; true carries on.
  function writeFailed(op, res) {
    if (res.status === 409 && res.body && res.body.current) {
      reconcile(op, res.body.current);
      return true;
    }
    if (res.status >= 400 && res.status < 500) {
      // The server understood and said no. Sending the identical body again
      // would only produce the identical refusal, so this row is parked until
      // it changes. The edit is not lost — it is in `state`, on the screen and
      // in localStorage — and the person is told it has not left the browser.
      //
      // The row is deliberately not removed on a 404. Somebody else deleted
      // what this person is editing, and throwing their work away to agree
      // with that is the one outcome worse than the row going out of step.
      blocked[opKey(op)] = opSignature(op);
      flash(t(res.status === 404 ? 'd.gone' : 'd.refused'));
      return true;
    }
    return false;
  }

  /* ---------- Conflicts ----------
   * A 409 means somebody else wrote this row between the revision we read and
   * the write we sent. Reconciling is a three-way merge against the shadow,
   * which is the version *both* edits started from:
   *
   *   field unchanged here  -> theirs is simply newer. Take it, on screen too.
   *   field changed here    -> keep ours. It goes again on the next pass.
   *
   * Either way the shadow becomes their row, so the retry carries only the
   * fields this person actually changed and the merge terminates — when there
   * is nothing left that differs, the next pass produces no write at all.
   *
   * What this must never do is what the obvious version does: set the shadow
   * to `current` and re-diff. `state` still holds the *old* value of the field
   * they changed, so the re-diff would send it back and quietly undo them.
   */
  function reconcile(op, current) {
    var c = op.coll;
    var row = liveRow(c, op.id);
    var known = shadowEntry(c, op.id);
    if (!row || !known) return;

    var base = known.row;
    var theirs = rowFromWire(c, current);
    var tookTheirs = false;

    c.fields.forEach(function (f) {
      var mine = f.canon(row);
      if (mine !== f.canon(base)) return;       // edited here: ours wins, and goes again
      if (f.canon(theirs) === mine) return;     // nobody changed it
      row[f.name] = f.copy(theirs[f.name]);
      tookTheirs = true;
    });

    known.row = theirs;
    known.revision = Number(current.revision) || known.revision;

    flash(t('d.merged'));
    if (tookTheirs) scheduleRefresh(c);
  }

  /* ---------- Optimistic ids ----------
   * The page makes an id the moment a row appears, because the row has to be
   * addressable before any round trip could have answered. The server makes a
   * uuid on POST. Reconciling the two is a rename, everywhere the old one was
   * referred to — otherwise a budget line keeps pointing at a sponsor id that
   * only ever existed in this browser.
   */
  function adoptServerId(coll, localId, serverId) {
    if (!serverId || localId === serverId) return;
    idMap[localId] = serverId;

    var row = findRow(state[coll.key], localId);
    if (row) row.id = serverId;

    if (coll.key === 'budgetItems' && state.rowHeights &&
        Object.prototype.hasOwnProperty.call(state.rowHeights, localId)) {
      state.rowHeights[serverId] = state.rowHeights[localId];
      delete state.rowHeights[localId];
    }
    if (coll.key === 'sponsors') {
      state.budgetItems.forEach(function (i) {
        i.sponsors = (i.sponsors || []).map(mapId);
      });
    }
  }

  /* ---------- Re-rendering from the network ----------
   * Rebuilding a table takes the caret with it. A reconcile that fires while
   * somebody is mid-word would move their cursor and detach the element they
   * are typing into, so a refresh of the table they are inside waits until
   * they are not.
   */
  var pendingRefresh = {};

  function scheduleRefresh(coll) {
    pendingRefresh[coll.key] = coll;
    applyRefresh();
  }

  function applyRefresh() {
    Object.keys(pendingRefresh).forEach(function (k) {
      var coll = pendingRefresh[k];
      var host = document.getElementById(coll.container);
      if (host && document.activeElement && host.contains(document.activeElement)) return;
      delete pendingRefresh[k];
      coll.render();
    });
  }

  // After the blur has settled, so activeElement is the element being moved to
  // rather than the one being left.
  document.addEventListener('focusout', function () { setTimeout(applyRefresh, 0); });

  /* ---------- The sync loop ----------
   * One pass at a time, strictly. That is what stops a row whose POST is in
   * flight from being posted a second time: the shadow only gains the row when
   * the 201 lands, and nothing else is computing a difference in the meantime.
   */
  var Sync = { queued: false, running: false, failures: 0, timer: null };

  Sync.push = function () {
    if (!apiMode) return;
    Sync.queued = true;
    Sync.run();
  };

  Sync.run = function () {
    if (!apiMode || Sync.running || Sync.timer || !Sync.queued) return;
    Sync.queued = false;
    Sync.running = true;
    drain(0).then(Sync.done, function () { Sync.done(false); });
  };

  Sync.done = function (ok) {
    Sync.running = false;
    if (ok) {
      if (Sync.failures >= FAILURES_BEFORE_NOTICE) flash(t('d.online'));
      Sync.failures = 0;
      setSticky('');
      if (Sync.queued) Sync.run();
      return;
    }
    Sync.failures++;
    if (Sync.failures >= FAILURES_BEFORE_NOTICE) setSticky(t('d.offline'));
    // Nothing was thrown away: the shadow did not advance, so the same
    // difference is still there to be sent when the wait is over.
    Sync.queued = true;
    Sync.timer = setTimeout(function () {
      Sync.timer = null;
      Sync.run();
    }, Math.min(RETRY_MAX_MS, RETRY_BASE_MS * Math.pow(2, Sync.failures - 1)));
  };

  // One pass, then look again: a reconcile changes what there is to send, and
  // a delete that lost a race has a new revision to try.
  function drain(pass) {
    var ops = planOps();
    if (!ops.length || pass >= MAX_PASSES) return Promise.resolve(true);
    return runOps(ops, 0).then(function (ok) {
      return ok ? drain(pass + 1) : false;
    });
  }

  function runOps(ops, i) {
    if (i >= ops.length) return Promise.resolve(true);
    return sendOp(ops[i]).then(function (ok) {
      return ok ? runOps(ops, i + 1) : false;
    });
  }

  /* ---------- Finding the backend ----------
   * One request, once. A 404 is the answer for a deployment with no database
   * and it is final: those paths are never registered, so asking again would
   * only be a second 404. Anything else that is not an answer might be this
   * browser being offline on a first visit, which is worth a few more tries.
   */
  function connect(attempt) {
    if (typeof fetch !== 'function' || typeof Promise !== 'function') return;
    api('GET', '/plan').then(function (res) {
      if (res.status === 200 && res.body) { adopt(res.body); return; }
      if (res.status === 404) return;
      if (attempt < 4) {
        setTimeout(function () { connect(attempt + 1); },
          Math.min(RETRY_MAX_MS, RETRY_BASE_MS * Math.pow(2, attempt)));
      }
    });
  }

  function adopt(plan) {
    shadow = shadowFromPlan(plan);
    apiMode = true;

    if (dirty) {
      // Something was typed between the cached copy painting and the plan
      // arriving. Those edits are kept and pushed up, and the rows the server
      // has that this browser has not are taken rather than deleted — in that
      // window "absent here" means "not fetched yet", not "removed", and this
      // is the union that makes the delete rule in planOps true.
      COLLECTIONS.forEach(function (c) {
        var present = {};
        (state[c.key] || []).forEach(function (r) { if (r && r.id) present[r.id] = true; });
        Object.keys(shadow[c.key]).forEach(function (id) {
          if (!present[id]) state[c.key].push(cloneRow(c, shadow[c.key][id].row));
        });
      });
    } else {
      var next = stateFromPlan(plan);
      // Per-browser preferences and the one flag with no column behind it.
      // Column widths are a view of the table, not a fact about the event: one
      // person dragging a column must not resize it for everybody.
      next.colWidths = state.colWidths;
      next.rowHeights = state.rowHeights;
      next.reopened = state.reopened;
      state = next;
    }

    renderAll();
    Sync.push();
  }

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
    // Noted before the write, not after: connect() needs to know whether this
    // browser has edits of its own before it decides whether the plan it just
    // fetched can simply replace what is on screen.
    dirty = true;
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

  // Whole minor units. Qty is not money and can carry three decimals, so the
  // product is rounded back to a whole minor unit here rather than being
  // carried as a fraction into every sum downstream.
  function lineTotalMinor(i) {
    return Math.round(toMinor(i.unit) * (Number(i.qty) || 0));
  }
  function lineTotal(i) { return toMajor(lineTotalMinor(i)); }

  function totals() {
    var t = 0, p = 0;
    state.budgetItems.forEach(function (i) {
      t += lineTotalMinor(i);
      p += toMinor(i.paid);
    });
    var buffer = Math.round(t * (1 + (Number(state.inflationPct) || 0) / 100));
    return {
      total: toMajor(t), paid: toMajor(p), owing: toMajor(t - p),
      forecast: toMajor(buffer), ceiling: Number(state.ceiling) || 0,
      // The same figure in minor units, for the callers that go on to divide
      // it between people and would otherwise start their arithmetic from a
      // float.
      totalMinor: t
    };
  }

  /* ---------- Static labels driven by config ---------- */
  (function applyConfigLabels() {
    Array.prototype.forEach.call(document.querySelectorAll('.cur-code'), function (el) {
      el.textContent = CUR;
    });
    var rateField = document.getElementById('rateField');
    // Set here rather than from a data-i18n key on the element: this label is
    // the one that composes a translated string with two configured currency
    // codes, and applyStrings() would flatten it back to the bare phrase.
    var lbl = document.getElementById('rateLabel');
    if (lbl) lbl.textContent = SECONDARY ? t('b.rate2', { a: CUR, b: SECONDARY }) : t('b.rate');
    if (!SECONDARY && rateField) rateField.style.display = 'none';
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

  /* ---------- Countdown ----------
   * daysLeft() is also the whole definition of "after the event": one place
   * decides what day it is, so the countdown and the archive can never
   * disagree about whether the evening has happened.
   */
  function daysLeft() {
    if (!EVENT_DATE) return null;
    // Whole UTC days between two calendar dates, not hours between two
    // instants. Two things go wrong if this measures instants:
    //
    //   - from *local* midnight, the same planner reads a different number of
    //     days to go on either side of UTC, which is the thing the parsing
    //     comment above sets out to avoid;
    //   - against an event date that carries a time — 19:00 for a dinner, the
    //     realistic configuration here — any rounding is a day out on one side
    //     or the other. On the evening's own date it would read "1 day to go",
    //     and the day after it would still read "0".
    //
    // Both matter more now that this figure also decides when the ledger
    // closes: getting it wrong leaves the planner open for a day after the
    // event, or shuts it on the morning of.
    var now = new Date();
    var today = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
    var day = Date.UTC(EVENT_DATE.getUTCFullYear(), EVENT_DATE.getUTCMonth(), EVENT_DATE.getUTCDate());
    return Math.round((day - today) / 86400000);
  }

  // The day itself is not "after": a planner is at its most useful on the
  // morning of, so the ledger closes the day after the date, not on it.
  function isPast() {
    var d = daysLeft();
    return d !== null && d < 0;
  }
  function isArchived() { return isPast() && !state.reopened; }

  function renderCountdown() {
    var diffDays = daysLeft();
    if (diffDays === null) {
      setText('daysNum', '–');
      setText('daysLabel', t('cd.none'));
      setText('statDaysLabel', '');
      return;
    }
    setText('daysNum', diffDays >= 0 ? diffDays : 0);
    setText('daysLabel', diffDays < 0
      ? (state.reopened ? t('cd.passed') : t('cd.closed'))
      : (diffDays === 1 ? t('cd.togo1') : t('cd.togo')));
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
    var done = state.tasks.filter(function (k) { return k.status === 'done'; }).length;
    var taskPct = total ? Math.round((done / total) * 100) : 0;
    setText('taskBarPct', taskPct + '%');
    setText('statTasks', t('pr.tasksub', { a: done, b: total }));
    document.getElementById('taskBarFill').style.width = taskPct + '%';

    var m = totals();

    // Exact figures, not compact ones. These four are the same numbers as the
    // budget table's totals row, and a page that shows "€5.7K" in one place
    // and "€5,660" in another is a page nobody trusts to reconcile against.
    setText('mCommitted', fmtCur(m.total));
    setText('mCommittedEur', fmtSecondary(m.total));
    setText('mForecast', fmtCur(Math.round(m.forecast)));
    setText('mForecastSub', t('pr.quoted', { a: Number(state.inflationPct) || 0 }));
    setText('mPaid', fmtCur(m.paid));
    setText('mPaidSub', m.total ? t('pr.ofcommitted', { a: Math.round((m.paid / m.total) * 100) }) : '');
    setText('mOutstanding', fmtCur(m.owing));
    setText('mOutstandingEur', fmtSecondary(m.owing));

    var budgetStatEl = document.getElementById('statBudget');
    var budgetFillEl = document.getElementById('budgetBarFill');

    if (m.ceiling > 0) {
      var pct = Math.round((m.total / m.ceiling) * 100);
      budgetStatEl.textContent = pct + '%';
      budgetStatEl.classList.toggle('warn', pct > 100);
      setText('statBudgetSub', t('pr.ceilsub', {
        a: fmtShort(m.total), b: fmtShort(m.ceiling), c: fmtShort(m.ceiling - m.forecast)
      }));
      budgetFillEl.style.width = Math.min(pct, 100) + '%';
      budgetFillEl.classList.toggle('over', pct > 100);
    } else {
      budgetStatEl.textContent = fmtCur(m.total);
      budgetStatEl.classList.remove('warn');
      setText('statBudgetSub', t('pr.noceiling'));
      budgetFillEl.style.width = '0%';
      budgetFillEl.classList.remove('over');
    }

    var paidPct = m.total ? Math.round((m.paid / m.total) * 100) : 0;
    setText('paidBarPct', paidPct + '%');
    setText('paidBarSub', t('pr.of', { a: fmtShort(m.paid), b: fmtShort(m.total) }));
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
      li.appendChild(cell('span', '', t3.name || t('un.untitled')));
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
      ta.placeholder = t('w.ph');
      ta.addEventListener('input', function () {
        state.notes[idx].text = ta.value;
        save();
      });

      var del = delButton(t('w.del'), function () {
        state.notes.splice(idx, 1);
        save();
        renderNotes();
      });

      row.appendChild(ta);
      row.appendChild(del);
      host.appendChild(row);
    });
    relock();
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
    var m = totals();
    setText('sumTotal', fmtCur(m.total));
    setText('sumPaid', fmtCur(m.paid));
    setText('sumOwing', fmtCur(m.owing));
    // Repeated below the table. On a wide screen the grid scrolls sideways and
    // takes its own tfoot with it; on a narrow one the buffer and the headroom
    // are not in the tfoot at all. Either way this is the copy that survives.
    setText('sumTotalAlt', fmtCur(m.total));
    setText('sumOwingAlt', fmtCur(m.owing));
    setText('sumForecast', fmtCur(Math.round(m.forecast)));
    setText('sumHeadroom', m.ceiling > 0 ? fmtCur(Math.round(m.ceiling - m.forecast)) : '–');

    // Rust means outstanding, so it only appears while something is.
    var owingEls = [document.getElementById('sumOwing'), document.getElementById('sumOwingAlt')];
    owingEls.forEach(function (el) { if (el) el.classList.toggle('owing', m.owing > 0); });
    renderSplit();
    renderSettlement();
  }

  // ---------- Sponsors ----------
  function sponsorById(id) {
    for (var i = 0; i < state.sponsors.length; i++) {
      if (state.sponsors[i].id === id) return state.sponsors[i];
    }
    return null;
  }
  function sponsorCode(s) { return (s && s.code ? s.code : '').trim() || t('sp.untitled'); }
  function sponsorLabel(s) {
    if (!s) return t('sp.unassigned');
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
      code.placeholder = t('sp.code');
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
      name.placeholder = t('sp.who');
      name.value = sp.name || '';
      name.addEventListener('input', function () {
        state.sponsors[idx].name = name.value;
        save();
        renderSplit();
      });

      var amt = document.createElement('span');
      amt.className = 'sp-amt';
      amt.textContent = fmtCur(sponsorShare(sp.id));

      var del = delButton(t('sp.del'), function () {
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
    relock();
    syncEmptyState();
  }

  // Amount attributed to one sponsor, shared lines divided evenly. Summed in
  // minor units and converted once, so a dozen shared lines cannot drift.
  function sponsorShare(id) {
    var sum = 0;
    state.budgetItems.forEach(function (i) {
      var ids = i.sponsors || [];
      if (ids.indexOf(id) !== -1) sum += lineTotalMinor(i) / ids.length;
    });
    return Math.round(toMajor(sum));
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

    // Every amount below is minor units, so the division between sponsors and
    // the percentages further down all run on integers.
    if (state.splitEvenly) {
      state.sponsors.forEach(function (sp) { add(sponsorLabel(sp), 0); });
      state.budgetItems.forEach(function (i) {
        var ids = i.sponsors || [];
        var tot = lineTotalMinor(i);
        if (!ids.length) { add(t('sp.unassigned'), tot); return; }
        ids.forEach(function (id) { add(sponsorLabel(sponsorById(id)), tot / ids.length); });
      });
    } else {
      state.budgetItems.forEach(function (i) {
        var codes = itemCodes(i);
        var key = codes.length
          ? codes.join(' + ') + (codes.length > 1 ? ' ' + t('sp.shared') : '')
          : t('sp.unassigned');
        add(key, lineTotalMinor(i));
      });
    }

    var list = document.getElementById('splitList');
    list.innerHTML = '';
    var grand = totals().totalMinor;
    order.sort(function (a, b) { return groups[b] - groups[a]; }).forEach(function (k) {
      var li = document.createElement('li');
      var share = grand ? Math.round((groups[k] / grand) * 100) : 0;
      li.appendChild(cell('span', '', k));
      li.appendChild(cell('span', 'amt', fmtCur(Math.round(toMajor(groups[k])))));
      li.appendChild(cell('span', 'pct', share + '%'));
      list.appendChild(li);
    });

    // keep the per-sponsor amounts in the editor in step
    var rows = document.querySelectorAll('#sponsorGrid .sponsor-row');
    state.sponsors.forEach(function (sp, idx) {
      if (rows[idx]) rows[idx].querySelector('.sp-amt').textContent = fmtCur(sponsorShare(sp.id));
    });
  }

  /* ---------- After the event ----------
   * A dated, one-shot thing. The day after, this stops being a plan and
   * becomes the record of what happened, so it reads as one: the closing
   * figures, who covered what, and no control that implies anything can still
   * be changed. Recoverable in one deliberate click, because a date in an
   * environment variable is as likely to be wrong as anything else here — and
   * because "something still needs settling" is the normal case, not the
   * exception.
   *
   * With no event date configured there is no "after": every branch below is
   * gated on isPast(), which is false forever when EVENT_DATE is null.
   */
  function renderSettlement() {
    var list = document.getElementById('settleList');
    if (!list || !isPast()) return;

    // Always split shared lines between the people who share them: the
    // question this list answers is "what did each person cover", and a joint
    // line attributed to nobody in particular does not answer it. That is the
    // same arithmetic as the figure beside each sponsor, so the two agree.
    var rows = state.sponsors.map(function (sp) {
      return { label: sponsorLabel(sp), amount: sponsorShare(sp.id) };
    });
    var loose = 0;
    state.budgetItems.forEach(function (i) {
      if (!(i.sponsors || []).length) loose += lineTotalMinor(i);
    });
    if (loose) rows.push({ label: t('sp.unassigned'), amount: Math.round(toMajor(loose)) });

    var grand = totals().total;
    list.innerHTML = '';
    rows.sort(function (a, b) { return b.amount - a.amount; }).forEach(function (r) {
      var li = document.createElement('li');
      li.appendChild(cell('span', '', r.label));
      li.appendChild(cell('span', 'amt', fmtCur(r.amount)));
      li.appendChild(cell('span', 'pct', (grand ? Math.round((r.amount / grand) * 100) : 0) + '%'));
      list.appendChild(li);
    });
    var empty = document.getElementById('settleEmpty');
    if (empty) empty.style.display = rows.length ? 'none' : 'block';
  }

  /* readOnly rather than disabled wherever the control supports it: a closed
   * ledger still has to be readable, selectable and copyable, and a disabled
   * input is none of those. Selects and checkboxes have no readonly, so those
   * are disabled outright. */
  var editingLocked = false;

  function lockEditing(on) {
    if (!on && !editingLocked) return;
    editingLocked = on;
    var wrap = document.querySelector('.wrap');
    Array.prototype.forEach.call(wrap.querySelectorAll('input, textarea, select'), function (el) {
      if (el.id === 'importFile') return;
      if (el.tagName === 'SELECT' || el.type === 'checkbox') el.disabled = on;
      else el.readOnly = on;
    });
    Array.prototype.forEach.call(wrap.querySelectorAll('.by-btn'), function (b) { b.disabled = on; });
    // Export stays open — an archive nobody can take a copy of is a worse
    // archive. Import does not: replacing the planner is an edit.
    var imp = document.getElementById('importData');
    if (imp) imp.disabled = on;
  }

  // Rows are rebuilt from scratch on every render, so the lock has to be
  // re-applied to the new controls. Free when nothing is locked.
  function relock() { if (editingLocked) lockEditing(true); }

  function applyMode() {
    var past = isPast();
    var locked = past && !state.reopened;
    document.body.classList.toggle('is-archived', locked);
    document.body.classList.toggle('is-reopened', past && !locked);
    setText('archiveLine', locked ? t('ar.closed') : t('ar.open'));
    setText('reopenPlanner', locked ? t('ar.reopen') : t('ar.close'));
    lockEditing(locked);
    renderCountdown();
  }

  (function () {
    var btn = document.getElementById('reopenPlanner');
    if (!btn) return;
    btn.addEventListener('click', function () {
      state.reopened = !state.reopened;
      // Written immediately rather than on the debounce: this is the one
      // setting someone is likely to change and then close the tab.
      flushSave();
      applyMode();
      renderSettlement();
    });
  })();

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

    // Take both references and clear the shared state *before* removing the
    // node. Removing a focused element fires focusout synchronously, and that
    // handler calls back in here — while openPop was still set, the re-entrant
    // call ran to completion and nulled openBtn, so by the time the outer call
    // reached focus() there was nothing left to focus and the ring landed on
    // <body>. Clearing first makes the re-entrant call a no-op at the guard.
    var pop = openPop;
    var btn = openBtn;
    openPop = null;
    openBtn = null;

    pop.remove();
    if (btn) {
      btn.setAttribute('aria-expanded', 'false');
      if (returnFocus) btn.focus();
    }
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
      none.textContent = t('sp.none');
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
    note.textContent = t('sp.multi');
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
    btn.textContent = codes.length ? codes.join(' · ') : t('sp.unassigned');
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
      grip.title = t('b.gripcol');
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
    grip.title = t('b.griprow');
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
      emptyRow(body, 9, t('b.empty'));
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
      // The column heading travels with the cell. A phone stacks this row into
      // a card with no header above it, and the label has to come from the
      // string table rather than from the stylesheet to stay translatable.
      label(tdUnit, 'c.unit');
      label(tdQty, 'c.qty');
      label(tdTotal, 'f.committed');
      label(tdPaid, 'f.paid');
      label(tdOwing, 'f.outstanding');
      label(tdBy, 'c.by');
      label(tdNote, 'c.remarks');
      label(tdDel, 'c.remove');
      tdDel.appendChild(delButton(t('b.del'), function () {
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
    relock();
    syncEmptyState();
  }

  function label(td, key) { td.setAttribute('data-label', t(key)); }

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
  var STATUS_KEYS = { 'not-started': 'k.notstarted', 'in-progress': 'k.inprogress', 'done': 'k.done' };

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
      Object.keys(STATUS_KEYS).forEach(function (key) {
        var opt = document.createElement('option');
        opt.value = key;
        opt.textContent = t(STATUS_KEYS[key]);
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
      tdDel.appendChild(delButton(t('k.del'), function () {
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
      emptyRow(body, 5, t(state.tasks.length ? 'k.nofilter' : 'k.empty'));
    }
    relock();
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
  // Its own function because the settings are a row like any other now: a
  // reconcile can bring somebody else's ceiling in, and these four controls
  // are where it has to land.
  function renderSettingsInputs() {
    ceilingInput.value = state.ceiling || '';
    inflationInput.value = state.inflationPct;
    rateInput.value = state.fxRate || '';
    splitToggle.checked = !!state.splitEvenly;
  }

  function renderAll() {
    renderSettingsInputs();
    syncEmptyState();
    renderSponsors();
    applyColWidths();
    renderBudgetTable();
    renderBudgetTotals();
    renderTasksTable();
    renderNotes();
    renderOverview();
    applyMode();
  }

  /* ---------- The status line ----------
   * One element, two kinds of message, and a precedence between them so they
   * cannot erase each other. A flash reports something that just happened and
   * stops being true a few seconds later. A sticky message is a condition that
   * is still true — writes are not reaching the server — and has to outlive
   * every flash, or the one piece of news worth having disappears behind
   * "Exported soiree-2030-06-12.json".
   */
  var FLASH_MS = 6000;
  var stickyMsg = '';
  var flashToken = 0;
  var flashUntil = 0;

  function paintMsg(text) {
    var el = document.getElementById('dataMsg');
    if (el) el.textContent = text;
  }

  function flash(msg) {
    paintMsg(msg);
    flashUntil = Date.now() + FLASH_MS;
    var mine = ++flashToken;
    setTimeout(function () {
      // Only if nothing newer has taken the element in the meantime.
      if (mine === flashToken) paintMsg(stickyMsg);
    }, FLASH_MS);
  }

  function setSticky(msg) {
    msg = msg || '';
    if (stickyMsg === msg) return;
    stickyMsg = msg;
    if (Date.now() >= flashUntil) paintMsg(stickyMsg);
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
      flash(t('d.exported', { a: filename }));
    } catch (e) {
      flash(t('d.exportfail'));
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
      // Clear the input first, not last. An <input type="file"> fires no
      // change event when the same filename is picked again, so leaving a
      // rejected or cancelled file in place meant a second attempt at the
      // same file did nothing at all — no import, no message, no clue why.
      importFile.value = '';
      try {
        var incoming = JSON.parse(reader.result);
        if (!incoming || !Array.isArray(incoming.budgetItems) || !Array.isArray(incoming.tasks)) {
          flash(t('d.notplanner'));
          return;
        }
        if (!window.confirm(t('d.confirm'))) return;
        // A wholesale replacement is the largest edit this page can make, and
        // in API mode it is a real one: every row the server holds and this
        // file does not is removed, and every row in the file is created.
        dirty = true;
        state = incoming;
        if (!Array.isArray(state.sponsors)) state.sponsors = [];
        if (!Array.isArray(state.notes)) state.notes = [];
        if (!Array.isArray(state.colWidths) || state.colWidths.length !== 9) state.colWidths = DEFAULT_COL_WIDTHS.slice();
        if (!state.rowHeights || typeof state.rowHeights !== 'object') state.rowHeights = {};
        if (typeof state.fxRate !== 'number') state.fxRate = Number(state.eurRate) || 0;
        if (typeof state.reopened !== 'boolean') state.reopened = false;
        flushSave();
        renderAll();
        flash(t('d.imported', { a: f.name }));
      } catch (e) {
        flash(t('d.unreadable'));
      }
    };
    reader.readAsText(f);
  });

  // ---------- Init ----------
  applyStrings();
  renderCountdown();
  initColGrips();
  renderAll();

  // Paint first, ask second. The cached copy is on screen before this request
  // is even sent, which is what keeps a 300 ms origin off the first render —
  // and if the answer is "no database", nothing further is ever sent.
  connect(0);

  // Offline shell. Registered last so it never delays first paint.
  if ('serviceWorker' in navigator) {
    window.addEventListener('load', function () {
      navigator.serviceWorker.register('/sw.js').catch(function () { /* non-fatal */ });
    });
  }
})();
