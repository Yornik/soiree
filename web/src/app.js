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
   * THEME
   * ------------------------------------------------------------------
   * Both palettes are in the stylesheet already and :root[data-theme] is the
   * hook that picks one. Until now nothing set it.
   *
   * A preference of the device, not a fact about the event, so it is the one
   * thing here that deliberately does not travel: not in `state`, not in the
   * export, not over the API. One person reading in the dark must not darken
   * the ledger for everybody else. Hence a key of its own — and only once
   * somebody has chosen: "follow the system" is the default and is stored by
   * storing nothing, so a planner nobody has themed keeps exactly the one key
   * it documents.
   *
   * Applied as the first thing this file does, so the page is painted once
   * rather than repainted. A frame of the OS theme still gets through before
   * this script runs; closing that needs an inline <script> in the head, which
   * the CSP forbids on purpose.
   * ------------------------------------------------------------------ */
  var THEME_KEY = 'soiree.theme';
  var THEMES = ['system', 'light', 'dark'];

  function readTheme() {
    try { return localStorage.getItem(THEME_KEY) || 'system'; } catch (e) { return 'system'; }
  }
  function paintTheme(mode) {
    if (mode === 'light' || mode === 'dark') document.documentElement.setAttribute('data-theme', mode);
    else document.documentElement.removeAttribute('data-theme');
  }
  paintTheme(readTheme());

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

  /* The event is on a calendar day, in a place. Both are in the configured
   * string as it was written - "2030-06-12T00:00:00+09:00" is the 12th, at
   * nine hours ahead of UTC - and neither survives `new Date()`, which keeps
   * the instant and forgets the rest. Read as an instant, that evening is
   * 15:00 UTC on the 11th, and a page that formats the instant announces the
   * 11th to everybody, wherever they are.
   *
   * So the day is taken from the text, and "today" is reckoned at the event's
   * own offset rather than the viewer's: the date and the days to go are then
   * the same on every screen, and the count turns over when the day turns
   * over where the event is.
   */
  var EVENT = (function () {
    var m = /^(\d{4})-(\d{2})-(\d{2})T\d{2}:\d{2}:\d{2}(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(String(CONFIG.eventDate || ''));
    if (!m) return null;
    var day = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
    if (isNaN(day)) return null;
    var offset = 0;
    if (m[4] !== 'Z') {
      offset = (Number(m[4].slice(1, 3)) * 60 + Number(m[4].slice(4, 6))) * (m[4].charAt(0) === '-' ? -1 : 1);
    }
    return { day: day, offsetMinutes: offset };
  })();
  var EVENT_DATE = EVENT ? new Date(EVENT.day) : null;

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
   *   2. the flag they chose — the switcher at the top of the page. Somebody's
   *                            own explicit choice, made on this device, so it
   *                            is kept on this device (LANG_KEY) and outranks
   *                            what their browser asks for: plenty of people
   *                            read a language their phone was never set to.
   *   3. the browser         — navigator.languages, in the person's own
   *                            order, first one there is a translation for. A
   *                            deployment has one locale and the people using
   *                            it do not have one language, and the setting
   *                            somebody made on their own device is the best
   *                            evidence there is of which one they read. It is
   *                            also theirs to change. Nothing about language
   *                            is stored against an account, for that reason:
   *                            an admin's guess made once, when inviting
   *                            somebody, is right for the one mail it was made
   *                            for and wrong to pin an interface to.
   *   4. config.language     — read if the server ever sends it.
   *   5. config.locale       — what an operator already configures, and it
   *                            carries the language tag: nl-NL -> nl.
   *   6. 'en'
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
      'ov.money': 'Money',
      'ov.todo': 'To do',
      'ru.today': 'today',
      'ru.overdue': '{n} overdue',
      'ru.aria': 'What is due, from today to the day',
      'ru.many': '{n} tasks, {a} to {b}',
      'ru.sameday': '{n} tasks, {a}',
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
      'sp.confirmdel': 'Lines on this sponsor: {n}. They lose the attribution and the split changes. Remove the sponsor?',
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
      'b.confirmdel': 'Files on this line: {n}. They go with it and cannot be recovered. Remove the line?',
      'b.confirmdelunknown': 'The files on this line cannot be counted until this browser has the planner from the server. Any there are go with it, for everyone, and cannot be recovered. Remove the line?',
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
      'k.confirmdel': 'Files on this task: {n}. They go with it and cannot be recovered. Remove the task?',
      'k.confirmdelunknown': 'The files on this task cannot be counted until this browser has the planner from the server. Any there are go with it, for everyone, and cannot be recovered. Remove the task?',
      'f.open': 'Files on this row: {n}',
      'f.title': 'Files',
      'f.none': 'No files yet.',
      'f.add': 'Add files',
      'f.limit': 'Up to {a} per file.',
      'f.wait': 'This row is still being saved. Try again in a moment.',
      'f.download': 'Download',
      'f.del': 'Remove {a}',
      'f.confirmdel': 'Remove {a}? This cannot be undone.',
      'f.sending': 'Uploading… {n}%',
      'f.checking': 'Checking…',
      'f.retry': 'Try again',
      'f.toolarge': 'Too large. The limit is {a}.',
      'f.empty': 'This file is empty.',
      'f.full': 'There is no room left for this file.',
      'f.readonly': 'You can view files, not add them.',
      'f.network': 'The upload did not get through. Check the connection.',
      'f.failed': 'The upload failed.',
      'f.delfailed': 'Could not remove the file.',
      'aria.filter': 'Filter tasks by status',
      'd.export': 'Export data (JSON)',
      'd.import': 'Import data…',
      'd.exported': 'Exported {a}',
      'd.exportfail': 'Could not export automatically — copy the JSON from the console instead.',
      'd.imported': 'Imported {a}',
      'd.notplanner': 'That file does not look like planner data.',
      'd.unreadable': 'Could not read that file.',
      'd.confirm': 'Replace everything currently in this planner with the imported data?',
      'd.shared': 'This changes the planner for everyone, not only in this browser.',
      'd.confirmcounts': 'Lines removed: {a}. Tasks removed: {b}.',
      'd.confirmfiles': 'Files attached to them: {n}, and those cannot be recovered.',
      'd.confirmcopy': 'A copy of the planner as it stands now is downloaded first.',
      'd.merged': 'Someone else was editing the same line. Both sets of changes have been kept.',
      'd.replaced': 'Someone else changed the same field at the same time. Yours replaced theirs.',
      'd.overtaken': 'Someone else had just changed the line you removed. It is gone.',
      'd.gone': 'Someone else removed the line you were editing. Your changes to it are gone with it.',
      'd.offline': 'Your changes are not reaching the server. Still trying — they are safe in this browser meanwhile.',
      'd.online': 'Back in touch with the server. Everything is saved.',
      'd.refused': 'The server would not accept one of your changes. It is still here, but only in this browser — export the planner if it matters.',
      'd.session': 'Your session has ended. Sign in again — what you changed is safe in this browser and is sent as soon as you are back.',
      'ar.closed': 'This event has passed. The planner is closed, and the figures below are the final reckoning.',
      'ar.reopen': 'Reopen for editing',
      'ar.open': 'Reopened for editing. Close it again once everything is settled.',
      'ar.close': 'Close the planner',
      'ar.settle': 'The final reckoning',
      'ar.settlenote': 'what each person covered',
      'ar.nothing': 'Nothing was recorded.',
      'th.title': 'Theme',
      'th.system': 'System',
      'th.light': 'Light',
      'th.dark': 'Dark',
      'n.offer': 'Want a reminder here when a deadline is near? One notification on this device, listing what is coming up.',
      'n.on': 'Turn on',
      'n.later': 'Not now',
      'n.done': 'Reminders are on for this device.',
      'n.no': 'Notifications are blocked for this site. Your browser’s site settings can undo that.',
      'n.why': 'Reminders are not on yet. “Your account” has the switch, and says what is in the way.'
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
      'ov.money': 'Geld',
      'ov.todo': 'Te doen',
      'ru.today': 'vandaag',
      'ru.overdue': '{n} te laat',
      'ru.aria': 'Wat er moet gebeuren, van vandaag tot de dag',
      'ru.many': '{n} taken, {a} tot {b}',
      'ru.sameday': '{n} taken, {a}',
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
      'sp.confirmdel': 'Posten op naam van deze bijdrager: {n}. Die verliezen de toewijzing en de verdeling verandert. Bijdrager verwijderen?',
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
      'b.confirmdel': 'Bestanden bij deze post: {n}. Die gaan mee en zijn niet terug te halen. Post verwijderen?',
      'b.confirmdelunknown': 'De bestanden bij deze post zijn niet te tellen zolang deze browser de planner niet van de server heeft. Wat er staat gaat mee, voor iedereen, en is niet terug te halen. Post verwijderen?',
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
      'k.confirmdel': 'Bestanden bij deze taak: {n}. Die gaan mee en zijn niet terug te halen. Taak verwijderen?',
      'k.confirmdelunknown': 'De bestanden bij deze taak zijn niet te tellen zolang deze browser de planner niet van de server heeft. Wat er staat gaat mee, voor iedereen, en is niet terug te halen. Taak verwijderen?',
      'f.open': 'Bestanden bij deze regel: {n}',
      'f.title': 'Bestanden',
      'f.none': 'Nog geen bestanden.',
      'f.add': 'Bestanden toevoegen',
      'f.limit': 'Maximaal {a} per bestand.',
      'f.wait': 'Deze regel wordt nog opgeslagen. Probeer het zo nog eens.',
      'f.download': 'Downloaden',
      'f.del': '{a} verwijderen',
      'f.confirmdel': '{a} verwijderen? Dit kan niet ongedaan worden gemaakt.',
      'f.sending': 'Uploaden… {n}%',
      'f.checking': 'Controleren…',
      'f.retry': 'Opnieuw proberen',
      'f.toolarge': 'Te groot. De limiet is {a}.',
      'f.empty': 'Dit bestand is leeg.',
      'f.full': 'Er is geen ruimte meer voor dit bestand.',
      'f.readonly': 'Je kunt bestanden bekijken, niet toevoegen.',
      'f.network': 'De upload is niet aangekomen. Controleer de verbinding.',
      'f.failed': 'De upload is mislukt.',
      'f.delfailed': 'Het bestand kon niet worden verwijderd.',
      'aria.filter': 'Taken filteren op status',
      'd.export': 'Gegevens exporteren (JSON)',
      'd.import': 'Gegevens importeren…',
      'd.exported': '{a} geëxporteerd',
      'd.exportfail': 'Automatisch exporteren lukte niet — kopieer de JSON uit de console.',
      'd.imported': '{a} geïmporteerd',
      'd.notplanner': 'Dat bestand lijkt geen plannergegevens te bevatten.',
      'd.unreadable': 'Dat bestand kon niet gelezen worden.',
      'd.confirm': 'Alles wat nu in deze planner staat vervangen door de geïmporteerde gegevens?',
      'd.shared': 'Dit verandert de planner voor iedereen, niet alleen in deze browser.',
      'd.confirmcounts': 'Posten die verdwijnen: {a}. Taken die verdwijnen: {b}.',
      'd.confirmfiles': 'Bestanden die eraan hangen: {n}, en die zijn niet terug te halen.',
      'd.confirmcopy': 'Er wordt eerst een kopie van de huidige planner gedownload.',
      'd.merged': 'Iemand anders bewerkte dezelfde regel. Beide wijzigingen zijn bewaard.',
      'd.replaced': 'Iemand anders wijzigde hetzelfde veld op hetzelfde moment. Jouw waarde heeft die van hen vervangen.',
      'd.overtaken': 'Iemand anders had de regel die je verwijderde net gewijzigd. Hij is nu weg.',
      'd.gone': 'Iemand anders heeft de regel die jij aan het bewerken was verwijderd. Jouw wijzigingen daaraan zijn ermee weg.',
      'd.offline': 'Je wijzigingen bereiken de server niet. Er wordt opnieuw geprobeerd — ondertussen staan ze veilig in deze browser.',
      'd.online': 'Weer verbinding met de server. Alles is opgeslagen.',
      'd.refused': 'De server accepteerde een van je wijzigingen niet. Hij staat er nog wel, maar alleen in deze browser — exporteer de planner als het belangrijk is.',
      'd.session': 'Je sessie is verlopen. Meld je opnieuw aan — je wijzigingen staan veilig in deze browser en worden verstuurd zodra je terug bent.',
      'ar.closed': 'Dit feest is geweest. De planner is gesloten; de cijfers hieronder zijn de eindafrekening.',
      'ar.reopen': 'Heropenen om te bewerken',
      'ar.open': 'Weer opengesteld. Sluit de planner zodra alles is afgerekend.',
      'ar.close': 'Planner sluiten',
      'ar.settle': 'De eindafrekening',
      'ar.settlenote': 'wat ieder heeft betaald',
      'ar.nothing': 'Er is niets vastgelegd.',
      'th.title': 'Thema',
      'th.system': 'Systeem',
      'th.light': 'Licht',
      'th.dark': 'Donker',
      'n.offer': 'Hier een seintje krijgen als een deadline nadert? Eén melding op dit apparaat, met wat eraan komt.',
      'n.on': 'Aanzetten',
      'n.later': 'Nu niet',
      'n.done': 'Herinneringen staan aan op dit apparaat.',
      'n.no': 'Meldingen zijn geblokkeerd voor deze site. Dat kun je terugdraaien bij de site-instellingen van je browser.',
      'n.why': 'Herinneringen staan nog niet aan. Bij “Je account” vind je de schakelaar, en wat er in de weg zit.'
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
      'ov.money': 'Uang',
      'ov.todo': 'Yang harus dikerjakan',
      'ru.today': 'hari ini',
      'ru.overdue': '{n} terlambat',
      'ru.aria': 'Yang jatuh tempo, dari hari ini sampai harinya',
      'ru.many': '{n} tugas, {a} sampai {b}',
      'ru.sameday': '{n} tugas, {a}',
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
      'sp.confirmdel': 'Baris atas nama penyumbang ini: {n}. Semuanya kehilangan penanggungnya dan pembagiannya berubah. Hapus penyumbang?',
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
      'b.confirmdel': 'Berkas di baris ini: {n}. Semuanya ikut terhapus dan tidak bisa dikembalikan. Hapus baris ini?',
      'b.confirmdelunknown': 'Berkas di baris ini belum bisa dihitung selama browser ini belum mengambil perencana dari server. Yang ada ikut terhapus, untuk semua orang, dan tidak bisa dikembalikan. Hapus baris ini?',
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
      'k.confirmdel': 'Berkas di tugas ini: {n}. Semuanya ikut terhapus dan tidak bisa dikembalikan. Hapus tugas ini?',
      'k.confirmdelunknown': 'Berkas di tugas ini belum bisa dihitung selama browser ini belum mengambil perencana dari server. Yang ada ikut terhapus, untuk semua orang, dan tidak bisa dikembalikan. Hapus tugas ini?',
      'f.open': 'Berkas di baris ini: {n}',
      'f.title': 'Berkas',
      'f.none': 'Belum ada berkas.',
      'f.add': 'Tambah berkas',
      'f.limit': 'Maksimal {a} per berkas.',
      'f.wait': 'Baris ini masih disimpan. Coba lagi sebentar.',
      'f.download': 'Unduh',
      'f.del': 'Hapus {a}',
      'f.confirmdel': 'Hapus {a}? Ini tidak bisa dibatalkan.',
      'f.sending': 'Mengunggah… {n}%',
      'f.checking': 'Memeriksa…',
      'f.retry': 'Coba lagi',
      'f.toolarge': 'Terlalu besar. Batasnya {a}.',
      'f.empty': 'Berkas ini kosong.',
      'f.full': 'Tidak ada ruang lagi untuk berkas ini.',
      'f.readonly': 'Anda dapat melihat berkas, tidak menambahkannya.',
      'f.network': 'Unggahan tidak sampai. Periksa koneksi.',
      'f.failed': 'Unggahan gagal.',
      'f.delfailed': 'Berkas tidak dapat dihapus.',
      'aria.filter': 'Saring tugas menurut status',
      'd.export': 'Ekspor data (JSON)',
      'd.import': 'Impor data…',
      'd.exported': '{a} diekspor',
      'd.exportfail': 'Ekspor otomatis gagal — salin JSON dari konsol.',
      'd.imported': '{a} diimpor',
      'd.notplanner': 'Berkas itu sepertinya bukan data perencana.',
      'd.unreadable': 'Berkas itu tidak bisa dibaca.',
      'd.confirm': 'Ganti semua isi perencana ini dengan data yang diimpor?',
      'd.shared': 'Ini mengubah perencana untuk semua orang, bukan hanya di browser ini.',
      'd.confirmcounts': 'Baris yang dihapus: {a}. Tugas yang dihapus: {b}.',
      'd.confirmfiles': 'Berkas yang menempel padanya: {n}, dan itu tidak bisa dikembalikan.',
      'd.confirmcopy': 'Salinan perencana yang sekarang diunduh lebih dulu.',
      'd.merged': 'Orang lain sedang mengubah baris yang sama. Kedua perubahan tetap tersimpan.',
      'd.replaced': 'Orang lain mengubah bidang yang sama pada saat bersamaan. Nilaimu menggantikan nilai mereka.',
      'd.overtaken': 'Orang lain baru saja mengubah baris yang kamu hapus. Baris itu sudah hilang.',
      'd.gone': 'Orang lain menghapus baris yang sedang kamu ubah. Perubahanmu pada baris itu ikut hilang.',
      'd.offline': 'Perubahanmu belum sampai ke server. Masih dicoba lagi — sementara ini aman tersimpan di browser.',
      'd.online': 'Terhubung lagi dengan server. Semuanya tersimpan.',
      'd.refused': 'Server menolak salah satu perubahanmu. Perubahan itu masih ada, tetapi hanya di browser ini — ekspor perencana kalau ini penting.',
      'd.session': 'Sesimu sudah berakhir. Masuk lagi — perubahanmu aman tersimpan di browser ini dan dikirim begitu kamu kembali.',
      'ar.closed': 'Acara ini sudah lewat. Perencana ditutup dan angka di bawah adalah perhitungan akhir.',
      'ar.reopen': 'Buka lagi untuk diubah',
      'ar.open': 'Dibuka lagi untuk diubah. Tutup lagi setelah semuanya beres.',
      'ar.close': 'Tutup perencana',
      'ar.settle': 'Perhitungan akhir',
      'ar.settlenote': 'berapa yang ditanggung tiap orang',
      'ar.nothing': 'Tidak ada yang tercatat.',
      'th.title': 'Tema',
      'th.system': 'Sistem',
      'th.light': 'Terang',
      'th.dark': 'Gelap',
      'n.offer': 'Mau diingatkan di sini kalau tenggat sudah dekat? Satu notifikasi di perangkat ini, berisi apa yang akan datang.',
      'n.on': 'Nyalakan',
      'n.later': 'Nanti saja',
      'n.done': 'Pengingat aktif di perangkat ini.',
      'n.no': 'Notifikasi diblokir untuk situs ini. Kamu bisa membatalkannya di pengaturan situs browser.',
      'n.why': 'Pengingat belum aktif. Di “Akunmu” ada tombolnya, dan penjelasan apa yang menghalangi.'
    }
  };

  function pickLang(v) {
    var tag = String(v || '').toLowerCase().replace(/_/g, '-').split('-')[0];
    return LANGS.indexOf(tag) !== -1 ? tag : '';
  }

  // What the deployment speaks, with nobody's preference applied.
  var LANG_DEFAULT = pickLang(CONFIG.language) || pickLang(CONFIG.locale) || 'en';

  // A language in the URL is somebody's explicit choice for this view, and
  // nothing an account says overrides it.
  var LANG_FROM_URL = (function () {
    var q = '';
    try {
      q = (location.search.match(/[?&]lang=([^&]*)/) || [])[1] || '';
      q = decodeURIComponent(q);
    } catch (e) { q = ''; }
    return pickLang(q);
  })();

  // The first language the browser asks for that this page is written in.
  // `languages` is the person's whole ordered list; `language` is only its
  // head, and somebody whose list is German, then Dutch, should get Dutch.
  var LANG_FROM_BROWSER = (function () {
    var list = [];
    try {
      list = (navigator.languages && navigator.languages.length)
        ? navigator.languages : [navigator.language];
    } catch (e) { list = []; }
    for (var i = 0; i < list.length; i += 1) {
      var tag = pickLang(list[i]);
      if (tag) return tag;
    }
    return '';
  })();

  // The third and last thing this page keeps in localStorage, beside the
  // planner and the theme: the language somebody picked from the switcher.
  // Theirs, on their device — nothing about language is stored on an account.
  var LANG_KEY = 'soiree.lang';

  var LANG = LANG_FROM_URL || (function () {
    try { return pickLang(localStorage.getItem(LANG_KEY)); } catch (e) { return ''; }
  })() || LANG_FROM_BROWSER || LANG_DEFAULT;

  // Things that say something once, when they are built, and have to be told
  // to say it again: see chooseLanguage().
  var languageHooks = [];

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
  // They add up to 1240, which is the page (--page in styles.css, 1280) less
  // its gutters: the whole table, Remarks and the remove button included, is on
  // screen at once on a desktop. Widen one without narrowing another and the
  // last columns go back to being reachable only by scrolling sideways.
  var DEFAULT_COL_WIDTHS = [230, 140, 70, 135, 110, 135, 135, 245, 40];

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
   * the three methods below and leave the rest of the file alone.
   *
   *   Store.read()       -> what was saved, or null if nothing was yet
   *   Store.write(state) -> persist the whole state object
   *   Store.keep()       -> persist it again, because the merge base moved
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
   *
   * With an API there is a second thing worth keeping: the shadow, the version
   * of every row the server last confirmed. It goes under the same key and in
   * the same setItem as the state, never beside it under a key of its own —
   * two writes are two moments, and a state paired on the next load with a
   * base another tab wrote differs from it in ways neither of them edited.
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
      this.put(s);
      // A no-op until the probe has found an API, so the local-only
      // deployment never touches the network again after it.
      Sync.push();
    },
    // The base moved with nobody typing: a write was confirmed, or a plan was
    // merged. Kept at once rather than at the next save, because the state and
    // the base have to be a pair — a page that came back holding a row the
    // base does not know would post it a second time, and a base holding a row
    // the state does not would delete it.
    keep: function () {
      if (shadow) this.put(state);
    },
    put: function (s) {
      // The base travels with the state or not at all: with no database there
      // is no base, and a bare state is also what every save before this one
      // looks like, which Store.read has to go on accepting.
      var value = shadow ? { state: s, shadow: shadow, idMap: idMap, createKeys: createKeys } : s;
      try { localStorage.setItem(this.key, JSON.stringify(value)); } catch (e) { /* storage unavailable */ }
    },
    clear: function () {
      try { localStorage.removeItem(this.key); } catch (e) { /* storage unavailable */ }
    }
  };

  var state;
  // Whether this browser arrived holding a planner somebody actually built,
  // as opposed to a blank one or generated demo data. It is the difference
  // between "this is a cached copy of the server's plan" and "this is the only
  // copy in existence", and adopt() has to know which it is looking at.
  var hadSavedCopy = false;
  // The merge base the last page left behind, the ids it had adopted by then,
  // and the names it had given the creates it had not sent. All three are
  // installed further down, where the shadow they belong to is declared; what
  // matters here is that they are taken out of the same value as the state, so
  // they cannot be from different moments.
  var savedBase = null;
  var savedIdMap = null;
  var savedCreateKeys = null;
  try {
    var saved = Store.read();
    hadSavedCopy = saved !== null;
    if (saved && saved.state && saved.shadow) {
      savedBase = saved.shadow;
      savedIdMap = saved.idMap;
      savedCreateKeys = saved.createKeys;
      saved = saved.state;
    }
    state = saved || (CONFIG.demoData ? demoState() : emptyState());
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

  // The planner exactly as this page found it, before anybody touched it. When
  // the plan arrives late — after a sign-in, on a tab reopened a week on — this
  // is the only record of what the copy on screen looked like before the edits
  // made to it, which is what tells an edit apart from a copy that is merely
  // old. See adopt().
  var loaded = JSON.parse(JSON.stringify(state));

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
  var shadow = null;       // rows as the server last confirmed them (restored below)
  var idMap = {};          // this browser's optimistic ids -> the server's uuids
  var createKeys = {};     // and the uuid each unsent create names itself by
  var blocked = {};        // writes the server refused, parked until they change

  // Backoff for a write that got no answer. Doubling from a second, capped,
  // because the common cause is a tunnel or a train and neither is helped by
  // hammering.
  var RETRY_BASE_MS = 1000;
  var RETRY_MAX_MS = 30000;
  // One failed write is a blip. Two in a row is worth telling somebody about,
  // because from here on their edits exist in one browser only.
  var FAILURES_BEFORE_NOTICE = 2;
  // A ceiling on reconcile-and-retry rounds inside one attempt. The three-way
  // merge below terminates on its own, so reaching this means somebody else is
  // rewriting the same rows about as fast as we are. It books a retry rather
  // than reporting success: stopping with work still queued and saying nothing
  // is exactly the silent loss the retry exists to prevent.
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
    function ids(v) { return (v || []).map(mapId); }
    return {
      name: name,
      canon: function (r) { return ids(r[name]).sort().join(','); },
      wire: function (r) { return ids(r[name]); },
      read: function (v) { return Array.isArray(v) ? v.slice() : []; },
      copy: function (v) { return (v || []).slice(); },
      // The one conflict a per-field merge can settle without discarding
      // either side. A list of ids is a set, not a value: theirs, plus what
      // was ticked here, less what was unticked here. Two people each naming
      // a payer for one line is what the picker is for, and neither of them
      // untagged the other's name.
      merge: function (base, mine, theirs) {
        var was = ids(base), now = ids(mine);
        var out = ids(theirs);
        now.forEach(function (id) {
          if (was.indexOf(id) === -1 && out.indexOf(id) === -1) out.push(id);
        });
        return out.filter(function (id) { return now.indexOf(id) !== -1 || was.indexOf(id) === -1; });
      }
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
   *
   * `entity` is the third name each of these has: the table, which is the word
   * the change feed speaks. Written down beside the other two rather than
   * derived, because `budget_items` -> `budget-items` -> `budgetItems` is three
   * spellings of one thing and a rule that converts between them is a rule to
   * get wrong.
   */
  var COLLECTIONS = [
    {
      key: 'sponsors', route: 'sponsors', entity: 'sponsors', container: 'sponsorGrid',
      fields: [textField('code'), textField('name')],
      render: function () { renderSponsors(); renderBudgetTable(); renderSplit(); }
    },
    {
      key: 'budgetItems', route: 'budget-items', entity: 'budget_items', container: 'budgetBody',
      fields: [
        textField('item'), moneyField('unit'), numberField('qty'),
        moneyField('paid'), textField('note'), idsField('sponsors')
      ],
      render: function () { renderBudgetTable(); renderBudgetTotals(); renderOverview(); }
    },
    {
      key: 'tasks', route: 'tasks', entity: 'tasks', container: 'tasksBody',
      fields: [textField('name'), textField('owner'), dateField('due'), textField('status')],
      render: function () { renderTasksTable(); renderOverview(); }
    },
    {
      key: 'notes', route: 'notes', entity: 'notes', container: 'watchList',
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
    key: 'settings', route: 'settings', entity: 'settings', singleton: true, container: 'panel-budget',
    fields: [
      moneyField('ceiling'), numberField('inflationPct'),
      numberField('fxRate'), boolField('splitEvenly')
    ],
    render: function () { renderSettingsInputs(); renderBudgetTotals(); renderOverview(); }
  };

  // Table name -> the descriptor that draws it, for reading the change feed.
  var BY_ENTITY = { settings: SETTINGS };
  COLLECTIONS.forEach(function (c) { BY_ENTITY[c.entity] = c; });

  // State key -> the same descriptor, for reading an op key back apart.
  var BY_KEY = { settings: SETTINGS };
  COLLECTIONS.forEach(function (c) { BY_KEY[c.key] = c; });

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

  // Removal by identity rather than by index. A remote change can add or take
  // away rows above this one, and a rebuild of the table is held off while
  // somebody is typing in it — so the position a row had when its controls were
  // drawn is not the position it has when one of them is pressed.
  function dropRow(list, row) {
    var i = list.indexOf(row);
    if (i !== -1) list.splice(i, 1);
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

  /* Every row in the plan becomes a row on the page, including the child rows
   * of a broken-down quote. This page has no notion of a parent and a
   * breakdown, so a plan that holds rows with a parentId — which only a
   * client writing to the API directly can create — reads its headline
   * figures high, because the parent and its children are both counted.
   *
   * cmd/soiree-import does not produce that. It folds a breakdown's amounts
   * into the parent, summarises the rows in the parent's note, and nests them
   * under a `children` key that the import below never reads.
   *
   * Do not fix the first case by filtering parentId out here. The shadow
   * would still hold those rows, so the very next difference would be a DELETE
   * for every child in the breakdown, and they would be destroyed by the act
   * of looking at them. Teaching the page about parents is the fix, and it is
   * a change to the page rather than to this function. */
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

  // A server id is a uuid; one minted here is a letter and seven characters of
  // base 36 (see uid), until adoptServerId() renames it when the create is
  // answered.
  var SERVER_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

  // The stand-in shadow adopt() merges against. The revisions are never read:
  // applyPlan() replaces every entry with the plan's own before it returns.
  function shadowFromLoaded() {
    var sh = { settings: { row: loaded, revision: 0 } };
    COLLECTIONS.forEach(function (c) {
      var byId = {};
      (loaded[c.key] || []).forEach(function (r) {
        if (r && r.id && SERVER_ID.test(r.id)) byId[r.id] = { row: cloneRow(c, r), revision: 0 };
      });
      sh[c.key] = byId;
    });
    return sh;
  }

  /* The real one, as the last page left it.
   *
   * A base that outlives the page is the whole of what tells an unsent edit
   * apart from a copy that is merely old. Without one `dirty` is false on the
   * next load, adopt() replaces the cached copy with the plan, and whatever
   * had not reached the server goes with it — after a status line that said it
   * was safe in this browser.
   *
   * Taken whole or not at all. A collection missing from it would read as
   * "somebody else added every row in that collection", which is a worse
   * answer than falling back to the copy as it was loaded.
   */
  function baseFromStored(v) {
    if (!v || !v.settings || !v.settings.row) return null;
    var whole = COLLECTIONS.every(function (c) {
      return v[c.key] && typeof v[c.key] === 'object';
    });
    return whole ? v : null;
  }

  // Installed before anything can save, so that a keystroke landing before the
  // plan does writes the base back out rather than dropping it.
  shadow = baseFromStored(savedBase);
  if (shadow && savedIdMap) idMap = savedIdMap;
  if (shadow && savedCreateKeys) createKeys = savedCreateKeys;

  // The server's answer to a write is the row as it now stands, so it is also
  // the new agreed version. Stored as its own object: a shadow that shared
  // references with `state` would diff to nothing forever, because every edit
  // site mutates its row in place.
  function shadowPut(coll, wireRow) {
    var entry = { row: rowFromWire(coll, wireRow), revision: Number(wireRow.revision) || 0 };
    if (coll.singleton) shadow.settings = entry;
    else shadow[coll.key][wireRow.id] = entry;
    // With the state it belongs to, at the moment they agree. A create that
    // landed is the case that cannot wait for the next keystroke: the row is
    // now in both under the server's id, and a page that came back holding
    // only the state would post it all over again.
    Store.keep();
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

  /* A create names the row it is creating, with a uuid of this browser's own.
   *
   * Nothing here can tell an answer that never arrived from a request that
   * never went, so a POST whose answer is lost on the way back is sent again.
   * Unnamed, the second one is a second budget line to the server, and the
   * same cost is then in every total twice with nothing saying so. Named, the
   * repeat is recognised and answered with the row already stored.
   *
   * Not the row's own id, which stays the one uid() minted. The shape of an id
   * is read in two places as "the server has this row": the stand-in shadow a
   * dirty reload merges against, and the gate on attaching a file to a line. A
   * uuid on a row that has never been sent would make that reload read it as a
   * row somebody else deleted and drop it, which turns a duplicate anybody can
   * see into work nobody can get back.
   */
  function createKeyFor(id) {
    if (createKeys[id]) return createKeys[id];
    createKeys[id] = uuidV4();
    // Kept before the request goes rather than after its answer, because the
    // answer is the thing that may not come: the retry that needs this name
    // may be on the other side of a reload.
    Store.keep();
    return createKeys[id];
  }

  // A version 4 uuid. getRandomValues rather than crypto.randomUUID(), which
  // exists only in a secure context and this page is served over plain http on
  // a LAN as well; Math.random for a browser with neither, since a name only
  // has to be unlikely to meet another row in one plan.
  function uuidV4() {
    var bytes = new Uint8Array(16);
    if (window.crypto && typeof window.crypto.getRandomValues === 'function') {
      window.crypto.getRandomValues(bytes);
    } else {
      for (var i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256);
    }
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    var out = '';
    for (var j = 0; j < 16; j++) {
      out += (bytes[j] + 0x100).toString(16).slice(1);
      if (j === 3 || j === 5 || j === 7 || j === 9) out += '-';
    }
    return out;
  }

  function sendCreate(op) {
    var c = op.coll;
    var row = liveRow(c, op.id);
    // Added and removed again inside one debounce window: it never existed on
    // the server, so there is nothing to create and nothing to delete either.
    if (!row) {
      delete createKeys[op.id];
      return Promise.resolve(true);
    }

    var body = {};
    c.fields.forEach(function (f) { body[f.name] = f.wire(row); });
    body.position = nextPosition(c);
    body.id = createKeyFor(op.id);

    return api('POST', '/' + c.route, body).then(function (res) {
      // 200 is this same create answered a second time: the first attempt
      // committed and its answer was lost, so what comes back is the row as
      // stored rather than a second one.
      //
      // Whether the row as stored is still the row this browser posted is its
      // revision. At 1 nobody has written it since, so the answer and the POST
      // say the same thing, and anything typed here since is a difference from
      // both that goes up as the next pass's patch.
      //
      // Above 1 somebody else wrote the line while the answer was not
      // arriving, and taking their row as the agreed version is what makes
      // that difference dangerous: this browser's copy, untouched since it was
      // typed, becomes a difference against the revision that now stands, so
      // the patch that follows puts the values of a row nobody has looked at
      // since over theirs and cannot 409. It is the one place the resync gate
      // cannot cover, because this is the write pass. So their row is
      // reconciled against the row as posted, the way a 409 is: a field only
      // they changed stays theirs, a field changed here since goes again, and
      // they are told rather than quietly overwritten.
      if ((res.status === 201 || res.status === 200) && res.body && res.body.id) {
        adoptServerId(c, op.id, res.body.id);
        delete createKeys[op.id];
        if (res.status === 200 && (Number(res.body.revision) || 0) > 1) {
          // The version both edits started from is the body that was posted,
          // so that is what the merge is given as the agreed one. Its revision
          // is never read: reconcile replaces it with the one that came back.
          shadow[c.key][res.body.id] = { row: rowFromWire(c, body), revision: 0 };
          reconcile({ kind: 'update', coll: c, id: res.body.id, fields: [] }, res.body);
          // What shadowPut keeps for a create that landed, and for the same
          // reason: the row is in the state and the shadow under the server's
          // id, and a page that came back holding only the state would post it
          // all over again.
          Store.keep();
        } else {
          shadowPut(c, res.body);
        }
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
        // The other half of the pair kept by shadowPut: gone from both, so a
        // page that came back cannot read it as a row somebody else added.
        Store.keep();
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
    if (res.status === 401) {
      // Not a refusal of this row — a refusal of this browser. Parking the row
      // would be exactly wrong: nothing about the edit needs to change for it
      // to be accepted, only who is asking, and a parked row stays parked
      // until it is edited again. So the pass stops, the shadow does not
      // advance, and the same difference goes up after the next sign-in.
      sessionLost();
      return false;
    }
    if (res.status >= 400 && res.status < 500) {
      // The server understood and said no. Sending the identical body again
      // would only produce the identical refusal, so this row is parked until
      // it changes. The edit is not lost — it is in `state`, on the screen and
      // in localStorage — and the person is told it has not left the browser.
      //
      // A 404 is the one answer that is not about the edit at all: somebody
      // else removed the line. The park still earns its place, because it
      // stops the identical write going again in the moment before the plan is
      // next read; but that read takes the row off this screen too, and there
      // is nowhere left to keep the work once the row it belongs to has no
      // server side. So the flash says the changes went with the line rather
      // than promising a copy the next read will drop.
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
   *   both changed it       -> a list of ids is a set and keeps both; anything
   *                            else keeps ours, and the person is told that
   *                            theirs is the value that went, rather than
   *                            being told both were kept.
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
    var landed = rowFromWire(c, current);
    var tookTheirs = false;
    var replaced = false;

    c.fields.forEach(function (f) {
      var mine = f.canon(row);
      var agreed = f.canon(base);
      var theirs = f.canon(landed);
      if (mine === agreed) {
        if (theirs === mine) return;            // nobody changed it
        row[f.name] = f.copy(landed[f.name]);
        tookTheirs = true;
        return;
      }
      if (theirs === agreed || theirs === mine) return;  // edited here: ours wins, and goes again
      if (!f.merge) { replaced = true; return; }
      row[f.name] = f.merge(base[f.name], row[f.name], landed[f.name]);
      tookTheirs = true;
    });

    known.row = landed;
    known.revision = Number(current.revision) || known.revision;

    flash(t(replaced ? 'd.replaced' : 'd.merged'));
    if (tookTheirs) scheduleRefresh(c);
  }

  /* ---------- Optimistic ids ----------
   * The page makes an id the moment a row appears, because the row has to be
   * addressable before any round trip could have answered. The uuid the row is
   * stored under is the one the create named (see createKeyFor), and the page
   * learns it from the answer. Reconciling the two is a rename, everywhere the
   * old one was referred to — otherwise a budget line keeps pointing at a
   * sponsor id that only ever existed in this browser.
   */
  function adoptServerId(coll, localId, serverId) {
    if (!serverId || localId === serverId) return;
    idMap[localId] = serverId;

    // The answer is not always the first thing to bring the row here. A plan
    // read between the create committing and its answer arriving carries it
    // under the very id the create named, and applyPlan cannot tell that from
    // a row somebody else added, so it is already on the page. Renaming onto
    // it would leave one id held by two rows, and everything downstream reads
    // this list by id: the difference is computed from both and sent from
    // whichever comes first, so the other never comes to match and the pass
    // goes round again for as long as the page is open. The copy from the
    // plan is the row as it was when it committed, and the copy being renamed
    // carries whatever was typed since, so the plan's is the one to let go of
    // and the difference between them goes up as the patch that follows.
    var fromPlan = findRow(state[coll.key], serverId);
    if (fromPlan) dropRow(state[coll.key], fromPlan);

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

    // A row has left the page, so what it was drawn in is drawn again, once
    // the rename above has left the state agreeing with itself. The figures
    // belong to no caret and are always current, the way applyPlan has them;
    // the table waits for a caret that is inside it, which is what
    // scheduleRefresh is for.
    if (fromPlan) {
      scheduleRefresh(coll);
      renderBudgetTotals();
      renderOverview();
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

  // Bumped whenever a write pass begins or ends. It is how a plan fetched from
  // the live stream knows it was overtaken: a read that spans a pass may be
  // missing a row this browser has just created, or carrying a revision it has
  // just superseded, and merging either would undo work that did land.
  var passes = 0;

  Sync.push = function () {
    if (!apiMode) return;
    Sync.queued = true;
    Sync.run();
  };

  Sync.run = function () {
    if (!apiMode || sessionGone || Sync.running || Sync.timer || !Sync.queued) return;
    Sync.queued = false;
    Sync.running = true;
    passes++;
    drain(0).then(Sync.done, function () { Sync.done('retry'); });
  };

  Sync.done = function (outcome) {
    Sync.running = false;
    passes++;
    if (outcome === 'done') {
      if (Sync.failures >= FAILURES_BEFORE_NOTICE) flash(t('d.online'));
      Sync.failures = 0;
      setSticky('');
      if (Sync.queued) Sync.run();
      return;
    }

    // No session: there is nothing to wait out. A retry timer here would send
    // the same write into the same 401 every thirty seconds for as long as the
    // tab stays open, and a 401 is what the proxy's ban rule counts. The work
    // stays queued; signing in is what runs it.
    if (sessionGone) { Sync.queued = true; return; }

    // 'retry' and 'outrun' are different causes with the same consequence and
    // the same remedy, so they share a message: in both, edits this person has
    // made are not on the server, this browser is going to keep trying, and
    // nothing has been thrown away — the shadow did not advance, so the same
    // difference is still there to send when the wait is over.
    Sync.failures++;
    if (Sync.failures >= FAILURES_BEFORE_NOTICE) setSticky(t('d.offline'));
    Sync.queued = true;
    Sync.timer = setTimeout(function () {
      Sync.timer = null;
      Sync.run();
    }, Math.min(RETRY_MAX_MS, RETRY_BASE_MS * Math.pow(2, Sync.failures - 1)));
  };

  // One pass, then look again: a reconcile changes what there is to send, and
  // a delete that lost a race has a new revision to try.
  //
  //   'done'   — nothing left to send
  //   'retry'  — no answer from the origin
  //   'outrun' — still not settled after MAX_PASSES rounds of reconciling
  function drain(pass) {
    var ops = planOps();
    if (!ops.length) return Promise.resolve('done');
    if (pass >= MAX_PASSES) return Promise.resolve('outrun');
    return runOps(ops, 0).then(function (ok) {
      return ok ? drain(pass + 1) : 'retry';
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
   * only be a second 404. Anything else that is not an answer is asked again
   * for as long as the page is open, on the write loop's backoff. It used to be
   * four more tries and then silence, which left a page opened during an
   * outage, or from the service worker's cache with no network, a local-only
   * planner for the rest of its life: Sync.push() does nothing until a plan
   * has arrived, so every edit stayed in this browser and nothing said so.
   */
  // One at a time. Two things ask for a connection on an ordinary signed-in
  // load — the page starting, and auth.js reporting the session a moment later
  // — and the second arrives while the first is still in flight. Letting both
  // through fetched the whole plan twice on every load, which is the largest
  // request this page makes, to a server that may be 300 ms away. It was also
  // a race with a worse ending: both answers reach adopt(), the second resets
  // the shadow while the first one's POSTs are landing, and a planner being
  // carried up to an empty database is carried up twice.
  var connecting = false;
  // The wait before the next try, while there is one: what connectNow() cuts
  // short.
  var connectTimer = null;
  // The status line is saying the origin is away, and connect() put it there.
  var awayNotice = false;

  // Whether the copy this page painted from came from a server. A planner that
  // has only ever lived in this browser is told nothing about a server: with
  // no database there is none, and the 404 that would say so cannot arrive
  // while the origin is away.
  function loadedFromServer() {
    return COLLECTIONS.some(function (c) {
      return (loaded[c.key] || []).some(function (r) { return r && SERVER_ID.test(String(r.id)); });
    });
  }

  // For the two answers with no plan behind them, and so nothing left to be
  // out of touch with.
  function noLongerAway() {
    if (!awayNotice) return;
    awayNotice = false;
    setSticky('');
  }

  function connect(attempt) {
    if (typeof fetch !== 'function' || typeof Promise !== 'function') return;
    // A retry (attempt > 0) is the connection already in progress, not a
    // second one.
    if (attempt === 0) {
      // Still one at a time. But whoever asks while this one is waiting out a
      // backoff has just heard from the origin (auth.js, with a sign-in), so
      // the wait has nothing left to wait for.
      if (connecting) { connectNow(); return; }
      connecting = true;
    }
    api('GET', '/plan').then(function (res) {
      if (res.status === 200 && res.body) {
        connecting = false;
        announceAPI(true);
        // The notice is handed to the write loop rather than taken down here.
        // adopt() sends what was typed meanwhile, and Sync.done() is what
        // knows when "everything is saved" has become true. Until then the
        // changes still have not reached the server.
        if (awayNotice) Sync.failures = FAILURES_BEFORE_NOTICE;
        awayNotice = false;
        adopt(res.body);
        return;
      }
      // 404 is final: with no database those paths are never registered.
      if (res.status === 404) { connecting = false; noLongerAway(); announceAPI(false); return; }
      // 401 is not "no API" — it is an API that wants a session. Falling back
      // to localStorage here would be the worst of both: edits would look
      // saved, live in this browser only, and never reach the plan everybody
      // else is reading. So hold, and connect for real once auth.js reports a
      // sign-in.
      if (res.status === 401) { connecting = false; noLongerAway(); announceAPI(true); return; }

      // No answer, or none that settles anything: a 5xx from a proxy with
      // nothing behind it, a captive portal's 200. Never the last try, and
      // `connecting` stays set so that a sign-in cannot start a second chain
      // beside this one. Said after the second failure in a row, as the write
      // loop does, because from here on edits exist in this browser only.
      awayNotice = attempt + 1 >= FAILURES_BEFORE_NOTICE && loadedFromServer();
      if (awayNotice) setSticky(t('d.offline'));
      connectTimer = setTimeout(function () {
        connectTimer = null;
        connect(attempt + 1);
      }, Math.min(RETRY_MAX_MS, RETRY_BASE_MS * Math.pow(2, attempt)));
    });
  }

  // Ask now instead of when the backoff comes round, and start the backoff
  // over. Only while a try is booked: with a request in flight the question is
  // already being asked, and with neither there is nothing to retry.
  function connectNow() {
    if (!connectTimer) return;
    clearTimeout(connectTimer);
    connectTimer = null;
    connect(1);
  }

  // The browser's own word that the network is back. It is a hint and not a
  // promise, which is why it only moves a try forward and never adds one.
  window.addEventListener('online', connectNow);

  /* Tell auth.js whether this deployment has an API at all.
   *
   * It has its own question to ask — whether anybody is signed in — and with
   * no database there is nobody to sign in as, so a second probe would be a
   * request whose answer is already known. Latched on window and announced as
   * an event, because auth.js may have finished loading either before or after
   * this resolves. */
  function announceAPI(available) {
    window.soiree = window.soiree || {};
    if (window.soiree.apiAvailable === available) return;
    window.soiree.apiAvailable = available;
    document.dispatchEvent(new CustomEvent('soiree:api', { detail: { available: available } }));
  }

  /* ---------- A session that ends while the page is open ----------
   * connect() above handles a 401 on the first request. This is every 401
   * after it: seven idle days, an admin disabling the account, a sign-out in
   * another tab. The page is mid-use when it happens, so three things are
   * already running that would each keep asking — the write loop, the resync
   * and the event stream — and each asks into the same refusal on a timer.
   *
   * That is not only wasted requests. The deployment this was written for has
   * a ban rule at the proxy that counts 401s per address, and one tab left
   * open over a week is enough of them to lock out a household.
   *
   * So: stop all three, say so, and keep every edit. Nothing here touches
   * `state` or the shadow, which is what makes signing back in free — the
   * difference is still there to send.
   */
  var sessionGone = false;

  // Stop asking. No event: this is also what an ordinary sign-out needs, and
  // auth.js already knows about that one because it caused it.
  function haltSync() {
    if (sessionGone) return false;
    sessionGone = true;
    closeLive();
    if (resyncTimer) { clearTimeout(resyncTimer); resyncTimer = null; }
    if (Sync.timer) { clearTimeout(Sync.timer); Sync.timer = null; }
    if (apiMode) setSticky(t('d.session'));
    return true;
  }

  // A request of ours was refused for want of a session. That is definitive —
  // the server has just said 401 to this browser's own cookie — so halt first,
  // and tell auth.js as a fact rather than as a question.
  function sessionLost() {
    if (haltSync()) doubtSession(true);
  }

  /* Ask auth.js to look again.
   *
   * It owns the question of who is signed in, and it answers on
   * `soiree:session` like any other change. Used directly by the one caller
   * that cannot know: an EventSource reports a refused connection as an error
   * with no status, so a closed stream may be a 401 or may be the subscriber
   * cap, and only one of those is a reason to stop.
   *
   * `definitive` is for the callers that do know. auth.js then acts on it at
   * once instead of asking the server to repeat itself. That second request
   * was a round trip nobody needed — 300 ms from the far side of the world —
   * and a second thing that could go missing: when it did, the status line
   * said "sign in again" and no sign-in screen ever opened. */
  function doubtSession(definitive) {
    document.dispatchEvent(new CustomEvent('soiree:session-check', {
      detail: { definitive: !!definitive }
    }));
  }

  /* ---------- Signing out ----------
   * The copy of the plan this browser keeps is what lets the page paint at
   * once and work offline. It is also a ledger of people's names against
   * money, in localStorage, on whatever computer somebody happened to use — so
   * signing out takes it away again.
   *
   * Only a sign-out somebody asked for. A session that merely ENDED leaves
   * everything where it is: that person is coming back, their unsent edits are
   * in that copy, and "what you changed is safe in this browser" is a promise
   * the sign-in screen has just made them.
   *
   * Which is also the hazard here, from the other side: signing out discards
   * whatever has not reached the server. So auth.js asks first, through
   * beforeSignOut() — the debounce is flushed, the write loop gets a few
   * seconds to finish, and what comes back is the number of changes that still
   * exist nowhere else. Anything above zero is put to the person as a
   * question rather than decided for them.
   */
  var forgotten = false;

  function unsentCount() {
    if (!apiMode || !shadow) return 0;
    // Waiting to be sent, plus refused by the server and parked: both are
    // edits that exist in this browser and nowhere else. A park whose row is
    // in neither the page nor the shadow is neither of those: nothing can
    // produce that op again, so nothing will ever clear the key, and counting
    // it asks the person about a change that exists nowhere at all.
    var parked = Object.keys(blocked).filter(function (key) {
      var c = BY_KEY[key.slice(0, key.indexOf(':'))];
      if (!c || c.singleton) return true;
      var id = key.slice(key.indexOf(':') + 1, key.lastIndexOf(':'));
      return !!findRow(state[c.key], id) || !!shadow[c.key][id];
    });
    return planOps().length + parked.length;
  }

  function beforeSignOut() {
    flushSave();
    if (!apiMode || sessionGone) return Promise.resolve(unsentCount());
    return new Promise(function (resolve) {
      var deadline = Date.now() + 4000;
      (function wait() {
        // Settled is either "nothing left to send" or "gave up for now": a
        // retry timer means the origin is not answering, and waiting out its
        // backoff would hold somebody at the door of a shared computer.
        var settled = !Sync.running && !saveTimer && (!Sync.queued || Sync.timer);
        if (settled || Date.now() > deadline) { resolve(unsentCount()); return; }
        setTimeout(wait, 100);
      })();
    });
  }

  function forgetPlan() {
    if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
    Store.clear();

    // Column widths are how this person likes their table and say nothing
    // about anybody. Row heights are keyed by the ids of budget lines, so they
    // go with the lines.
    var widths = state.colWidths;
    state = emptyState();
    state.colWidths = widths;

    // Back to a page that has never met the server. The next sign-in then
    // takes connect() and adopt() — the plan as the server has it — rather
    // than a merge against a shadow of something this browser no longer holds.
    shadow = null;
    apiMode = false;
    dirty = false;
    hadSavedCopy = false;
    blocked = {};
    idMap = {};
    createKeys = {};
    pendingRefresh = {};
    loaded = JSON.parse(JSON.stringify(state));
    Sync.queued = false;
    Sync.failures = 0;
    forgotten = true;
    // File names are as much the plan as anything in it.
    attachments = [];
    uploads = [];

    setSticky('');
    renderAll();
  }

  window.soiree = window.soiree || {};
  window.soiree.beforeSignOut = beforeSignOut;

  // Another tab signed out. This one still holds the plan in memory and would
  // write it straight back the next time it saved or was closed, so it lets go
  // of it too, and asks auth.js to look at the session — which is gone.
  window.addEventListener('storage', function (e) {
    if (!e || e.key !== STORAGE_KEY || e.newValue !== null || forgotten) return;
    if (apiMode) haltSync();
    forgetPlan();
    doubtSession();
  });

  // The digest is pushed to active admins and to nobody else (the store's
  // NotifiablePushSubscriptions), so an offer made to anybody else is an offer
  // of something that will never arrive.
  var sessionRole = '';

  document.addEventListener('soiree:session', function (e) {
    var signedIn = !!(e && e.detail && e.detail.signedIn);
    sessionRole = signedIn ? String(e.detail.role || '') : '';
    if (!signedIn) {
      // Only once there is something to halt. Before the first plan arrives
      // this is the ordinary "nobody is signed in yet" and connect() is
      // already holding.
      if (apiMode) haltSync();
      if (e && e.detail && e.detail.reason === 'signout') forgetPlan();
      return;
    }

    // Back. Rows parked by a refusal are released, because who is asking is
    // exactly what changed: an editor signing in where a viewer was is a
    // different answer to the same 403. Anything still refused is parked
    // again on the pass that follows, with one notice rather than none.
    sessionGone = false;
    blocked = {};
    setSticky('');
    // Signing out is what raised this, so signing in is what lowers it. It
    // exists to stop a signed-out page writing its emptied planner back; a
    // signed-in page has a plan again, and one worth keeping.
    forgotten = false;

    // Never signed in on this page before: the ordinary first load.
    if (!apiMode) { connect(0); return; }

    // Signed in AGAIN, on a page that has been holding a plan — and for as
    // long as it was signed out, other people kept editing. That makes this a
    // merge and emphatically not adopt(), for the reason applyPlan() gives:
    // adopt() lets this browser's copy win. `dirty` has been true since the
    // first keystroke of this page's life, so adopt() would diff a copy that
    // may be a week stale against a fresh shadow and PATCH it over everyone —
    // at the current revision, so without so much as a 409 — and POST back
    // every row somebody deleted in the meantime.
    //
    // The shadow this page still holds is the version both sides started
    // from, which is exactly what the three-way merge wants: a field edited
    // here while signed out is kept and sent, everything else is theirs.
    openLive();
    scheduleResync();
  });

  /* Take the plan the origin sent.
   *
   * Normally the server is simply right and what is on screen is a cached
   * copy of it. Two cases are not normal, and in both the answer is to keep
   * what this browser is holding and send it up instead:
   *
   *   1. Something was typed between the cached copy painting and the plan
   *      arriving. Discarding those keystrokes because a request happened to
   *      land after them is not a defensible reason to lose them.
   *   2. This browser is holding a planner somebody built, and the server has
   *      none. That is the database being added to a deployment that was
   *      running without one — and replacing that planner with an empty plan
   *      destroys the only copy of it, on the first load, with no warning.
   *
   * The second is deliberately narrow: only a planner that was actually saved
   * (not a blank one, and not generated demo data) and only against a plan
   * with nothing in it at all. The cost of getting it wrong is two browsers
   * each seeding the same empty database and producing every row twice, which
   * is visible and can be fixed by hand. The cost of not doing it is silent
   * destruction of somebody's planner, which cannot.
   */
  function adopt(plan) {
    takeAttachments(plan);

    // What the last page left unsent, if it left anything. `dirty` is a fact
    // about this page alone — the first keystroke since it painted sets it —
    // and an edit that never reached the server is exactly as unsent after a
    // reload as it was before one. Asked before the base is replaced, because
    // the base is what those edits are a difference from.
    var base = shadow;
    if (base && planOps().length) dirty = true;

    shadow = shadowFromPlan(plan);
    apiMode = true;

    var planIsEmpty = COLLECTIONS.every(function (c) {
      return Object.keys(shadow[c.key]).length === 0;
    });
    var holdingRows = COLLECTIONS.some(function (c) {
      return (state[c.key] || []).length > 0;
    });

    // Edited here, and the server has a plan of its own. "Something was typed
    // between the cached copy painting and the plan arriving" was written for
    // a gap of a few hundred milliseconds. Behind a sign-in it is as long as
    // somebody takes to notice they are signed out — on a tab reopened after a
    // week, showing a week-old copy, with nothing stopping them editing it.
    // Letting this browser's copy win then does not keep a keystroke; it
    // PATCHes every stale field over a week of other people's work, at the
    // current revision and so with no 409, and POSTs back every row they
    // deleted.
    //
    // So this is a merge as well, against the base the last page persisted:
    // a field that differs from it was edited here and is ours; everything
    // else is theirs. Only rows the server once knew are in it — a row under
    // an id this browser minted has never been sent, and applyPlan() must read
    // its absence from the plan as "not created yet", not as "somebody deleted
    // it".
    //
    // With no base there is the copy as it was loaded, which is the same
    // reasoning one degree weaker: it stands in for the base, and it is right
    // only about the edits made since this page painted. The unsent edits of
    // an earlier page are in it too, so a merge against it reads them as
    // "unchanged here" and takes the server's values over them. That is what
    // persisting the base is for.
    if (dirty && !planIsEmpty) {
      shadow = base || shadowFromLoaded();
      applyPlan(plan);
      renderAll();
      openLive();
      pushRefresh();
      return;
    }

    if (dirty || (hadSavedCopy && holdingRows && planIsEmpty)) {
      // The rows the server has that this browser has not are taken rather
      // than deleted: in this window "absent here" means "not fetched yet",
      // not "removed". That union is what makes the delete rule in planOps
      // true from here on.
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
    // Written before anything is sent, as everything here is: this is the
    // first moment the page has a base at all, and the first load is also the
    // one a tab is most likely not to survive.
    Store.keep();
    Sync.push();
    openLive();
    pushRefresh();
  }

  /* ==================================================================
   * LIVE SYNC — GET /api/v1/events
   * ==================================================================
   * Two people have shared one ledger since the API was wired up, and neither
   * saw the other until they reloaded. That is how a venue gets booked twice.
   *
   * The stream carries identifiers, never rows — "budget_items <id> is now at
   * revision 4" — so every event resolves to the same act: re-read GET /plan
   * and merge it. The interesting parts are the merge, and knowing when not to
   * read. Four rules from the endpoint's contract, each counter-intuitive and
   * each load-bearing:
   *
   *   - `resync` means "your copy is stale". It arrives on every connect and
   *     again whenever the server re-establishes its database connection,
   *     because changes happened in that gap that nobody was told about. There
   *     is no replay and no Last-Event-ID: this is the whole of what a client
   *     does about missed events.
   *   - On create and update, ignore a revision already held. That is what
   *     suppresses the echo of this browser's own write — the server does not
   *     filter it out, and re-reading the plan to be told what we just wrote is
   *     a round trip for nothing.
   *   - On delete, never compare revisions. A delete announces the row's final
   *     revision, which is the one already held, so a `<=` test would discard
   *     the one event that must never be discarded and the row would stay.
   *   - Deleting a phase, a sponsor or a budget line is why every event ends in
   *     a whole-plan read rather than a surgical one. Those cascade — a line's
   *     phase and a programme entry's line go to NULL, attributions are removed
   *     — without bumping the affected rows' revisions, so none of it is
   *     announced, and a stale copy here would sail through the revision check
   *     on the next PATCH and write the dangling reference back.
   * ================================================================== */

  var live = null;        // the EventSource, once there is a database behind us
  var liveWait = 0;       // backoff for a stream that was refused outright
  var liveTimer = null;   // the pending reopen, so that it can be called off

  // Reconnect is normally the browser's job — the server sends `retry: 3000`.
  // This is only for the case the browser will not retry: see onerror.
  var LIVE_RETRY_MS = 5000;

  function closeLive() {
    if (liveTimer) { clearTimeout(liveTimer); liveTimer = null; }
    if (live) { live.close(); live = null; }
    liveWait = 0;
  }

  function openLive() {
    if (!apiMode || sessionGone || live || typeof EventSource !== 'function') return;
    try { live = new EventSource(API_BASE + '/events'); } catch (e) { live = null; return; }

    // Named events. Nothing is ever sent as a default `message`, so onmessage
    // would sit there receiving nothing and the page would look connected and
    // be deaf.
    live.addEventListener('resync', function () { scheduleResync(); });
    live.addEventListener('change', function (e) { onChange(e.data); });
    live.onopen = function () { liveWait = 0; };
    live.onerror = function () {
      // CONNECTING means the browser is already reconnecting on the interval
      // the server asked for, and anything done here would be a second attempt
      // racing the first. CLOSED is the case that needs us: a stream refused
      // with a status — a 503 when the subscriber cap is reached — puts an
      // EventSource into CLOSED permanently, and it never comes back on its
      // own. The Retry-After that came with it is not readable from here, so
      // the wait is ours, doubling and jittered: a full cap means everyone hit
      // it at once, and reconnecting in lockstep is how it stays hit.
      if (!live || live.readyState !== EventSource.CLOSED) return;
      live.close();
      live = null;
      liveWait = Math.min(RETRY_MAX_MS, liveWait ? liveWait * 2 : LIVE_RETRY_MS);
      liveTimer = setTimeout(function () { liveTimer = null; openLive(); },
        liveWait / 2 + Math.random() * liveWait);

      // The other reason a stream is refused outright is a session that has
      // ended, and from in here the two look identical. The reopen above stays
      // booked, because if this was the cap it is still the right thing to do;
      // if auth.js finds there is no session, haltSync() calls it off before
      // it fires. One question asked, rather than a 401 every thirty seconds
      // for as long as the tab is open.
      doubtSession();
    };
  }

  function onChange(raw) {
    var n;
    try { n = JSON.parse(raw); } catch (e) { return; }
    if (!n || !n.entity || !shadow) return;

    // A file has no revision to compare and is not one of the collections the
    // merge knows: somebody added or removed one, so read the list again.
    if (n.entity === 'attachments') { scheduleResync(); return; }

    var c = BY_ENTITY[n.entity];
    if (n.action === 'delete') {
      // No revision test, deliberately. `phases` is not drawn here and is acted
      // on anyway, because deleting one silently rewrites budget lines; a
      // programme entry is the one deletion with nothing behind it.
      if (!c && n.entity !== 'phases') return;
    } else {
      if (!c) return;   // phases and programme entries: nothing here is drawn from them
      var held = c.singleton ? shadow.settings : shadow[c.key][n.id];
      if (held && n.revision != null && Number(n.revision) <= held.revision) return;
    }
    scheduleResync();
  }

  /* ---------- Re-reading the plan ----------
   * Coalesced, and never on top of this browser's own writing.
   *
   * The wait is long enough to swallow the two resyncs a connect sends and the
   * burst a cascading delete makes, and long enough that a keystroke which has
   * only just landed has started the save debounce below: a read that overtakes
   * the write it raced settles a conflict silently, where the write would have
   * reported it.
   */
  var RESYNC_MS = 400;
  var resyncTimer = null;
  var resyncing = false;
  var resyncAgain = false;
  var resyncWait = 0;

  function scheduleResync() {
    if (!apiMode) return;
    resyncAgain = true;
    armResync(RESYNC_MS);
  }

  function armResync(ms) {
    if (sessionGone || resyncTimer || resyncing) return;
    resyncTimer = setTimeout(runResync, ms);
  }

  function runResync() {
    resyncTimer = null;

    // Not while this browser has a write in flight, waiting to be retried, or
    // still sitting in the save debounce. A plan read around a POST that has
    // committed but not answered carries a row under an id this page has never
    // seen, and merging it would put a second copy of it on screen. A plan
    // merged on top of an edit that has not been sent resolves a conflict
    // silently and in the wrong direction — where letting the write go first
    // produces the 409 the merge path already handles, and says out loud.
    if (Sync.running || Sync.timer || saveTimer) { armResync(RESYNC_MS); return; }

    resyncing = true;
    resyncAgain = false;
    var mark = passes;
    api('GET', '/plan').then(function (res) {
      resyncing = false;
      // The same three conditions again, because the answer is what gets
      // merged and a write can have started while it was on its way.
      if (res.status === 401) {
        // An answer, not an outage, and asking again will not change it.
        sessionLost();
        return;
      }
      if (res.status === 200 && res.body && passes === mark && !Sync.running && !saveTimer) {
        resyncWait = 0;
        applyPlan(res.body);
      } else {
        // Overtaken by a write of our own, or no answer at all. Both are worth
        // another go — a stream that is still up will not repeat itself — but
        // on a widening wait, so an origin that is down is asked about once in
        // a while rather than constantly.
        resyncAgain = true;
        resyncWait = Math.min(RETRY_MAX_MS, resyncWait ? resyncWait * 2 : RETRY_BASE_MS);
      }
      if (resyncAgain) armResync(resyncWait || RESYNC_MS);
    });
  }

  /* Merge a plan that arrived while this page was already running.
   *
   * Emphatically not adopt(). That one is allowed to decide this browser's
   * copy wins and seed an empty database from it, which is right on a first
   * load and catastrophic here: somebody else removing the last row would make
   * this browser put its entire planner back.
   *
   * It is the same three-way merge reconcile() does on a 409, applied to every
   * row at once, against the shadow — the version both sides started from:
   *
   *   field changed here since the server confirmed it -> ours. It is an edit
   *     somebody is in the middle of making, and it goes again on the next
   *     pass. What is on this screen is never overwritten, focused or not.
   *   field unchanged here -> theirs, which is simply newer.
   *   both changed it -> a list of ids is a set and keeps both. Anything else
   *     keeps ours and says nothing: this is a plan arriving rather than a
   *     write being answered, and it runs over every row.
   *
   * Rows work the same way, and the shadow is what tells the two halves of
   * each ambiguity apart:
   *
   *   here, not on the server -> in the shadow? somebody deleted it: drop it,
   *                              edited here since or not. There is no row
   *                              left to write that edit to, and posting it
   *                              back would undo a removal somebody meant.
   *                              not in the shadow? our own create, unsent:
   *                              keep it, and let the next pass POST it.
   *   on the server, not here -> in the shadow? our own delete, unsent: keep
   *                              it deleted, and keep the shadow entry so the
   *                              DELETE still goes, at the revision that now
   *                              stands. not in the shadow? somebody added it.
   */
  function applyPlan(plan) {
    takeAttachments(plan);
    var touched = {};

    COLLECTIONS.forEach(function (c) {
      var was = shadow[c.key];
      var now = {};
      var here = {};

      (plan[c.key] || []).forEach(function (w) {
        if (!w || !w.id) return;
        here[w.id] = true;
        var entry = { row: rowFromWire(c, w), revision: Number(w.revision) || 0 };
        now[w.id] = entry;

        var mine = findRow(state[c.key], w.id);
        if (!mine) {
          if (was[w.id]) return;                     // removed here, not yet sent
          state[c.key].push(cloneRow(c, entry.row)); // somebody else's new row
          touched[c.key] = c;
          return;
        }
        var base = was[w.id] ? was[w.id].row : entry.row;
        c.fields.forEach(function (f) {
          var ours = f.canon(mine);
          var agreed = f.canon(base);
          var theirs = f.canon(entry.row);
          if (ours === agreed) {
            if (ours === theirs) return;             // nobody changed it
            mine[f.name] = f.copy(entry.row[f.name]);
            touched[c.key] = c;
            return;
          }
          // Edited here: ours wins, except where the field is a set and can
          // hold both. Nothing is said about it: this is a plan arriving
          // rather than a write being answered, and it runs over every row.
          if (theirs === agreed || theirs === ours || !f.merge) return;
          mine[f.name] = f.merge(base[f.name], mine[f.name], entry.row[f.name]);
          touched[c.key] = c;
        });
      });

      state[c.key] = (state[c.key] || []).filter(function (r) {
        if (!r || !r.id || here[r.id] || !was[r.id]) return true;
        touched[c.key] = c;
        return false;
      });
      shadow[c.key] = now;
    });

    var wire = plan.settings || {};
    var entry = { row: rowFromWire(SETTINGS, wire), revision: Number(wire.revision) || 0 };
    var base = shadow.settings.row;
    SETTINGS.fields.forEach(function (f) {
      var ours = f.canon(state);
      if (ours !== f.canon(base) || ours === f.canon(entry.row)) return;
      state[f.name] = f.copy(entry.row[f.name]);
      touched.settings = SETTINGS;
    });
    shadow.settings = entry;

    // Before the early return below: a merge can leave work to do even when
    // nothing on screen moved — a delete of ours that the plan shows already
    // gone settles here, and a pending one keeps its shadow entry and still has
    // to go out. The base has moved either way, so the state it is now a base
    // for is written with it.
    Store.keep();
    Sync.push();

    var keys = Object.keys(touched);
    if (!keys.length) return;

    // The figures belong to no caret, so they are always current. The editable
    // containers go through scheduleRefresh, which holds a rebuild back while
    // somebody is typing inside one — rebuilding a table under a cursor moves
    // it, drops the selection and truncates a half-typed number, which is the
    // one thing a remote change must never do to a local edit.
    keys.forEach(function (k) { scheduleRefresh(touched[k]); });
    renderBudgetTotals();
    renderOverview();
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
    // Signed out and nothing typed since: there is nothing to keep, and
    // writing the empty planner back would only put the key there again.
    if (forgotten) return;
    Store.write(state);
  }
  function save() {
    forgotten = false;
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
    fitBudgetText();
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
    //
    // And "today" is today where the event is, not where the reader is or in
    // UTC: shifting the clock by the event's offset and reading the UTC fields
    // gives the calendar date there. See EVENT above.
    var there = new Date(Date.now() + EVENT.offsetMinutes * 60000);
    var today = Date.UTC(there.getUTCFullYear(), there.getUTCMonth(), there.getUTCDate());
    return Math.round((EVENT.day - today) / 86400000);
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
    renderRunUp(diffDays);
  }

  /* ---------- The run-up ----------
   * A scale from today to the day, with every open task that has a due date
   * pinned where it falls.
   *
   * Positions are reckoned in whole days at the event's own offset, exactly as
   * daysLeft() does, so a task due "tomorrow" sits one day along for every
   * reader, and the scale is linear from end to end. A task that is already
   * late is pinned at today, in the alarm colour, and counted: a scale that
   * quietly dropped what was overdue would flatter precisely the plan that
   * most needs looking at.
   *
   * Real plans are front-loaded - six things in the next month, two in the
   * spring - and a linear scale puts those six in a fifth of the line. Three
   * things keep that legible without bending time to do it:
   *
   *   - Names sit in lanes. A name that would run into its neighbour goes a
   *     lane higher instead of being dropped or cut, tied to its date by a
   *     leader. The soonest ends up on top, so a crowded month reads as a
   *     flight of steps down to the line, in the order the work is due. Near
   *     the day a name runs leftward from its leader rather than off the end.
   *   - Marks that would touch are merged, and say so: one bead as long as the
   *     dates it covers, with the count in it. Two dots a pixel apart read as
   *     one dot, which is a wrong picture; "2" is a right one.
   *   - Every mark is a button that lists what is due there. A name that did
   *     not get a lane, or is inside a bead, is one press away rather than in
   *     a hover title a phone never shows.
   *
   * What was weighed and left out: a scale that gives the near term more room
   * (it misdraws distance in the one picture whose job is distance, and does
   * nothing for two tasks due the same day), and a cap that simply hides the
   * crowd behind a count (it hides exactly the month people came to see).
   */
  var RUNUP = {
    dot: 4,     // the radius of a lone mark
    bead: 8,    // and of a counted one
    lamp: 9,    // how far today's lamp reaches along the line
    air: 3,     // the least paper between two marks before they are merged
    base: 18,   // from the foot of the scale to the foot of the lowest name
    pitch: 18,  // one lane
    name: 15,   // the height of a name: 12px at a line-height of 1.25
    clear: 8    // the least paper between a name and another mark's leader
  };
  var runupDrawn = '';
  var runupWatched = false;
  var runupFormats = {};

  function runupDate(kind, ms) {
    var options = {
      day: { day: 'numeric', month: 'short', timeZone: 'UTC' },
      full: { weekday: 'short', day: 'numeric', month: 'short', timeZone: 'UTC' },
      month: { month: 'short', timeZone: 'UTC' }
    }[kind];
    try {
      if (!runupFormats[kind]) runupFormats[kind] = new Intl.DateTimeFormat(LOC, options);
      return runupFormats[kind].format(new Date(ms));
    } catch (e) {
      return new Date(ms).toISOString().slice(kind === 'month' ? 5 : 0, kind === 'month' ? 7 : 10);
    }
  }

  function renderRunUp(diffDays) {
    var scale = document.getElementById('runupScale');
    var marks = document.getElementById('runupMarks');
    var from = document.getElementById('runupFrom');
    if (!scale || !marks) return;

    if (!runupWatched) {
      runupWatched = true;
      marks.addEventListener('keydown', runupKeys);
      // The names are laid out in pixels, so the scale is drawn again whenever
      // its width changes - a window resized, the display face arriving and
      // narrowing the count beside it, or the planner appearing from behind a
      // sign-in, before which there was no width to lay anything out in.
      // On the next frame, not in the callback: drawing can change the height
      // of the very element being observed, which a browser reports as a loop.
      if (window.ResizeObserver) {
        var queued = false;
        new ResizeObserver(function () {
          if (queued) return;
          queued = true;
          requestAnimationFrame(function () { queued = false; renderRunUp(daysLeft()); });
        }).observe(scale);
      }
    }

    var usable = EVENT && diffDays !== null && diffDays > 0 && !isArchived();
    document.getElementById('runup').classList.toggle('no-scale', !usable);
    if (from) from.textContent = t('ru.today');
    if (!usable) {
      runupClear(scale, marks);
      if (from) from.classList.remove('late');
      return;
    }

    var there = new Date(Date.now() + EVENT.offsetMinutes * 60000);
    var today = Date.UTC(there.getUTCFullYear(), there.getUTCMonth(), there.getUTCDate());
    var span = EVENT.day - today;

    var late = [];
    var ahead = [];
    state.tasks.forEach(function (task) {
      if (task.status === 'done' || !task.due) return;
      var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(task.due));
      if (!m) return;
      var due = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
      (due < today ? late : ahead).push({ task: task, due: due, at: Math.max(0, Math.min(1, (due - today) / span)) });
    });
    late.sort(function (a, b) { return a.due - b.due; });
    ahead.sort(function (a, b) { return a.due - b.due; });

    if (late.length && from) from.textContent = t('ru.today') + ' \u2013 ' + t('ru.overdue', { n: late.length });
    if (from) from.classList.toggle('late', late.length > 0);

    // Hidden, so there is nothing to measure; the observer calls back when
    // there is. Without one, guess, as this always did.
    var width = scale.clientWidth || (window.ResizeObserver ? 0 : 800);
    if (!width) { runupClear(scale, marks); return; }

    // Drawn already, and nothing it shows has changed: leave it alone. This is
    // called on every render of the overview and every resize event, and
    // rebuilding the marks would take the focus and any open list with them.
    var drawn = [LANG, width, today, EVENT.day].concat(late.concat(ahead).map(function (p) {
      return [p.task.id, p.due, p.task.name, p.task.owner].join('\u001f');
    })).join('\u001e');
    if (drawn === runupDrawn) return;
    runupDrawn = drawn;

    var held = marks.contains(document.activeElement) ? document.activeElement.getAttribute('data-key') : null;
    if (openBtn && marks.contains(openBtn)) closePop(false);
    marks.textContent = '';

    var groups = runupGroups(late, ahead, width);
    groups.forEach(function (g, i) {
      g.el = runupMark(g, i === 0);
      marks.appendChild(g.el);
    });
    var lanes = runupNames(groups, width);
    // A mark with no name beside it says what it is when pointed at.
    groups.forEach(function (g) {
      if (!g.el.querySelector('.runup-label')) g.el.title = g.el.getAttribute('aria-label');
    });
    scale.style.height = Math.max(44, RUNUP.base + lanes * RUNUP.pitch) + 'px';
    runupMonths(today, span, width);

    if (groups.length) {
      marks.setAttribute('role', 'toolbar');
      marks.setAttribute('aria-label', t('ru.aria'));
    } else {
      marks.removeAttribute('role');
      marks.removeAttribute('aria-label');
    }
    if (held) {
      var again = marks.querySelector('[data-key="' + held.replace(/["\\]/g, '\\$&') + '"]');
      if (again) { runupRove(marks, again); again.focus(); }
    }
  }

  function runupClear(scale, marks) {
    if (openBtn && marks.contains(openBtn)) closePop(false);
    runupDrawn = '';
    marks.textContent = '';
    marks.removeAttribute('role');
    marks.removeAttribute('aria-label');
    scale.style.height = '';
    ['runupTicks', 'runupMonths'].forEach(function (id) {
      var el = document.getElementById(id);
      if (el) el.textContent = '';
    });
  }

  /* Which tasks share a mark. Everything late is one mark, at today. Ahead of
   * today a task gets a mark of its own unless that would touch the one before
   * it, and then the two are one bead from the first date to the last. Merging
   * makes a mark longer and so can bring the next one into reach, which is why
   * this runs until nothing moves. */
  function runupGroups(late, ahead, width) {
    var groups = [];
    ahead.forEach(function (p) {
      var x = p.at * width;
      var last = groups[groups.length - 1];
      if (last && last.x1 === x) last.tasks.push(p);
      else groups.push({ x0: x, x1: x, tasks: [p] });
    });
    function radius(g) { return g.tasks.length > 1 ? RUNUP.bead : RUNUP.dot; }
    for (var i = 1; i < groups.length;) {
      var a = groups[i - 1];
      var b = groups[i];
      if (b.x0 - radius(b) - runupEnd(a) < RUNUP.air) {
        a.x1 = b.x1;
        a.tasks = a.tasks.concat(b.tasks);
        groups.splice(i, 1);
        if (i > 1) i -= 1;   // the longer bead may now reach the one before it
      } else {
        i += 1;
      }
    }
    if (late.length) groups.unshift({ x0: 0, x1: 0, tasks: late, late: true });
    return groups;
  }

  // Today's lamp is never covered, so a bead that starts under it keeps its
  // count clear of it, and is drawn that much longer if it has to be.
  function runupTuck(g) {
    return g.tasks.length > 1 ? Math.max(0, RUNUP.lamp - (g.x0 - RUNUP.bead)) : 0;
  }
  // Where a mark's drawing ends, which is what the next one must stay clear of.
  function runupEnd(g) {
    if (g.tasks.length < 2) return g.x1 + RUNUP.dot;
    return g.x0 - RUNUP.bead + Math.max(g.x1 - g.x0, runupTuck(g)) + RUNUP.bead * 2;
  }

  function runupMark(g, first) {
    var many = g.tasks.length > 1;
    var head = g.tasks[0];
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'runup-mark' + (g.late ? ' late' : many ? ' many' : '');
    btn.style.left = (head.at * 100).toFixed(2) + '%';
    btn.tabIndex = first ? 0 : -1;
    btn.setAttribute('data-key', (g.late ? 'late' : 'due') + ':' + head.task.id);
    btn.setAttribute('aria-expanded', 'false');

    var what;
    if (g.late) {
      what = t('ru.overdue', { n: g.tasks.length });
    } else if (!many) {
      what = runupDate('day', head.due) + ', ' + (head.task.name || t('un.untitled'));
    } else {
      var lastDue = g.tasks[g.tasks.length - 1].due;
      what = lastDue === head.due
        ? t('ru.sameday', { n: g.tasks.length, a: runupDate('day', head.due) })
        : t('ru.many', { n: g.tasks.length, a: runupDate('day', head.due), b: runupDate('day', lastDue) });
    }
    btn.setAttribute('aria-label', what);

    if (many && !g.late) {
      var tuck = runupTuck(g);
      btn.style.width = Math.round(Math.max(g.x1 - g.x0, tuck) + RUNUP.bead * 2) + 'px';
      if (tuck) btn.style.paddingLeft = Math.round(tuck) + 'px';
      var count = document.createElement('span');
      count.className = 'runup-count';
      count.textContent = String(g.tasks.length);
      btn.appendChild(count);
    }
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      openRunUpList(btn, g);
    });
    btn.addEventListener('focus', function () { runupRove(btn.parentNode, btn); });
    return btn;
  }

  /* Names, in lanes. Returns how many lanes were used.
   *
   * Candidates are taken soonest first and each is kept only if the whole set
   * still has a layout, so when something has to give it is the later name. A
   * layout is an order of heights: a name that passes over another mark's
   * leader has to be above the end of it, and two names that overlap have to
   * be in different lanes. Those "above" relations are relaxed into the lowest
   * lanes that satisfy them. Two names that each pass over the other's leader
   * have no such order, and the newcomer is shortened until it stops short of
   * the leader instead - at a word, never through one.
   */
  function runupNames(groups, width) {
    var phone = width < 480;
    var mostNames = phone ? 5 : 8;
    var mostLanes = phone ? 5 : 6;
    var least = phone ? 96 : 120;
    var edge = 7;                       // the scale's own side margin is free
    var kept = [];

    groups.forEach(function (g, i) {
      if (g.late || kept.length >= mostNames) return;
      var head = g.tasks[0];
      var label = document.createElement('span');
      label.className = 'runup-label';
      var when = document.createElement('span');
      when.className = 'runup-when';
      when.textContent = runupDate('day', head.due);
      var name = document.createElement('span');
      name.className = 'runup-name';
      name.textContent = head.task.name || t('un.untitled');
      label.appendChild(when);
      label.appendChild(document.createTextNode(' '));
      label.appendChild(name);
      // The next thing due is the one name set in ink; measured as set.
      if (!kept.length && (i === 0 || groups[i - 1].late)) g.el.classList.add('next');
      g.el.appendChild(label);
      var natural = Math.ceil(label.getBoundingClientRect().width) + 1;

      var tries = [];
      [1, -1].forEach(function (dir) {
        var room = dir > 0 ? width + edge - g.x0 : g.x0 + edge;
        kept.forEach(function (k) {
          var between = (k.g.x0 - g.x0) * dir;
          // Would pass over k's leader while k passes over this one.
          if (between > 0 && k.lo - RUNUP.clear < g.x0 && g.x0 < k.hi + RUNUP.clear) {
            room = Math.min(room, between - RUNUP.clear);
          }
        });
        var w = Math.min(natural, Math.floor(room));
        if (w >= Math.min(natural, least)) tries.push({ dir: dir, w: w, whole: w >= natural });
      });
      tries.sort(function (a, b) { return (b.whole - a.whole) || (b.w - a.w) || (b.dir - a.dir); });

      for (var n = 0; n < tries.length; n++) {
        var c = {
          g: g, label: label, name: name, dir: tries[n].dir, w: tries[n].w, whole: tries[n].whole,
          lo: tries[n].dir > 0 ? g.x0 : g.x0 - tries[n].w,
          hi: tries[n].dir > 0 ? g.x0 + tries[n].w : g.x0
        };
        if (runupLanes(kept.concat([c]), mostLanes)) { kept.push(c); return; }
      }
      runupLanes(kept, mostLanes);      // put back the lanes the attempt moved
      g.el.classList.remove('next');
      label.remove();
    });

    var lanes = 0;
    kept.forEach(function (c) {
      var many = c.g.tasks.length > 1;
      var foot = many ? -1 : 3;                                     // the mark's own foot above the scale's
      var top = many ? RUNUP.bead * 2 - 1 : 3 + RUNUP.dot * 2;      // and its top
      var bottom = RUNUP.base + c.lane * RUNUP.pitch;
      if (c.dir < 0) c.g.el.classList.add('flip');
      c.label.style.bottom = (bottom - foot) + 'px';
      var leader = document.createElement('span');
      leader.className = 'runup-leader';
      leader.style.height = (bottom + RUNUP.name - top) + 'px';
      c.g.el.insertBefore(leader, c.label);
      if (!c.whole) runupShorten(c.label, c.name, c.w);
      lanes = Math.max(lanes, c.lane + 1);
    });
    return lanes;
  }

  // Sets .lane on every name, lowest lanes first. False when there is no
  // layout: a cycle, or more lanes than the scale is allowed to grow.
  function runupLanes(names, most) {
    var over = [];
    for (var i = 0; i < names.length; i++) {
      for (var j = 0; j < names.length; j++) {
        if (i === j) continue;
        var a = names[i];
        var b = names[j];
        var passes = a.lo - RUNUP.clear < b.g.x0 && b.g.x0 < a.hi + RUNUP.clear;
        var back = b.lo - RUNUP.clear < a.g.x0 && a.g.x0 < b.hi + RUNUP.clear;
        var overlap = a.lo < b.hi + RUNUP.clear && b.lo < a.hi + RUNUP.clear;
        // Overlapping with neither over the other's leader: the sooner on top.
        if (passes || (overlap && !back && i < j)) over.push([a, b]);
      }
    }
    names.forEach(function (n) { n.lane = 0; });
    for (var pass = 0; pass <= names.length; pass++) {
      var moved = false;
      over.forEach(function (pair) {
        if (pair[0].lane <= pair[1].lane) { pair[0].lane = pair[1].lane + 1; moved = true; }
      });
      if (!moved) break;
      if (pass === names.length) return false;
    }
    return names.every(function (n) { return n.lane < most; });
  }

  // Shortened at a word. One word too long for the room is left to the
  // stylesheet's ellipsis, which is the only case a name is cut through.
  function runupShorten(label, name, w) {
    label.style.maxWidth = w + 'px';
    var words = name.textContent.split(/\s+/);
    // A flipped name is a flex item and does the overflowing itself.
    function spills() {
      return label.scrollWidth > label.clientWidth || (name.clientWidth > 0 && name.scrollWidth > name.clientWidth);
    }
    while (words.length > 1 && spills()) {
      words.pop();
      name.textContent = words.join(' ').replace(/[\s,;:.\u2013\u2014-]+$/, '') + '\u2026';
    }
  }

  /* The months, so that distance along the line reads as time: a tick at each
   * first of the month, named under the line as often as there is room for,
   * and not where "today" or the date already stands. January carries the
   * year, since it is the one month that says which year the others are in. */
  function runupMonths(today, span, width) {
    var ticks = document.getElementById('runupTicks');
    var names = document.getElementById('runupMonths');
    if (!ticks || !names) return;
    ticks.textContent = '';
    names.textContent = '';

    var start = new Date(today);
    var firsts = [];
    for (var y = start.getUTCFullYear(), m = start.getUTCMonth() + 1; ; m++) {
      var first = Date.UTC(y, m, 1);
      if (first >= today + span) break;
      firsts.push(first);
      if (firsts.length > 240) break;
    }
    var perMonth = width / (span / 86400000 / 30.44);
    var every = [1, 2, 3, 6, 12].filter(function (n) { return perMonth * n >= 44; })[0] || 12;

    var taken = [document.getElementById('runupFrom'), document.getElementById('statDaysLabel')]
      .filter(Boolean).map(function (el) { return el.getBoundingClientRect(); });
    firsts.forEach(function (first) {
      var at = ((first - today) / span * 100).toFixed(2) + '%';
      var tick = document.createElement('span');
      tick.className = 'runup-tick';
      tick.style.left = at;
      ticks.appendChild(tick);

      var d = new Date(first);
      if (d.getUTCMonth() % every !== 0) return;
      var label = document.createElement('span');
      label.className = 'runup-month';
      label.style.left = at;
      label.textContent = d.getUTCMonth() === 0 ? String(d.getUTCFullYear()) : runupDate('month', first);
      names.appendChild(label);
      var box = label.getBoundingClientRect();
      var clash = taken.some(function (r) { return box.left < r.right + 10 && r.left < box.right + 10; });
      if (clash) label.remove();
    });
  }

  // One stop in the tab order for the whole scale; the arrow keys walk it.
  function runupRove(marks, to) {
    Array.prototype.forEach.call(marks.querySelectorAll('.runup-mark'), function (b) {
      b.tabIndex = b === to ? 0 : -1;
    });
  }
  function runupKeys(e) {
    var all = Array.prototype.slice.call(e.currentTarget.querySelectorAll('.runup-mark'));
    var at = all.indexOf(document.activeElement);
    if (at < 0) return;
    var to = at;
    if (e.key === 'ArrowRight') to = Math.min(all.length - 1, at + 1);
    else if (e.key === 'ArrowLeft') to = Math.max(0, at - 1);
    else if (e.key === 'Home') to = 0;
    else if (e.key === 'End') to = all.length - 1;
    else return;
    e.preventDefault();
    all[to].focus();
  }

  /* What is due at a mark. The same popover as the cost-by picker and the
   * files list, with the same manners: focus moves in, Escape closes it and
   * hands focus back to the mark, tabbing away or pressing elsewhere closes
   * it, and pressing the mark again closes it too. */
  function openRunUpList(btn, g) {
    var wasOpen = openBtn === btn;
    closePop(wasOpen);
    if (wasOpen) return;

    var pop = document.createElement('div');
    pop.className = 'sp-pop runup-pop';
    pop.setAttribute('role', 'group');
    pop.setAttribute('aria-label', btn.getAttribute('aria-label'));
    var list = document.createElement('ul');
    g.tasks.forEach(function (p) {
      var li = document.createElement('li');
      li.appendChild(cell('span', 'when' + (g.late ? ' late' : ''), runupDate('full', p.due)));
      li.appendChild(cell('span', 'what', p.task.name || t('un.untitled')));
      li.appendChild(cell('span', 'who', p.task.owner || ''));
      list.appendChild(li);
    });
    pop.appendChild(list);

    pop.tabIndex = -1;
    pop.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { e.stopPropagation(); closePop(true); }
    });
    pop.addEventListener('focusout', function (e) {
      // Focus going to the mark itself is a press on it, and the click that
      // follows closes the list; closing here would have that click reopen it.
      if (e.relatedTarget === btn) return;
      if (!pop.contains(e.relatedTarget)) closePop(false);
    });

    document.body.appendChild(pop);
    var r = btn.getBoundingClientRect();
    var top = r.bottom + 10;
    if (top + pop.offsetHeight > window.innerHeight - 8) top = Math.max(8, r.top - pop.offsetHeight - 10);
    var left = Math.min(r.left - 12, window.innerWidth - pop.offsetWidth - 8);
    pop.style.top = top + 'px';
    pop.style.left = Math.max(8, left) + 'px';
    openPop = pop;
    openBtn = btn;
    btn.setAttribute('aria-expanded', 'true');
    pop.focus();
  }

  /* ---------- Empty state ----------
   * A fresh instance has nothing to reconcile, so the overview shows what to
   * do first instead of a grid of dashes. Called from every path that can add
   * or remove the first record, not just from renderOverview: adding a sponsor
   * does not touch the overview but does end the empty state.
   */
  function syncEmptyState() {
    document.body.classList.toggle('is-empty', planIsEmpty());
  }

  // Its own function because the import reads it too, to decide whether there
  // is anything here worth keeping a copy of before it replaces the lot.
  function planIsEmpty() {
    return !state.budgetItems.length && !state.tasks.length &&
      !state.sponsors.length && !state.notes.length;
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

    // The money bar: paid and owed as the two halves of what is committed,
    // drawn against whichever is larger, the commitment or the ceiling, so a
    // plan under its ceiling shows the room it has left and one over it shows
    // the ceiling as a mark it has passed.
    var barMax = Math.max(m.total, m.ceiling, 1);
    document.getElementById('moneyBarPaid').style.width = ((Math.min(m.paid, m.total) / barMax) * 100).toFixed(2) + '%';
    document.getElementById('moneyBarOwed').style.width = ((Math.max(m.total - m.paid, 0) / barMax) * 100).toFixed(2) + '%';
    var ceilingMark = document.getElementById('moneyBarCeiling');
    ceilingMark.hidden = !(m.ceiling > 0);
    ceilingMark.style.left = ((m.ceiling / barMax) * 100).toFixed(2) + '%';
    ceilingMark.classList.toggle('over', m.ceiling > 0 && m.total > m.ceiling);

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
      var dueCell = cell('span', 'due', t3.due || '');
      // Late is late where the event is, as everywhere else on this page.
      if (t3.due && EVENT) {
        var thereNow = new Date(Date.now() + EVENT.offsetMinutes * 60000);
        var todayThere = thereNow.toISOString().slice(0, 10);
        if (String(t3.due) < todayThere) dueCell.classList.add('late');
      }
      li.appendChild(dueCell);
      list.appendChild(li);
    });
    // A task added, finished or given a date moves a mark on the run-up.
    renderRunUp(daysLeft());
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

    state.notes.forEach(function (note) {
      var row = document.createElement('div');
      row.className = 'flag';

      var ta = document.createElement('textarea');
      ta.rows = 2;
      ta.value = note.text || '';
      ta.placeholder = t('w.ph');
      ta.addEventListener('input', function () {
        note.text = ta.value;
        save();
      });

      var del = delButton(t('w.del'), function () {
        dropRow(state.notes, note);
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

  /* The question a destructive click asks, and only when something goes that
   * nothing brings back: the files on a row, or an attribution nobody is told
   * has moved. A row with neither stays one click, which is what the × is
   * for.
   *
   * With a database the row is everybody's, so the dialog says that first and
   * then what goes.
   */
  function confirmLoss(message) {
    return window.confirm(apiMode ? t('d.shared') + ' ' + message : message);
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
    state.sponsors.forEach(function (sp) {
      var row = document.createElement('div');
      row.className = 'sponsor-row';

      var code = document.createElement('input');
      code.type = 'text';
      code.className = 'code-input';
      code.placeholder = t('sp.code');
      code.value = sp.code || '';
      code.addEventListener('input', function () {
        sp.code = code.value;
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
        sp.name = name.value;
        save();
        renderSplit();
      });

      var amt = document.createElement('span');
      amt.className = 'sp-amt';
      amt.textContent = fmtCur(sponsorShare(sp.id));

      var del = delButton(t('sp.del'), function () {
        var id = sp.id;
        // Every line this name was on is re-split by the lines below, which
        // changes who owes what without saying so anywhere.
        var attributed = state.budgetItems.filter(function (i) {
          return (i.sponsors || []).indexOf(id) !== -1;
        }).length;
        if (attributed && !confirmLoss(t('sp.confirmdel', { n: attributed }))) return;
        dropRow(state.sponsors, sp);
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
      // The share again, as a length: five percentages in a column have to be
      // read and compared, five bars are compared by looking.
      var bar = cell('span', 'share', '');
      bar.setAttribute('aria-hidden', 'true');
      var fill = cell('span', 'share-fill' + (k === t('sp.unassigned') ? ' unassigned' : ''), '');
      fill.style.width = share + '%';
      bar.appendChild(fill);
      li.appendChild(bar);
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

    if (filesView && filesView.pop === pop) {
      filesView.input.remove();
      filesView = null;
    }
    pop.remove();
    if (btn) {
      btn.setAttribute('aria-expanded', 'false');
      if (returnFocus) btn.focus();
    }
  }
  document.addEventListener('click', function (e) {
    if (pickingFiles) return;
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


  /* ---------- Attachments ----------
   * Files on a budget line or a task: a quote, a receipt, a floor plan.
   *
   * The bytes never touch this origin. The server hands out an address in an
   * object store, signed for one file of one size; the browser sends the file
   * there itself and then tells the server, which goes and looks. A download
   * is an ordinary link that the server turns into a redirect.
   *
   * None of this is part of `state`, and that is the design rather than an
   * omission. A file exists on the server or it does not exist: there is
   * nothing to edit offline, nothing to merge, and nothing to put in this
   * browser's storage. The list comes in with every plan read and is simply
   * replaced.
   *
   * The control sits in the first cell of the row, beside the name, because
   * that column is frozen: whatever else has scrolled away, whether a line has
   * a quote attached is still in view.
   */
  var FILES = (CONFIG.attachments && Number(CONFIG.attachments.maxBytes) > 0)
    ? { maxBytes: Number(CONFIG.attachments.maxBytes) } : null;
  var attachments = [];
  var uploads = [];        // in flight or failed: { key, name, size, sent, phase, error, file, id, final }
  var filesView = null;    // the open list, so a plan read can redraw it: { key, draw }
  var pickingFiles = false;

  function fileKey(kind, id) { return kind + ':' + id; }
  function filesOf(kind, id) {
    var field = kind === 'task' ? 'taskId' : 'budgetItemId';
    return attachments.filter(function (a) { return a[field] === id; });
  }
  // Only once the plan has come from the server: before that this browser does
  // not know whether there is a server, and afterwards it may have lost it.
  function filesOffered() { return !!FILES && apiMode && !sessionGone; }

  // Whether a count of zero on this row is an answer or only ignorance. The
  // list arrives with the plan and with nothing else, so on a page painted
  // from this browser's copy, with the origin away, or a session that ended
  // before the reload, or simply the gap before the first read lands, it is
  // empty because nothing has filled it. A row under an id this browser minted has never
  // been sent and can have no files whatever the page knows, which is the one
  // case where the empty list is the truth.
  function filesUncounted(id) {
    return !!FILES && !apiMode && SERVER_ID.test(String(id));
  }
  function canChangeFiles() {
    return (sessionRole === 'admin' || sessionRole === 'editor') &&
      !document.body.classList.contains('is-archived');
  }

  function takeAttachments(plan) {
    attachments = (plan && Array.isArray(plan.attachments)) ? plan.attachments.slice() : [];
    refreshFileCounts();
    if (filesView) filesView.draw();
  }

  function fmtBytes(n) {
    var units = ['B', 'kB', 'MB', 'GB'];
    var v = Number(n) || 0, i = 0;
    while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
    var digits = (i === 0 || v >= 100) ? 0 : 1;
    var text;
    try { text = v.toLocaleString(LANG, { maximumFractionDigits: digits }); } catch (e) { text = v.toFixed(digits); }
    return text + ' ' + units[i];
  }

  function clipIcon() {
    var NS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', '15');
    svg.setAttribute('height', '15');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    var path = document.createElementNS(NS, 'path');
    path.setAttribute('d', 'M21 11.5l-8.6 8.6a5.5 5.5 0 0 1-7.8-7.8l8.9-8.9a3.7 3.7 0 0 1 5.2 5.2l-8.9 8.9a1.8 1.8 0 0 1-2.6-2.6l8.3-8.2');
    path.setAttribute('fill', 'none');
    path.setAttribute('stroke', 'currentColor');
    path.setAttribute('stroke-width', '1.8');
    path.setAttribute('stroke-linecap', 'round');
    path.setAttribute('stroke-linejoin', 'round');
    svg.appendChild(path);
    return svg;
  }

  function addFilesButton(td, kind, row) {
    if (!filesOffered()) return;
    td.classList.add('has-files');
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'files-btn';
    btn.setAttribute('aria-haspopup', 'true');
    btn.setAttribute('aria-expanded', 'false');
    btn.setAttribute('data-files-kind', kind);
    // Read at click time, not captured: the id is this browser's own until the
    // row's first save lands, and the server's from then on.
    btn.appendChild(clipIcon());
    var count = document.createElement('span');
    count.className = 'files-count';
    btn.appendChild(count);
    btn.addEventListener('click', function (ev) {
      ev.stopPropagation();
      openFiles(btn, kind, row);
    });
    td.appendChild(btn);
    setFileCount(btn, kind, row.id);
  }

  function setFileCount(btn, kind, id) {
    var n = filesOf(kind, id).length;
    btn.setAttribute('data-files-id', id);
    btn.classList.toggle('has-some', n > 0);
    btn.setAttribute('aria-label', t('f.open', { n: n }));
    btn.title = t('f.open', { n: n });
    btn.querySelector('.files-count').textContent = n > 0 ? String(n) : '';
  }

  // Counts only, without rebuilding a table somebody may be typing in.
  function refreshFileCounts() {
    Array.prototype.forEach.call(document.querySelectorAll('.files-btn'), function (btn) {
      setFileCount(btn, btn.getAttribute('data-files-kind'), mapId(btn.getAttribute('data-files-id')));
    });
  }

  function openFiles(btn, kind, row) {
    closePop();
    var pop = document.createElement('div');
    pop.className = 'sp-pop files-pop';
    pop.setAttribute('role', 'dialog');
    pop.setAttribute('aria-label', t('f.title'));

    var key = function () { return fileKey(kind, row.id); };
    var onServer = function () { return SERVER_ID.test(String(row.id)); };

    var title = document.createElement('div');
    title.className = 'files-title';
    title.textContent = t('f.title');
    pop.appendChild(title);

    var list = document.createElement('ul');
    list.className = 'files-list';
    pop.appendChild(list);

    var foot = document.createElement('div');
    foot.className = 'files-foot';
    pop.appendChild(foot);

    // Outside the popup on purpose. Opening the system's file chooser takes
    // focus away from the page, and an input inside a popup that closes when
    // focus leaves would be removed while somebody is still choosing.
    var input = document.createElement('input');
    input.type = 'file';
    input.multiple = true;
    input.className = 'sr-only';
    input.tabIndex = -1;
    input.setAttribute('aria-hidden', 'true');
    document.body.appendChild(input);
    function donePicking() { setTimeout(function () { pickingFiles = false; }, 0); }
    input.addEventListener('change', function () {
      var chosen = Array.prototype.slice.call(input.files || []);
      input.value = '';
      donePicking();
      chosen.forEach(function (file) { startUpload(kind, row, file); });
      if (openPop === pop) pop.focus();
    });
    input.addEventListener('cancel', donePicking);

    function draw() {
      // Redrawing removes whatever was focused inside the list - the button
      // that was just pressed, usually - and an element removed while focused
      // fires focusout with nowhere to go, which is this popup's cue to close.
      // So pressing "remove" shut the list it was pressed in. Park the focus
      // on the popup itself first.
      if (pop.contains(document.activeElement) && document.activeElement !== pop) pop.focus();
      list.textContent = '';
      var files = filesOf(kind, row.id);
      var mine = uploads.filter(function (u) { return u.key === key(); });

      if (!files.length && !mine.length) {
        var none = document.createElement('li');
        none.className = 'files-none';
        none.textContent = t('f.none');
        list.appendChild(none);
      }
      files.forEach(function (a) { list.appendChild(fileRow(a)); });
      mine.forEach(function (u) { list.appendChild(uploadRow(u)); });

      foot.textContent = '';
      if (!canChangeFiles()) {
        if (sessionRole === 'viewer') note(foot, t('f.readonly'));
        return;
      }
      if (!onServer()) { note(foot, t('f.wait')); return; }
      var add = document.createElement('button');
      add.type = 'button';
      add.className = 'add-row-btn files-add';
      add.textContent = t('f.add');
      add.addEventListener('click', function () {
        pickingFiles = true;
        input.click();
      });
      foot.appendChild(add);
      note(foot, t('f.limit', { a: fmtBytes(FILES.maxBytes) }));
    }

    function note(parent, text) {
      var n = document.createElement('div');
      n.className = 'pop-note';
      n.textContent = text;
      parent.appendChild(n);
    }

    function fileRow(a) {
      var li = document.createElement('li');
      li.className = 'files-row';
      var href = API_BASE + '/attachments/' + encodeURIComponent(a.id) + '/content';

      var link = document.createElement('a');
      link.className = 'files-name';
      link.textContent = a.name;
      // What the server will show opens beside the planner; everything else is
      // a download, and says so by not pretending to be a page.
      if (a.viewable) {
        link.href = href + '?inline=1';
        link.target = '_blank';
        link.rel = 'noopener';
      } else {
        link.href = href;
      }
      li.appendChild(link);

      var meta = document.createElement('span');
      meta.className = 'files-meta';
      meta.textContent = fmtBytes(a.size);
      li.appendChild(meta);

      if (a.viewable) {
        var dl = document.createElement('a');
        dl.className = 'files-dl';
        dl.href = href;
        dl.textContent = t('f.download');
        li.appendChild(dl);
      }

      if (canChangeFiles()) {
        li.appendChild(delButton(t('f.del', { a: a.name }), function () {
          // Some browsers move focus when their own dialog opens, and this
          // list closes when focus leaves it.
          pickingFiles = true;
          var sure = window.confirm(t('f.confirmdel', { a: a.name }));
          pickingFiles = false;
          if (!sure) return;
          api('DELETE', '/attachments/' + encodeURIComponent(a.id)).then(function (res) {
            // Gone already is gone: somebody else removed it first.
            if (res.status !== 204 && res.status !== 404) {
              if (res.status === 401) { sessionLost(); return; }
              flash(t('f.delfailed'));
              return;
            }
            attachments = attachments.filter(function (x) { return x.id !== a.id; });
            refreshFileCounts();
            draw();
          });
        }));
      }
      return li;
    }

    function uploadRow(u) {
      var li = document.createElement('li');
      li.className = 'files-row files-pending';
      var name = document.createElement('span');
      name.className = 'files-name';
      name.textContent = u.name;
      li.appendChild(name);

      var status = document.createElement('span');
      status.className = 'files-meta';
      status.setAttribute('role', 'status');
      li.appendChild(status);

      if (u.phase === 'failed') {
        li.classList.add('files-failed');
        status.textContent = u.error;
        if (!u.final) {
          var again = document.createElement('button');
          again.type = 'button';
          again.className = 'link-btn';
          again.textContent = t('f.retry');
          again.addEventListener('click', function () { sendUpload(kind, row, u); });
          li.appendChild(again);
        }
        li.appendChild(delButton(t('f.del', { a: u.name }), function () {
          uploads = uploads.filter(function (x) { return x !== u; });
          draw();
        }));
      } else if (u.phase === 'checking') {
        status.textContent = t('f.checking');
      } else {
        var pct = u.size ? Math.min(100, Math.floor((u.sent / u.size) * 100)) : 0;
        status.textContent = t('f.sending', { n: pct });
        var bar = document.createElement('progress');
        bar.max = 100;
        bar.value = pct;
        li.appendChild(bar);
      }
      return li;
    }

    pop.tabIndex = -1;
    pop.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') { e.stopPropagation(); closePop(true); }
    });
    pop.addEventListener('focusout', function (e) {
      if (pickingFiles) return;
      if (!pop.contains(e.relatedTarget)) closePop(false);
    });

    document.body.appendChild(pop);
    draw();
    var r = btn.getBoundingClientRect();
    var top = r.bottom + 4;
    if (top + pop.offsetHeight > window.innerHeight - 8) top = Math.max(8, r.top - pop.offsetHeight - 4);
    var left = Math.min(r.left, window.innerWidth - pop.offsetWidth - 8);
    pop.style.top = top + 'px';
    pop.style.left = Math.max(8, left) + 'px';

    openPop = pop;
    openBtn = btn;
    btn.setAttribute('aria-expanded', 'true');
    filesView = { pop: pop, draw: draw, input: input };
    pop.focus();
  }

  function redrawFiles() { if (filesView) filesView.draw(); }

  function startUpload(kind, row, file) {
    var u = { key: fileKey(kind, row.id), name: file.name, size: file.size, sent: 0,
      phase: 'sending', error: '', file: file, id: null, final: false };
    uploads.push(u);
    // Refused here, before anything is asked of anybody: the server would say
    // the same, after a round trip from the far side of the world.
    if (file.size === 0) { failUpload(u, t('f.empty'), true); return; }
    if (file.size > FILES.maxBytes) { failUpload(u, t('f.toolarge', { a: fmtBytes(FILES.maxBytes) }), true); return; }
    sendUpload(kind, row, u);
  }

  function failUpload(u, message, final) {
    u.phase = 'failed';
    u.error = message;
    u.final = !!final;
    redrawFiles();
  }

  function sendUpload(kind, row, u) {
    u.phase = 'sending';
    u.sent = 0;
    u.error = '';
    u.key = fileKey(kind, row.id);
    redrawFiles();

    var body = { name: u.name, size: u.size, contentType: u.file.type || '' };
    body[kind === 'task' ? 'taskId' : 'budgetItemId'] = row.id;

    api('POST', '/attachments', body).then(function (res) {
      if (res.status !== 201 || !res.body || !res.body.upload) {
        if (res.status === 401) { sessionLost(); failUpload(u, t('f.failed')); return; }
        if (res.status === 413) { failUpload(u, t('f.toolarge', { a: fmtBytes(FILES.maxBytes) }), true); return; }
        if (res.status === 409) { failUpload(u, t('f.full'), true); return; }
        if (res.status === 403) { failUpload(u, t('f.readonly'), true); return; }
        failUpload(u, res.status === 0 ? t('f.network') : t('f.failed'));
        return;
      }
      u.id = res.body.attachment.id;
      var up = res.body.upload;

      // XMLHttpRequest and not fetch, for one reason: fetch cannot report how
      // much of a body has been sent, and on a slow connection a bar that
      // moves is the difference between waiting and giving up.
      var xhr = new XMLHttpRequest();
      xhr.open(up.method || 'PUT', up.url);
      Object.keys(up.headers || {}).forEach(function (name) { xhr.setRequestHeader(name, up.headers[name]); });
      xhr.upload.onprogress = function (e) {
        if (!e.lengthComputable) return;
        u.sent = e.loaded;
        redrawFiles();
      };
      xhr.onload = function () {
        if (xhr.status >= 200 && xhr.status < 300) { confirmUpload(u, 0); return; }
        failUpload(u, t('f.failed'));
      };
      xhr.onerror = function () { failUpload(u, t('f.network')); };
      xhr.onabort = xhr.onerror;
      xhr.send(u.file);
    });
  }

  function confirmUpload(u, attempt) {
    u.phase = 'checking';
    redrawFiles();
    api('POST', '/attachments/' + encodeURIComponent(u.id) + '/complete').then(function (res) {
      if (res.status === 200 && res.body) {
        uploads = uploads.filter(function (x) { return x !== u; });
        if (!attachments.some(function (a) { return a.id === res.body.id; })) attachments.push(res.body);
        refreshFileCounts();
        redrawFiles();
        return;
      }
      // The file store did not answer, or the request did not arrive. The
      // file is there and the server has said it will keep the record, so ask
      // again rather than send the whole file a second time.
      if ((res.status === 503 || res.status === 0) && attempt < 5) {
        setTimeout(function () { confirmUpload(u, attempt + 1); }, 2000 * (attempt + 1));
        return;
      }
      if (res.status === 401) sessionLost();
      failUpload(u, res.status === 0 ? t('f.network') : t('f.failed'));
    });
  }

  /* ---------- Text that fits ----------
   * A name or a remark in the budget table grows its row rather than hiding
   * behind a scrollbar. The field used to be as tall as the row and no taller,
   * so "Cetak-cetak all sign & cue cards" showed its first line, half of its
   * second, and a pair of scroll arrows in a cell the width of a thumb.
   *
   * Measured, because CSS cannot do it everywhere yet (field-sizing: content is
   * not in every browser a family owns). And only while it can be measured: a
   * field inside a hidden tab reports a scroll height of zero, and sizing it
   * to that makes the text vanish - so a hidden field is left alone and
   * fitted when its tab is shown. A row somebody has dragged taller keeps its
   * height; this only ever asks for more room, never less than the text needs.
   */
  function fitText(ta) {
    if (!ta || ta.offsetParent === null) return;
    ta.style.height = 'auto';
    // scrollHeight is the text and its padding; the height being set includes
    // the border as well (box-sizing: border-box), so without adding it back
    // the field comes out two pixels short and clips the last descender.
    var border = ta.offsetHeight - ta.clientHeight;
    ta.style.height = (ta.scrollHeight + border) + 'px';
  }

  function fitBudgetText() {
    Array.prototype.forEach.call(document.querySelectorAll('#budgetBody textarea'), fitText);
  }

  var fitQueued = false;
  window.addEventListener('resize', function () {
    if (fitQueued) return;
    fitQueued = true;
    requestAnimationFrame(function () { fitQueued = false; fitBudgetText(); renderRunUp(daysLeft()); });
  });

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
    // A narrower column wraps a name onto more lines, a wider one onto fewer.
    fitBudgetText();
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
    state.budgetItems.forEach(function (item) {
      var tr = document.createElement('tr');

      function textCell(key, cls) {
        var td = document.createElement('td');
        if (cls) td.className = cls;
        var inp = document.createElement('textarea');
        inp.rows = 1;
        inp.value = item[key] || '';
        inp.addEventListener('input', function () {
          item[key] = inp.value;
          save();
          fitText(inp);
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
          item[key] = Number(inp.value) || 0;
          save();
          refreshRow(tr, item);
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
        openPicker(byBtn, item);
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
        // The row's files go with it, out of the plan and then out of the
        // bucket, and they are in neither the export nor the backup. Asked
        // either way where they cannot be counted: the delete is queued
        // against the copy on the screen and takes them when the origin is
        // back, which is the moment the loss is least visible.
        var files = filesOf('budget', item.id).length;
        if (filesUncounted(item.id)) {
          if (!confirmLoss(t('b.confirmdelunknown'))) return;
        } else if (files && !confirmLoss(t('b.confirmdel', { n: files }))) return;
        dropRow(state.budgetItems, item);
        save();
        renderBudgetTable();
        renderBudgetTotals();
        renderOverview();
      }));

      addFilesButton(tdItem, 'budget', item);

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
    fitBudgetText();
  }

  function label(td, key) { td.setAttribute('data-label', t(key)); }

  function refreshRow(tr, item) {
    var tot = lineTotal(item);
    var owing = tot - (Number(item.paid) || 0);
    tr.children[3].textContent = fmtCur(tot);
    tr.children[5].textContent = fmtCur(owing);
    tr.children[5].classList.toggle('owing', owing > 0);
    // Paid in full: the line is settled, and the figure that said what was
    // owed says so in the colour of money paid rather than as a bare zero.
    tr.classList.toggle('settled', tot > 0 && owing <= 0);
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

  /* Today's date where the event is, as YYYY-MM-DD, so that "late" means the
   * same thing on every screen - and on a planner with no date, the reader's
   * own today, which is the only one there is. */
  function todayISO() {
    if (EVENT) return new Date(Date.now() + EVENT.offsetMinutes * 60000).toISOString().slice(0, 10);
    var d = new Date();
    return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0') + '-' + String(d.getDate()).padStart(2, '0');
  }

  /* A row says what state it is in without being read: the stylesheet draws a
   * done task struck through and quiet, one in progress with the lamp at its
   * edge, and a late date in the alarm colour. Set here, on the row, so a
   * change of status or date repaints one row and not the table under a
   * cursor. */
  function markTaskRow(tr, task) {
    tr.setAttribute('data-status', task.status || 'not-started');
    tr.classList.toggle('late', !!task.due && task.status !== 'done' && String(task.due) < todayISO());
  }

  // How many of each, beside the filter that would show them.
  function renderTaskCounts() {
    var counts = { all: state.tasks.length, 'not-started': 0, 'in-progress': 0, done: 0 };
    state.tasks.forEach(function (task) { if (task.status in counts) counts[task.status] += 1; });
    Array.prototype.forEach.call(document.querySelectorAll('#taskFilters .pill-count'), function (el) {
      el.textContent = counts[el.getAttribute('data-count')] || 0;
    });
  }

  function renderTasksTable() {
    var body = document.getElementById('tasksBody');
    body.innerHTML = '';
    var shown = 0;
    renderTaskCounts();
    state.tasks.forEach(function (task) {
      if (currentFilter !== 'all' && task.status !== currentFilter) return;
      shown++;
      var tr = document.createElement('tr');

      var tdName = document.createElement('td');
      var nameInput = document.createElement('input');
      nameInput.type = 'text';
      nameInput.value = task.name;
      nameInput.addEventListener('input', function () {
        task.name = nameInput.value;
        save();
      });
      tdName.appendChild(nameInput);

      var tdOwner = document.createElement('td');
      var ownerInput = document.createElement('input');
      ownerInput.type = 'text';
      ownerInput.value = task.owner || '';
      ownerInput.addEventListener('input', function () {
        task.owner = ownerInput.value;
        save();
        renderOverview();
      });
      tdOwner.appendChild(ownerInput);

      var tdDue = document.createElement('td');
      var dueInput = document.createElement('input');
      dueInput.type = 'date';
      dueInput.value = task.due || '';
      dueInput.addEventListener('input', function () {
        task.due = dueInput.value;
        save();
        markTaskRow(tr, task);
        renderOverview();
        // The one gesture on this page that makes a deadline reminder obvious:
        // somebody has just written a date they intend to be held to.
        if (dueInput.value) offerPush();
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
        task.status = statusSelect.value;
        save();
        markTaskRow(tr, task);
        renderTaskCounts();
        renderOverview();
        if (currentFilter !== 'all') renderTasksTable();
      });
      tdStatus.appendChild(statusSelect);

      var tdDel = document.createElement('td');
      tdDel.className = 'del-cell';
      tdDel.appendChild(delButton(t('k.del'), function () {
        var files = filesOf('task', task.id).length;
        if (filesUncounted(task.id)) {
          if (!confirmLoss(t('k.confirmdelunknown'))) return;
        } else if (files && !confirmLoss(t('k.confirmdel', { n: files }))) return;
        dropRow(state.tasks, task);
        save();
        renderTasksTable();
        renderOverview();
      }));

      addFilesButton(tdName, 'task', task);
      markTaskRow(tr, task);

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

  /* The planner as a file. Its own function because the import takes one for
   * itself before it replaces anything: the server keeps no undo, and the
   * files on the rows it removes are in neither the export nor the backup.
   * Answers with the filename, or null where the browser would not save it.
   */
  function downloadPlan() {
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
      return filename;
    } catch (e) {
      console.log(json);
      return null;
    }
  }

  /* What replacing the planner with a file takes away: the rows the server
   * holds whose ids the file does not name, and the attachments on them. A
   * file written by another browser, or by cmd/soiree-import, names none of
   * them, so the answer is usually everything, which is a number worth
   * reading before saying yes rather than working out afterwards.
   */
  function replacedRows(incoming) {
    var out = { budgetItems: 0, tasks: 0, files: 0 };
    [['budgetItems', 'budget'], ['tasks', 'task']].forEach(function (pair) {
      var keeping = {};
      (incoming[pair[0]] || []).forEach(function (row) {
        if (row && row.id) keeping[row.id] = true;
      });
      Object.keys(shadow[pair[0]]).forEach(function (id) {
        if (keeping[id]) return;
        out[pair[0]]++;
        out.files += filesOf(pair[1], id).length;
      });
    });
    return out;
  }

  document.getElementById('exportData').addEventListener('click', function () {
    flushSave();
    var filename = downloadPlan();
    flash(filename ? t('d.exported', { a: filename }) : t('d.exportfail'));
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
        // A wholesale replacement is the largest edit this page can make, and
        // in API mode it is a real one: every row the server holds and this
        // file does not is removed, and every row in the file is created, for
        // everybody looking at that planner, with the attachments on those
        // rows. So the dialog counts them first, and a copy of what is about
        // to go is downloaded on the way through, since nothing on the server
        // can bring it back.
        var parts = [];
        if (apiMode && shadow) {
          var going = replacedRows(incoming);
          parts.push(t('d.confirmcounts', { a: going.budgetItems, b: going.tasks }));
          if (going.files) parts.push(t('d.confirmfiles', { n: going.files }));
        }
        // No copy of an empty planner: this arrives in a browser that has
        // never seen one often enough that the file would be a puzzle.
        var keepCopy = !planIsEmpty();
        if (keepCopy) parts.push(t('d.confirmcopy'));
        parts.push(t('d.confirm'));
        if (!confirmLoss(parts.join(' '))) return;
        // The copy is the way back from a replacement the server keeps no undo
        // of, and the dialog they have just agreed to says it was taken. A
        // browser that would not make the blob has not taken it, so the
        // replacement stops here rather than proceeding on a promise nobody
        // kept. Reported in the words the export button uses, because the
        // fallback is the same one: the planner is in the console.
        if (keepCopy && !downloadPlan()) { flash(t('d.exportfail')); return; }

        // `forgotten` is cleared here because this path calls flushSave()
        // directly and save() is the only other place that clears it. On a
        // page that had been signed out of and back into, the flag was still
        // up, flushSave() returned at its first line, and the import sat on
        // screen looking finished: not sent to the server, not even written to
        // this browser. Everybody else saw an empty plan.
        forgotten = false;
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

  /* ---------- The theme control ----------
   * Built here rather than written into index.html because the markup and the
   * stylesheet are somebody else's file this week. It borrows the classes the
   * task filters already use, which is the same segmented control this page
   * makes elsewhere and costs no new rules.
   *
   * Three states, not two. A switch with two positions cannot express "follow
   * whatever this device is set to", which is the state most people want and
   * the one the page starts in — and a person who has picked dark on a laptop
   * that switches at sunset needs a way back to that.
   */
  (function themeControl() {
    var tools = document.querySelector('.data-tools');
    if (!tools) return;

    var group = document.createElement('div');
    group.className = 'filter-pills';
    group.setAttribute('role', 'group');
    group.setAttribute('aria-label', t('th.title'));

    var btns = THEMES.map(function (mode) {
      var b = document.createElement('button');
      b.className = 'pill';
      b.type = 'button';
      b.textContent = t('th.' + mode);
      b.addEventListener('click', function () { choose(mode); });
      group.appendChild(b);
      return b;
    });

    function mark(mode) {
      THEMES.forEach(function (m, i) {
        var on = m === mode;
        btns[i].classList.toggle('active', on);
        btns[i].setAttribute('aria-pressed', on ? 'true' : 'false');
      });
    }

    function choose(mode) {
      try {
        // Back to the system means back to no stored preference at all, so the
        // key exists exactly while somebody is overriding their device.
        if (mode === 'system') localStorage.removeItem(THEME_KEY);
        else localStorage.setItem(THEME_KEY, mode);
      } catch (e) { /* storage unavailable; the choice still applies here */ }
      paintTheme(mode);
      mark(mode);
    }

    mark(readTheme());
    languageHooks.push(function () {
      group.setAttribute('aria-label', t('th.title'));
      THEMES.forEach(function (mode, i) { btns[i].textContent = t('th.' + mode); });
    });
    // Before the status line, which is the last thing in the row and the one
    // that grows a sentence long.
    tools.insertBefore(group, document.getElementById('dataMsg'));
  })();

  /* ---------- The language switcher ----------
   * In place, never by reloading. A page that has a base behind it now comes
   * back from a reload with its unsent edits intact, but a page that has never
   * reached the server has no base and nothing to merge against, so a reload
   * still throws away whatever was typed into it — and a reload is disruptive
   * in its own right: it takes the screen somebody is on and the field they
   * are halfway through.
   * Everything this page says comes from three calls, so saying it again in
   * another language is those three calls, plus the few things that bake a
   * label when they are built.
   *
   * A click is remembered on this device. It also takes any ?lang= out of the
   * address bar: that outranks a stored choice, so leaving it there would make
   * the flag somebody just clicked stop working at the next reload.
   *
   * auth.js is told on `soiree:language`, because its screens may be the ones
   * showing — the switcher sits above the sign-in screen precisely so that
   * somebody who cannot read it can change it.
   */
  function chooseLanguage(tag) {
    tag = pickLang(tag);
    if (!tag) return;
    try { localStorage.setItem(LANG_KEY, tag); } catch (e) { /* this visit only */ }

    if (/[?&]lang=/.test(location.search) && window.history && history.replaceState) {
      var rest = location.search.replace(/([?&])lang=[^&]*(&|$)/, function (m, lead, tail) {
        return tail ? lead : '';
      });
      history.replaceState(null, '', location.pathname + rest + location.hash);
    }

    if (tag !== LANG) {
      LANG = tag;
      document.documentElement.lang = LANG;
      applyStrings();
      renderCountdown();
      renderAll();
      languageHooks.forEach(function (fn) { fn(); });
      document.dispatchEvent(new CustomEvent('soiree:language', { detail: { language: LANG } }));
    }
    markFlags();
  }

  function markFlags() {
    Array.prototype.forEach.call(document.querySelectorAll('#langSwitch [data-lang]'), function (b) {
      b.setAttribute('aria-pressed', b.getAttribute('data-lang') === LANG ? 'true' : 'false');
    });
  }

  Array.prototype.forEach.call(document.querySelectorAll('#langSwitch [data-lang]'), function (b) {
    b.addEventListener('click', function () { chooseLanguage(b.getAttribute('data-lang')); });
  });
  markFlags();

  /* ---------- Deadline notifications ----------
   * Three conditions, all of them necessary:
   *
   *   - The server published a VAPID public key. Its *absence* is the signal:
   *     the field is omitted from the config rather than sent empty, because a
   *     browser cannot subscribe without it and offering a button that cannot
   *     work spends a permission prompt on nothing.
   *   - There is an API here at all. The subscription endpoints are mounted
   *     with the accounts surface, so a deployment with a key and no database
   *     has the key and no route behind it.
   *   - The browser has the Push API, which on iOS means the site has been
   *     added to the Home Screen first. That is Apple's rule, not something to
   *     work around; on Android, which is most of this audience, it is ordinary.
   */
  var VAPID = String(CONFIG.vapidPublicKey || '');

  function pushable() {
    return !!(apiMode && VAPID && navigator.serviceWorker &&
      window.PushManager && window.Notification);
  }

  // Unpadded base64url to the bytes applicationServerKey wants. atob reads
  // neither the URL alphabet nor a missing pad, so both go back on first.
  function vapidKey(k) {
    var b = (k + '==='.slice(0, (4 - k.length % 4) % 4)).replace(/-/g, '+').replace(/_/g, '/');
    var raw = atob(b);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out;
  }

  // POSTed verbatim: the endpoint takes the PushSubscription as the Push API
  // hands it over, and transcribing it by hand is how a p256dh ends up in the
  // auth field. JSON.stringify calls the object's own toJSON, which is the
  // shape the server parses.
  function pushSave(sub) { return api('POST', '/push/subscriptions', sub); }

  /* Re-post whatever this device is already subscribed to, on every load.
   *
   * It costs one request and the endpoint is idempotent on the endpoint URL,
   * so it cannot pile up rows — and it is the only thing that puts a device
   * back in the table after the server's subscriptions are restored from a
   * backup, or after its key changed and the browser made a new subscription.
   * Silent: no permission is asked for and nothing is said if it fails.
   */
  function pushRefresh() {
    if (!pushable() || Notification.permission !== 'granted') return;
    navigator.serviceWorker.ready
      .then(function (reg) { return reg.pushManager.getSubscription(); })
      .then(function (sub) { if (sub) pushSave(sub); })
      .catch(function () { /* nothing subscribed here */ });
  }

  /* Turning reminders on and off, for the one-time offer below and for the
   * standing control on the account screen (auth.js draws it; this owns what
   * it does). The states:
   *
   *   unavailable — no VAPID key or no API. Not this deployment's feature;
   *                 nothing should be drawn at all.
   *   unsupported — the browser has no Push API, and that is all it means. On
   *                 an iPhone the page is in a Safari tab rather than on the
   *                 Home Screen; anywhere else the browser is old, or private.
   *   starting    — the browser can, and the service worker is not active yet.
   *                 A first visit has to fetch and install it, which on a slow
   *                 line outlasts the wait below. Not a verdict: `soiree:push`
   *                 is dispatched when that changes and the switch is redrawn.
   *   noworker    — the browser can, and the service worker did not register.
   *   blocked     — the person said no to the browser's prompt. Only the
   *                 browser's own site settings can undo that.
   *   refused     — enable() only: permission was given and the browser still
   *                 would not subscribe. Brave, until its push setting is on.
   *   off, on     — whether this device holds a subscription.
   *
   * "starting" and "noworker" used to be reported as "unsupported", which is
   * how desktop Chrome came to be told it cannot receive notifications and to
   * try an iPhone's Home Screen: the worker the server handed out did not
   * parse, register() rejected into a catch that said nothing, and `ready`
   * never settled. Whatever is wrong with the worker is this site's problem or
   * a setting's, and the sentence has to be able to say which.
   *
   * `ready` never settles on a page whose service worker did not register, so
   * everything that waits on it is bounded: a control that spins for ever is
   * worse than one that says it cannot.
   */
  var swProblem = '';   // '' | 'storage' | 'failed', from register() rejecting
  var swGaveUp;         // settles swRejected, which is the only way it settles
  var swRejected = new Promise(function (resolve) { swGaveUp = resolve; });

  function pushRegistration() {
    return new Promise(function (resolve) {
      var done = false;
      function finish(reg) { if (!done) { done = true; resolve(reg || null); } }
      if (swProblem) { finish(null); return; }
      setTimeout(function () { finish(null); }, 3000);
      // A rejection that arrives during the wait ends it: there is nothing
      // left to wait for.
      swRejected.then(function () { finish(null); });
      try { navigator.serviceWorker.ready.then(finish, function () { finish(null); }); }
      catch (e) { finish(null); }
    });
  }

  var pushWatching = false;

  // Not ready within the bound is not the same as never. Keep waiting, without
  // one, and say so when the answer changes — either way it goes.
  function pushStarting() {
    if (!pushWatching) {
      pushWatching = true;
      var changed = function () {
        if (!pushWatching) return;
        pushWatching = false;
        document.dispatchEvent(new CustomEvent('soiree:push'));
      };
      swRejected.then(changed);
      try { navigator.serviceWorker.ready.then(changed, function () { /* stays as it is */ }); }
      catch (e) { /* stays as it is */ }
    }
    return 'starting';
  }

  function pushState() {
    // `apiAvailable` rather than `apiMode`: the first is latched the moment the
    // origin answers, the second only once the plan has been adopted, and the
    // account screen can ask in between — it draws as soon as the session
    // answers, which on a page opened at /#/account is usually first.
    var api = apiMode || !!(window.soiree && window.soiree.apiAvailable === true);
    if (!api || !VAPID) return Promise.resolve('unavailable');
    if (!(navigator.serviceWorker && window.PushManager && window.Notification)) {
      return Promise.resolve('unsupported');
    }
    if (Notification.permission === 'denied') return Promise.resolve('blocked');
    return pushRegistration().then(function (reg) {
      if (!reg) return swProblem ? 'noworker' : pushStarting();
      return reg.pushManager.getSubscription().then(function (sub) { return sub ? 'on' : 'off'; });
    }).catch(function () { return 'off'; });
  }

  /* Why, for the states that need explaining. auth.js has the sentences; this
   * has the facts they are chosen by, because every one of them is a question
   * about the browser and this is the file that talks to it.
   *
   * Feature checks wherever one exists: `navigator.standalone` and the
   * display-mode query for "opened from the Home Screen", `isSecureContext`,
   * `window.safari` (macOS Safari and nothing else), `navigator.brave`.
   *
   * The user-agent string is read for one thing, because nothing else tells it:
   * whether this is an iPhone or iPad. Safari in a tab there has no PushManager
   * and neither has a ten-year-old desktop browser, and the advice is opposite
   * — "put it on your Home Screen" against "use another browser". An iPad says
   * it is a Mac, so a Mac with a touch screen, which does not exist, is one.
   * And, only once that is known, whether it is Safari: every browser on iOS
   * is WebKit and says "Safari", so the others are told by the token they add.
   * An in-app browser adds none and drops "Version/". A wrong guess is cheap,
   * since both sentences end in the same place: Safari, then the Home Screen.
   */
  function appleTouch() {
    var ua = navigator.userAgent || '';
    return /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
  }

  function onHomeScreen() {
    if (navigator.standalone === true) return true;
    try { return !!window.matchMedia && window.matchMedia('(display-mode: standalone)').matches; }
    catch (e) { return false; }
  }

  function pushWhy(state) {
    var ua = navigator.userAgent || '';
    if (state === 'unsupported') {
      if (appleTouch()) {
        // Already an app and still no push: an iOS from before 16.4.
        if (onHomeScreen()) return 'ios-app';
        return /Version\/[\d.]+.*Safari\//.test(ua) && !/CriOS|FxiOS|EdgiOS|OPiOS|OPT\/|DuckDuckGo|GSA\//.test(ua)
          ? 'ios-safari' : 'ios-other';
      }
      // A page on plain http has no service worker whatever the browser.
      return window.isSecureContext === false ? 'insecure' : '';
    }
    if (state === 'blocked') {
      if (appleTouch()) return 'ios-app';
      return window.safari ? 'safari' : '';
    }
    if (state === 'refused') return navigator.brave ? 'brave' : '';
    if (state === 'noworker') return swProblem;
    return '';
  }

  // Must be called from inside a click: the permission prompt is only shown
  // for a gesture, and this page spends it exactly once.
  function pushEnable() {
    if (!pushable()) return pushState();
    var asked;
    try { asked = Notification.requestPermission(); } catch (e) { asked = null; }
    // Old Safari answers through a callback and returns nothing.
    if (!asked || typeof asked.then !== 'function') return pushState();
    return asked.then(function (verdict) {
      if (verdict !== 'granted') return 'blocked';
      return pushRegistration().then(function (reg) {
        if (!reg) return swProblem ? 'noworker' : pushStarting();
        return reg.pushManager.getSubscription().then(function (existing) {
          return existing || reg.pushManager.subscribe({
            userVisibleOnly: true,
            applicationServerKey: vapidKey(VAPID)
          });
        }).then(function (sub) {
          return pushSave(sub).then(function (res) { return res.status === 204 ? 'on' : 'off'; });
        }, function () {
          // Permission given, a worker running, and the browser says no: push
          // is switched off in the browser itself. "Try again" would be a lie.
          return 'refused';
        });
      });
    }).catch(function () { return pushState(); });
  }

  // The server first, then the browser. The other order leaves a row the
  // digest keeps sending to until the push service reports it gone; this one
  // leaves, at worst, a browser subscription nobody sends to.
  function pushDisable() {
    return pushRegistration().then(function (reg) {
      if (!reg) return swProblem ? 'noworker' : pushStarting();
      return reg.pushManager.getSubscription().then(function (sub) {
        if (!sub) return 'off';
        return api('DELETE', '/push/subscriptions?endpoint=' + encodeURIComponent(sub.endpoint))
          .then(function (res) {
            if (res.status !== 204) return 'on';
            return Promise.resolve(sub.unsubscribe()).then(function () { return 'off'; }, function () { return 'off'; });
          });
      });
    }).catch(function () { return pushState(); });
  }

  window.soiree = window.soiree || {};
  window.soiree.push = { state: pushState, enable: pushEnable, disable: pushDisable, why: pushWhy };

  var pushAsked = false;

  /* The offer, and the one permission prompt this page ever spends.
   *
   * Never on load. A denied notification permission in Chrome is sticky — the
   * person has to go into the site settings to undo it — so there is exactly
   * one attempt per person, and it is worth spending at the moment the value
   * is obvious rather than the moment the page appears. Notification.permission
   * is also the memory of whether the question has been asked: it is already
   * persistent, already per-device, and not asking the browser to remember it
   * twice keeps this page's storage to the three keys it documents.
   */
  function offerPush() {
    if (pushAsked || editingLocked || !pushable() || sessionRole !== 'admin') return;
    if (Notification.permission !== 'default') return;
    var anchor = document.getElementById('addTaskRow');
    if (!anchor || !anchor.parentNode) return;
    pushAsked = true;

    var note = document.createElement('p');
    note.className = 'empty-note';
    note.setAttribute('role', 'status');
    // One plain sentence saying what will arrive, before anything is asked.
    note.appendChild(document.createTextNode(t('n.offer') + ' '));

    function button(label, fn) {
      var b = document.createElement('button');
      b.className = 'ghost-btn';
      b.type = 'button';
      b.textContent = label;
      b.style.marginLeft = '8px';
      b.addEventListener('click', fn);
      note.appendChild(b);
      return b;
    }

    button(t('n.on'), function () {
      note.remove();
      // Inside the click, so the browser still counts it as a gesture.
      // "Blocked" only when that is what happened. Everything else that is
      // not "on" — a worker still installing, one that failed, a browser with
      // push switched off — has its own sentence on the account screen, and
      // calling those "blocked" sends somebody to a setting that is fine.
      pushEnable().then(function (now) {
        flash(t(now === 'on' ? 'n.done' : now === 'blocked' ? 'n.no' : 'n.why'));
      });
    });
    button(t('n.later'), function () { note.remove(); });

    anchor.parentNode.insertBefore(note, anchor);
  }

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
  //
  // Non-fatal, and no longer silent: the rejection is kept, because reminders
  // wait on this worker and "it did not register" is a different sentence from
  // "this browser cannot". A browser set to block cookies and site data for the
  // site refuses with a security error (so does a private window in some);
  // anything else — a worker that does not parse, a bad answer from the server
  // — is this site's fault and is said to be. The name is all that is read:
  // the message carries the address and goes nowhere.
  if ('serviceWorker' in navigator) {
    var registerWorker = function () {
      var attempt;
      try { attempt = navigator.serviceWorker.register('/sw.js'); }
      catch (e) { attempt = Promise.reject(e); }
      Promise.resolve(attempt).then(function (reg) {
        // Registered is not installed. A first worker whose install fails goes
        // "redundant" and that is the end of it: nothing rejects, `ready`
        // never settles, and it is the same silence by another door.
        var first = reg && !reg.active && (reg.installing || reg.waiting);
        if (!first) return;
        first.addEventListener('statechange', function () {
          if (first.state !== 'redundant' || reg.active) return;
          swProblem = 'failed';
          swGaveUp();
        });
      }, function (err) {
        var name = (err && err.name) || '';
        swProblem = /^(SecurityError|NotAllowedError|NotSupportedError)$/.test(name) ? 'storage' : 'failed';
        swGaveUp();
      });
    };
    // `load` has usually not fired yet. Where it has, waiting for it is
    // waiting for ever, and the page would never have a worker at all.
    if (document.readyState === 'complete') registerWorker();
    else window.addEventListener('load', registerWorker);
  }
})();
