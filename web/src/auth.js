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
   * Interface language
   * ------------------------------------------------------------------
   * The same three languages as the planner, and the same choice of which.
   * app.js resolves it — ?lang= in the URL, then the reader's own browser, then
   * the deployment's locale, then English — and writes the answer to
   * <html lang>. It is a deferred script
   * ahead of this one in the document, so by the time this line runs the
   * answer is there; reading it, rather than resolving it a second time, is
   * what keeps the sign-in screen from ever disagreeing with the page behind
   * it. Should app.js not have run, the template's own lang="en" is what is
   * found, and English is the right fallback.
   *
   * The table is this file's own rather than more keys in app.js's. A
   * deployment with no database loads the planner and never draws any of
   * this, and should not carry a hundred strings for screens it does not have.
   *
   * English is the fallback for a missing language and for a missing key, so a
   * half-finished translation ships a mixed screen rather than raw ids.
   *
   * The first screen somebody invited to a planner ever sees is one of these.
   * For a long time it was the one part of the application that was not in
   * their language.
   * ------------------------------------------------------------------ */

  var STRINGS = {
    en: {
      'signin': 'Sign in',
      'signout': 'Sign out',
      'bar.people': 'People',
      'bar.account': 'Your account',
      'role.admin': 'admin',
      'role.editor': 'editor',
      'role.viewer': 'viewer, read-only',
      'back': 'Back to the planner',
      'signout.unsent': 'Some of your changes have not reached the server. Signing out removes this browser\'s copy of the planner, and those changes with it. Sign out anyway?',
      'signout.failed': 'Signing out did not reach the server, so you are still signed in. Check your connection and try again.',
      'f.email': 'Email',
      'f.password': 'Password',
      'login.passkey': 'Use a passkey',
      'login.forgot': 'Email me a link to set a new password',
      'login.lede': 'Sign in with the address your invitation was sent to.',
      'login.ended': 'Your session has ended. Sign in again — what you changed is still here, and is sent as soon as you are back.',
      'login.lede.admin': 'Sign in with an admin account to manage who has access.',
      'login.lede.account': 'Sign in to reach your account.',
      'login.need': 'Fill in both your email and your password.',
      'login.nomatch': 'That email and password do not match an account.',
      'forgot.need': 'Enter your email first, then ask for a link.',
      'forgot.sent': 'If that address has an account, a link is on its way. It works once and expires in 24 hours.',
      'wait.title': 'One moment',
      'wait.body': 'Checking whether you are signed in.',
      'setpw.title': 'Choose a password',
      'setpw.lede': 'This link works once. Once it is used, every session on this account is signed out.',
      'setpw.new': 'New password',
      'setpw.hint': 'At least 12 characters. Length is what makes a password hard to guess, so a short sentence beats a short scramble.',
      'setpw.again': 'Type it again',
      'setpw.show': 'Show password',
      'setpw.hide': 'Hide password',
      'setpw.submit': 'Set password',
      'setpw.incomplete.open': 'This link is incomplete. Open the whole link from your email, or ask an admin for a new one.',
      'setpw.incomplete': 'This link is incomplete. Ask an admin for a new one.',
      'setpw.mismatch': 'The two passwords are not the same.',
      'setpw.short': 'A password needs at least 12 characters.',
      'setpw.weak': 'Choose a password between 12 and 1024 characters.',
      'setpw.expired': 'This link has expired or has already been used. Ask an admin for a new one.',
      'setpw.done.body': 'Your password is saved. Sign in with it to reach the planner.',
      'people.title': 'People',
      'people.lede': 'Everyone with an account on this planner.',
      'people.notadmin': 'Managing accounts is an admin\'s job. Ask an admin to make the change for you.',
      'bar.activity': 'Activity',
      'act.title': 'Activity',
      'act.lede': 'Every change to the plan, newest first.',
      'act.notadmin': 'The activity names everybody with an account, so it is for admins. Ask an admin what you want to know.',
      'login.lede.activity': 'Sign in with an admin account to see what has changed.',
      'act.empty': 'Nothing has changed yet.',
      'act.more': 'Show older',
      'act.retry': 'Try again',
      'act.failed': 'Could not load the activity. Try again.',
      'act.s.create': 'Added {e} {l}',
      'act.s.update': 'Changed {e} {l}',
      'act.s.delete': 'Removed {e} {l}',
      'act.e.attachments': 'file',
      'act.e.budget_items': 'budget line',
      'act.e.notes': 'note',
      'act.e.phases': 'phase',
      'act.e.programme_entries': 'programme entry',
      'act.e.settings': 'the settings',
      'act.e.sponsors': 'sponsor',
      'act.e.tasks': 'task',
      'act.e.users': 'account',
      'act.k.system': 'The system',
      'act.k.import': 'An import',
      'act.k.unknown': 'Nobody signed in',
      'act.k.gone': 'A deleted account',
      'act.none': 'nothing',
      'act.hidden': 'changed, not shown',
      'act.items': '{n} chosen',
      'act.yes': 'yes',
      'act.no': 'no',
      'act.f.item': 'Item',
      'act.f.vendor': 'Vendor',
      'act.f.unit': 'Unit price',
      'act.f.qty': 'Quantity',
      'act.f.paid': 'Paid',
      'act.f.lockBy': 'Decide by',
      'act.f.note': 'Remarks',
      'act.f.name': 'Name',
      'act.f.owner': 'Owner',
      'act.f.due': 'Due date',
      'act.f.status': 'Status',
      'act.f.code': 'Callsign',
      'act.f.text': 'Text',
      'act.f.ceiling': 'Ceiling',
      'act.f.inflationPct': 'Inflation buffer',
      'act.f.fxRate': 'Exchange rate',
      'act.f.splitEvenly': 'Split evenly',
      'act.f.sponsorIds': 'Cost by',
      'act.f.position': 'Order',
      'act.f.email': 'Email',
      'act.f.role': 'Role',
      'act.f.passwordHash': 'Password',
      'people.empty': 'No accounts yet.',
      'people.role': 'Role',
      'people.add': 'Add person',
      'people.roles': 'A viewer reads the ledger. An editor changes it. An admin also decides who has an account.',
      'people.needemail': 'Enter the address the invitation should go to.',
      'people.language': 'Language',
      'people.language.hint': 'The language of the invitation, and of the screen it opens. It is not kept: after that, the planner follows their own browser.',
      'lang.default': 'Same as the planner ({name})',
      'person.language.aria': 'Language of the next mail to {email}',
      'e.invalid_language': 'That language has no translation here.',
      'opt.viewer': 'Viewer',
      'opt.editor': 'Editor',
      'opt.admin': 'Admin',
      'status.invited': 'invited',
      'status.active': 'active',
      'status.disabled': 'no access',
      'person.you': 'you',
      'person.added': 'added {date}',
      'person.role.aria': 'Role for {email}',
      'person.resend': 'Send the link again',
      'person.sendlink': 'Send a password link',
      'person.access.on': 'Turn access back on',
      'person.access.off': 'Turn off access',
      'remove': 'Remove',
      'person.confirm': 'Remove the account for {email}? This cannot be undone.',
      'person.conflict': 'Somebody else changed that account first. The list now shows where it stands.',
      'person.conflict.retry': 'Somebody else changed that account first. Check it and try again.',
      'person.removed': 'Removed {email}.',
      'person.gone': 'That account no longer exists. It has gone off the list.',
      'invite.sent': 'A set-password link is on its way to {email}.',
      'invite.manual': 'No mail is configured on this deployment, so send this link to {email} yourself. It works once, expires in 24 hours, and is not shown again.',
      'link.aria': 'Set-password link',
      'link.copy': 'Copy link',
      'link.copied': 'Copied',
      'link.copyfail': 'Press Ctrl+C to copy',
      'link.done': 'Done',
      'e.no_password': 'That account has never set a password, so it cannot be made active. Send it a link instead.',
      'e.self_change': 'Change your own role or status from another admin\'s account.',
      'e.email_taken': 'That address already has an account.',
      'e.account_disabled': 'Turn access back on before sending a link.',
      'e.invalid_email': 'That is not an address the server will accept.',
      'e.link_failed': 'The account was created but no link could be issued. Try sending one again.',
      'account.title': 'Your account',
      'account.lede': '{email}, signed in as {role}.',
      'pk.title': 'Passkeys',
      'pk.body': 'A passkey signs you in with the fingerprint reader or screen lock on a device you already have. It never leaves that device, and it cannot be phished or reused anywhere else.',
      'pk.empty': 'No passkeys on this account yet.',
      'pk.name': 'Name this device',
      'pk.placeholder': 'Phone',
      'pk.add': 'Add a passkey',
      'pk.unnamed': 'Unnamed passkey',
      'pk.this': 'this passkey',
      'pk.added': 'added {date}',
      'pk.lastused': 'last used {date}',
      'pk.unused': 'not used yet',
      'pk.confirm': 'Remove {label}? Signing in with that device will stop working.',
      'pk.removed': 'Removed.',
      'pk.gone': 'That passkey is already gone. It has gone off the list.',
      'pk.follow': 'Follow the prompt from your device.',
      'pk.ok': 'Passkey added. You can sign in with it from now on.',
      'pk.e.device': 'This device already has a passkey for this account.',
      'pk.e.origin': 'This page\'s address does not match the one passkeys were set up for.',
      'pk.e.toomany': 'This account already holds as many passkeys as it may. Remove one first.',
      'pk.e.readsetup': 'This browser could not read the setup this server offered.',
      'pk.e.readchallenge': 'This browser could not read the challenge this server offered.',
      'pk.e.exists': 'That passkey is already registered.',
      'pk.e.slow': 'That took too long. Try adding it again.',
      'pk.e.verify': 'That registration could not be verified. Try again.',
      'pk.e.setup': 'Your device did not complete the setup. Try again.',
      'pk.e.nologin': 'That passkey did not sign you in. Try your password instead.',
      'pk.e.signin': 'Your device did not complete the sign-in. Try again.',
      'pk.e.blocked': 'This browser refused to show the passkey prompt. Tap the button once more; if it keeps happening, use your password.',
      'pk.e.dismissed': 'The passkey prompt was closed or timed out before it finished.',
      'pk.e.unsupported': 'This device cannot make the kind of passkey this site uses.',
      'pk.e.constraint': 'This device cannot store a passkey, or has no screen lock to protect one with.',
      'pk.e.abort': 'The passkey prompt was interrupted. Try again.',
      'pk.e.kind': '{message} ({kind})',
      'rem.title': 'Reminders',
      'rem.body': 'One notification on this device when a deadline is near, listing what is coming up. Reminders go to admins, and each device is turned on by itself.',
      'rem.on': 'Reminders are on for this device.',
      'rem.off': 'Reminders are off on this device.',
      'rem.starting': 'Reminders are still being set up on this device. The first time takes a moment; this line changes by itself when they are ready.',
      'rem.noworker.failed': 'Reminders could not start on this device. Reload the page. If this message stays, the fault is with this site and not with your browser: please tell whoever runs the site.',
      'rem.noworker.storage': 'This browser did not let the page set up reminders. That happens when cookies and site data are blocked for this site, and in some private windows. Allow them for this site, then reload the page.',
      'rem.blocked': 'Notifications are blocked for this site. Click or tap the icon at the start of the address bar, find Notifications, and choose Allow. Then reload this page. A private or incognito window always blocks them, so use an ordinary window.',
      'rem.blocked.ios': 'Notifications are turned off for this app. Open the Settings app, tap Notifications, find this app in the list, and turn on Allow Notifications. Then come back here.',
      'rem.blocked.safari': 'Notifications are blocked for this site. In Safari, open Settings, choose Websites, then Notifications, and set this site to Allow. Then reload this page.',
      'rem.refused': 'You said yes, but this browser could not turn reminders on. Either it cannot reach its notification service right now, or notifications from web pages are switched off in its settings. Try again in a moment. If this stays, look in the browser\'s settings.',
      'rem.refused.brave': 'Brave keeps reminders off until you allow them. Open Brave\'s Settings, choose Privacy and security, and turn on “Use Google services for push messaging”. Then reload this page and try again.',
      'rem.unsupported': 'This browser cannot receive reminders from a web page. If this is a private window, try an ordinary one. Otherwise a current Chrome, Edge, Firefox or Safari can.',
      'rem.insecure': 'Reminders only work over a secure connection, where the address starts with https.',
      'rem.ios.safari': 'An iPhone or iPad only delivers reminders to a page that is on the Home Screen. Tap the Share button (the square with an arrow), choose “Add to Home Screen”, then open the planner from its new icon. Sign in there once more and turn reminders on. If you do not see “Add to Home Screen”, open this page in Safari first.',
      'rem.ios.other': 'On an iPhone or iPad, reminders start in Safari. Open this page in Safari, tap the Share button (the square with an arrow), choose “Add to Home Screen”, then open the planner from its new icon. Sign in there once more and turn reminders on.',
      'rem.ios.old': 'This iPhone or iPad needs iOS 16.4 or newer for reminders. Update it in the Settings app under General, Software Update, then look here again.',
      'rem.turnon': 'Turn on',
      'rem.turnoff': 'Turn off',
      'rem.failed': 'That did not work. Try again.',
      'tr.internal': 'built into a device',
      'tr.hybrid': 'a phone or tablet',
      'tr.usb': 'a security key',
      'tr.nfc': 'a tapped key',
      'tr.ble': 'a Bluetooth key',
      'tr.smart-card': 'a smart card',
      'tr.cable': 'a cabled key',
      'p.offline': 'No answer from the server. Check your connection and try again.',
      'p.rate': 'Too many attempts just now. Wait a minute, then try again.',
      'p.403': 'Your account is not allowed to do that.',
      'p.401': 'Sign in again — your session has ended.',
      'p.other': 'That did not work. Try again.'
    },
    nl: {
      'signin': 'Aanmelden',
      'signout': 'Afmelden',
      'bar.people': 'Mensen',
      'bar.account': 'Je account',
      'role.admin': 'beheerder',
      'role.editor': 'bewerker',
      'role.viewer': 'lezer, alleen lezen',
      'back': 'Terug naar de planner',
      'signout.unsent': 'Een deel van je wijzigingen heeft de server niet bereikt. Afmelden verwijdert de kopie van de planner in deze browser, en die wijzigingen ook. Toch afmelden?',
      'signout.failed': 'Het afmelden heeft de server niet bereikt, dus je bent nog aangemeld. Controleer je verbinding en probeer het opnieuw.',
      'f.email': 'E-mailadres',
      'f.password': 'Wachtwoord',
      'login.passkey': 'Een passkey gebruiken',
      'login.forgot': 'Mail me een link om een nieuw wachtwoord in te stellen',
      'login.lede': 'Meld je aan met het adres waarop je de uitnodiging hebt ontvangen.',
      'login.ended': 'Je sessie is verlopen. Meld je opnieuw aan — je wijzigingen staan er nog en worden verstuurd zodra je terug bent.',
      'login.lede.admin': 'Meld je aan met een beheerdersaccount om te regelen wie toegang heeft.',
      'login.lede.account': 'Meld je aan om bij je account te komen.',
      'login.need': 'Vul zowel je e-mailadres als je wachtwoord in.',
      'login.nomatch': 'Dat e-mailadres en wachtwoord horen niet bij een account.',
      'forgot.need': 'Vul eerst je e-mailadres in en vraag dan een link aan.',
      'forgot.sent': 'Als er een account bij dat adres hoort, is er een link onderweg. Hij werkt één keer en verloopt na 24 uur.',
      'wait.title': 'Een ogenblik',
      'wait.body': 'Er wordt gekeken of je bent aangemeld.',
      'setpw.title': 'Kies een wachtwoord',
      'setpw.lede': 'Deze link werkt één keer. Zodra hij is gebruikt, worden alle sessies van dit account afgemeld.',
      'setpw.new': 'Nieuw wachtwoord',
      'setpw.hint': 'Minstens 12 tekens. Lengte maakt een wachtwoord moeilijk te raden, dus een korte zin is beter dan een korte brij tekens.',
      'setpw.again': 'Typ het nog een keer',
      'setpw.show': 'Wachtwoord tonen',
      'setpw.hide': 'Wachtwoord verbergen',
      'setpw.submit': 'Wachtwoord instellen',
      'setpw.incomplete.open': 'Deze link is onvolledig. Open de hele link uit je e-mail, of vraag een beheerder om een nieuwe.',
      'setpw.incomplete': 'Deze link is onvolledig. Vraag een beheerder om een nieuwe.',
      'setpw.mismatch': 'De twee wachtwoorden zijn niet gelijk.',
      'setpw.short': 'Een wachtwoord heeft minstens 12 tekens nodig.',
      'setpw.weak': 'Kies een wachtwoord van 12 tot 1024 tekens.',
      'setpw.expired': 'Deze link is verlopen of al gebruikt. Vraag een beheerder om een nieuwe.',
      'setpw.done.body': 'Je wachtwoord is opgeslagen. Meld je ermee aan om bij de planner te komen.',
      'people.title': 'Mensen',
      'people.lede': 'Iedereen met een account op deze planner.',
      'people.notadmin': 'Accounts beheren is het werk van een beheerder. Vraag een beheerder om de wijziging voor je te doen.',
      'bar.activity': 'Activiteit',
      'act.title': 'Activiteit',
      'act.lede': 'Elke wijziging in het plan, de nieuwste eerst.',
      'act.notadmin': 'De activiteit noemt iedereen met een account en is daarom voor beheerders. Vraag een beheerder wat je wilt weten.',
      'login.lede.activity': 'Meld je aan met een beheerdersaccount om te zien wat er is gewijzigd.',
      'act.empty': 'Er is nog niets gewijzigd.',
      'act.more': 'Ouder tonen',
      'act.retry': 'Opnieuw proberen',
      'act.failed': 'De activiteit kon niet worden geladen. Probeer het opnieuw.',
      'act.s.create': '{e} {l} toegevoegd',
      'act.s.update': '{e} {l} gewijzigd',
      'act.s.delete': '{e} {l} verwijderd',
      'act.e.attachments': 'bestand',
      'act.e.budget_items': 'budgetpost',
      'act.e.notes': 'notitie',
      'act.e.phases': 'fase',
      'act.e.programme_entries': 'programmaonderdeel',
      'act.e.settings': 'de instellingen',
      'act.e.sponsors': 'sponsor',
      'act.e.tasks': 'taak',
      'act.e.users': 'account',
      'act.k.system': 'Het systeem',
      'act.k.import': 'Een import',
      'act.k.unknown': 'Niemand aangemeld',
      'act.k.gone': 'Een verwijderd account',
      'act.none': 'niets',
      'act.hidden': 'gewijzigd, niet getoond',
      'act.items': '{n} gekozen',
      'act.yes': 'ja',
      'act.no': 'nee',
      'act.f.item': 'Post',
      'act.f.vendor': 'Leverancier',
      'act.f.unit': 'Prijs per stuk',
      'act.f.qty': 'Aantal',
      'act.f.paid': 'Betaald',
      'act.f.lockBy': 'Beslissen voor',
      'act.f.note': 'Opmerkingen',
      'act.f.name': 'Naam',
      'act.f.owner': 'Eigenaar',
      'act.f.due': 'Deadline',
      'act.f.status': 'Status',
      'act.f.code': 'Roepnaam',
      'act.f.text': 'Tekst',
      'act.f.ceiling': 'Plafond',
      'act.f.inflationPct': 'Inflatiebuffer',
      'act.f.fxRate': 'Wisselkoers',
      'act.f.splitEvenly': 'Gelijk verdelen',
      'act.f.sponsorIds': 'Betaald door',
      'act.f.position': 'Volgorde',
      'act.f.email': 'E-mail',
      'act.f.role': 'Rol',
      'act.f.passwordHash': 'Wachtwoord',
      'people.empty': 'Nog geen accounts.',
      'people.role': 'Rol',
      'people.add': 'Persoon toevoegen',
      'people.roles': 'Een lezer bekijkt het kasboek. Een bewerker past het aan. Een beheerder bepaalt ook wie een account heeft.',
      'people.needemail': 'Vul het adres in waar de uitnodiging naartoe moet.',
      'people.language': 'Taal',
      'people.language.hint': 'De taal van de uitnodiging, en van het scherm dat ze opent. Ze wordt niet bewaard: daarna volgt de planner hun eigen browser.',
      'lang.default': 'Zelfde als de planner ({name})',
      'person.language.aria': 'Taal van de volgende mail aan {email}',
      'e.invalid_language': 'Voor die taal is hier geen vertaling.',
      'opt.viewer': 'Lezer',
      'opt.editor': 'Bewerker',
      'opt.admin': 'Beheerder',
      'status.invited': 'uitgenodigd',
      'status.active': 'actief',
      'status.disabled': 'geen toegang',
      'person.you': 'jij',
      'person.added': 'toegevoegd op {date}',
      'person.role.aria': 'Rol van {email}',
      'person.resend': 'Link opnieuw sturen',
      'person.sendlink': 'Wachtwoordlink sturen',
      'person.access.on': 'Toegang weer aanzetten',
      'person.access.off': 'Toegang uitzetten',
      'remove': 'Verwijderen',
      'person.confirm': 'Het account van {email} verwijderen? Dit kan niet ongedaan worden gemaakt.',
      'person.conflict': 'Iemand anders heeft dat account eerder gewijzigd. De lijst toont nu hoe het ervoor staat.',
      'person.conflict.retry': 'Iemand anders heeft dat account eerder gewijzigd. Controleer het en probeer het opnieuw.',
      'person.removed': '{email} is verwijderd.',
      'person.gone': 'Dat account bestaat niet meer. Het staat niet meer in de lijst.',
      'invite.sent': 'Er is een link om een wachtwoord in te stellen onderweg naar {email}.',
      'invite.manual': 'Op deze installatie is geen mail ingesteld, dus stuur deze link zelf naar {email}. Hij werkt één keer, verloopt na 24 uur en wordt niet opnieuw getoond.',
      'link.aria': 'Link om een wachtwoord in te stellen',
      'link.copy': 'Link kopiëren',
      'link.copied': 'Gekopieerd',
      'link.copyfail': 'Druk op Ctrl+C om te kopiëren',
      'link.done': 'Klaar',
      'e.no_password': 'Dat account heeft nooit een wachtwoord ingesteld en kan dus niet actief worden gemaakt. Stuur het in plaats daarvan een link.',
      'e.self_change': 'Je eigen rol of status wijzig je vanuit het account van een andere beheerder.',
      'e.email_taken': 'Dat adres heeft al een account.',
      'e.account_disabled': 'Zet de toegang eerst weer aan voordat je een link stuurt.',
      'e.invalid_email': 'Dat is geen adres dat de server accepteert.',
      'e.link_failed': 'Het account is aangemaakt, maar er kon geen link worden gemaakt. Probeer er opnieuw een te sturen.',
      'account.title': 'Je account',
      'account.lede': '{email}, aangemeld als {role}.',
      'pk.title': 'Passkeys',
      'pk.body': 'Met een passkey meld je je aan met de vingerafdruklezer of schermvergrendeling van een apparaat dat je al hebt. Hij verlaat dat apparaat nooit, en kan niet met phishing worden buitgemaakt of ergens anders worden hergebruikt.',
      'pk.empty': 'Nog geen passkeys bij dit account.',
      'pk.name': 'Geef dit apparaat een naam',
      'pk.placeholder': 'Telefoon',
      'pk.add': 'Passkey toevoegen',
      'pk.unnamed': 'Passkey zonder naam',
      'pk.this': 'deze passkey',
      'pk.added': 'toegevoegd op {date}',
      'pk.lastused': 'laatst gebruikt op {date}',
      'pk.unused': 'nog niet gebruikt',
      'pk.confirm': '{label} verwijderen? Aanmelden met dat apparaat werkt dan niet meer.',
      'pk.removed': 'Verwijderd.',
      'pk.gone': 'Die passkey is er al niet meer. Hij staat niet meer in de lijst.',
      'pk.follow': 'Volg de aanwijzingen van je apparaat.',
      'pk.ok': 'Passkey toegevoegd. Je kunt je er vanaf nu mee aanmelden.',
      'pk.e.device': 'Dit apparaat heeft al een passkey voor dit account.',
      'pk.e.origin': 'Het adres van deze pagina komt niet overeen met het adres waarvoor passkeys zijn ingesteld.',
      'pk.e.toomany': 'Dit account heeft al het maximale aantal passkeys. Verwijder er eerst een.',
      'pk.e.readsetup': 'Deze browser kon de instellingen van de server niet lezen.',
      'pk.e.readchallenge': 'Deze browser kon de vraag van de server niet lezen.',
      'pk.e.exists': 'Die passkey is al geregistreerd.',
      'pk.e.slow': 'Dat duurde te lang. Probeer hem opnieuw toe te voegen.',
      'pk.e.verify': 'Die registratie kon niet worden gecontroleerd. Probeer het opnieuw.',
      'pk.e.setup': 'Je apparaat heeft de installatie niet afgerond. Probeer het opnieuw.',
      'pk.e.nologin': 'Met die passkey ben je niet aangemeld. Probeer je wachtwoord.',
      'pk.e.signin': 'Je apparaat heeft het aanmelden niet afgerond. Probeer het opnieuw.',
      'pk.e.blocked': 'Deze browser weigerde het passkey-venster te tonen. Tik nog een keer op de knop; blijft het gebeuren, gebruik dan je wachtwoord.',
      'pk.e.dismissed': 'Het passkey-venster is gesloten of verlopen voordat het klaar was.',
      'pk.e.unsupported': 'Dit apparaat kan het soort passkey dat deze site gebruikt niet maken.',
      'pk.e.constraint': 'Dit apparaat kan geen passkey bewaren, of heeft geen schermvergrendeling om hem mee te beveiligen.',
      'pk.e.abort': 'Het passkey-venster werd onderbroken. Probeer het opnieuw.',
      'pk.e.kind': '{message} ({kind})',
      'rem.title': 'Herinneringen',
      'rem.body': 'Eén melding op dit apparaat als een deadline nadert, met wat eraan komt. Herinneringen gaan naar beheerders, en elk apparaat zet je apart aan.',
      'rem.on': 'Herinneringen staan aan op dit apparaat.',
      'rem.off': 'Herinneringen staan uit op dit apparaat.',
      'rem.starting': 'Herinneringen worden op dit apparaat nog klaargezet. De eerste keer duurt dat even; deze regel verandert vanzelf als ze klaar zijn.',
      'rem.noworker.failed': 'Herinneringen konden op dit apparaat niet starten. Laad de pagina opnieuw. Blijft deze melding staan, dan ligt het aan deze site en niet aan je browser: geef het door aan wie de site beheert.',
      'rem.noworker.storage': 'Deze browser liet de pagina geen herinneringen klaarzetten. Dat gebeurt als cookies en sitegegevens voor deze site geblokkeerd zijn, en in sommige privévensters. Sta ze toe voor deze site en laad de pagina opnieuw.',
      'rem.blocked': 'Meldingen zijn geblokkeerd voor deze site. Klik of tik op het icoon aan het begin van de adresbalk, zoek Meldingen en kies Toestaan. Laad daarna deze pagina opnieuw. Een privé- of incognitovenster blokkeert ze altijd; gebruik dus een gewoon venster.',
      'rem.blocked.ios': 'Meldingen staan uit voor deze app. Open de app Instellingen, tik op Meldingen, zoek deze app in de lijst en zet Sta meldingen toe aan. Kom daarna hier terug.',
      'rem.blocked.safari': 'Meldingen zijn geblokkeerd voor deze site. Open in Safari de Instellingen, kies Websites en dan Meldingen, en zet deze site op Sta toe. Laad daarna deze pagina opnieuw.',
      'rem.refused': 'Je zei ja, maar deze browser kon de herinneringen niet aanzetten. Of hij kan zijn meldingendienst nu niet bereiken, of meldingen van webpagina\'s staan uit in zijn instellingen. Probeer het zo nog eens. Blijft dit staan, kijk dan in de instellingen van de browser.',
      'rem.refused.brave': 'Brave houdt herinneringen uit tot je ze toestaat. Open de instellingen van Brave, kies Privacy en beveiliging en zet “Google-services gebruiken voor pushberichten” aan (in het Engels: “Use Google services for push messaging”). Laad daarna deze pagina opnieuw en probeer het nog eens.',
      'rem.unsupported': 'Deze browser kan geen herinneringen van een webpagina ontvangen. Is dit een privévenster, probeer dan een gewoon venster. Anders lukt het met een recente Chrome, Edge, Firefox of Safari.',
      'rem.insecure': 'Herinneringen werken alleen over een beveiligde verbinding, waarbij het adres met https begint.',
      'rem.ios.safari': 'Een iPhone of iPad bezorgt herinneringen alleen aan een pagina die op het beginscherm staat. Tik op de deelknop (het vierkant met de pijl), kies “Zet op beginscherm” en open de planner daarna via het nieuwe icoon. Meld je daar nog één keer aan en zet de herinneringen aan. Zie je “Zet op beginscherm” niet, open deze pagina dan eerst in Safari.',
      'rem.ios.other': 'Op een iPhone of iPad beginnen herinneringen in Safari. Open deze pagina in Safari, tik op de deelknop (het vierkant met de pijl), kies “Zet op beginscherm” en open de planner daarna via het nieuwe icoon. Meld je daar nog één keer aan en zet de herinneringen aan.',
      'rem.ios.old': 'Deze iPhone of iPad heeft iOS 16.4 of nieuwer nodig voor herinneringen. Werk hem bij in de app Instellingen, onder Algemeen, Software-update, en kijk daarna hier opnieuw.',
      'rem.turnon': 'Aanzetten',
      'rem.turnoff': 'Uitzetten',
      'rem.failed': 'Dat is niet gelukt. Probeer het opnieuw.',
      'tr.internal': 'ingebouwd in een apparaat',
      'tr.hybrid': 'een telefoon of tablet',
      'tr.usb': 'een beveiligingssleutel',
      'tr.nfc': 'een NFC-sleutel',
      'tr.ble': 'een bluetooth-sleutel',
      'tr.smart-card': 'een smartcard',
      'tr.cable': 'een sleutel met kabel',
      'p.offline': 'Geen antwoord van de server. Controleer je verbinding en probeer het opnieuw.',
      'p.rate': 'Te veel pogingen achter elkaar. Wacht een minuut en probeer het dan opnieuw.',
      'p.403': 'Je account mag dat niet doen.',
      'p.401': 'Meld je opnieuw aan — je sessie is verlopen.',
      'p.other': 'Dat is niet gelukt. Probeer het opnieuw.'
    },
    id: {
      'signin': 'Masuk',
      'signout': 'Keluar',
      'bar.people': 'Anggota',
      'bar.account': 'Akunmu',
      'role.admin': 'admin',
      'role.editor': 'penyunting',
      'role.viewer': 'pembaca, hanya baca',
      'back': 'Kembali ke perencana',
      'signout.unsent': 'Sebagian perubahanmu belum sampai ke server. Keluar akan menghapus salinan perencana di browser ini, termasuk perubahan itu. Tetap keluar?',
      'signout.failed': 'Permintaan keluar tidak sampai ke server, jadi kamu masih masuk. Periksa koneksimu lalu coba lagi.',
      'f.email': 'Email',
      'f.password': 'Kata sandi',
      'login.passkey': 'Pakai kunci sandi',
      'login.forgot': 'Kirimi saya tautan untuk membuat kata sandi baru',
      'login.lede': 'Masuk dengan alamat email tempat undanganmu dikirim.',
      'login.ended': 'Sesimu sudah berakhir. Masuk lagi — perubahanmu masih ada di sini dan dikirim begitu kamu kembali.',
      'login.lede.admin': 'Masuk dengan akun admin untuk mengatur siapa yang punya akses.',
      'login.lede.account': 'Masuk untuk membuka akunmu.',
      'login.need': 'Isi email dan kata sandimu.',
      'login.nomatch': 'Email dan kata sandi itu tidak cocok dengan akun mana pun.',
      'forgot.need': 'Isi emailmu dulu, lalu minta tautan.',
      'forgot.sent': 'Kalau alamat itu punya akun, tautan sedang dikirim. Tautan hanya bisa dipakai sekali dan kedaluwarsa dalam 24 jam.',
      'wait.title': 'Sebentar',
      'wait.body': 'Memeriksa apakah kamu sudah masuk.',
      'setpw.title': 'Buat kata sandi',
      'setpw.lede': 'Tautan ini hanya bisa dipakai sekali. Setelah dipakai, semua sesi di akun ini dikeluarkan.',
      'setpw.new': 'Kata sandi baru',
      'setpw.hint': 'Minimal 12 karakter. Panjanglah yang membuat kata sandi sulit ditebak, jadi kalimat pendek lebih baik daripada acakan pendek.',
      'setpw.again': 'Ketik sekali lagi',
      'setpw.show': 'Tampilkan kata sandi',
      'setpw.hide': 'Sembunyikan kata sandi',
      'setpw.submit': 'Simpan kata sandi',
      'setpw.incomplete.open': 'Tautan ini tidak lengkap. Buka tautan utuh dari emailmu, atau minta yang baru ke admin.',
      'setpw.incomplete': 'Tautan ini tidak lengkap. Minta yang baru ke admin.',
      'setpw.mismatch': 'Kedua kata sandi tidak sama.',
      'setpw.short': 'Kata sandi harus minimal 12 karakter.',
      'setpw.weak': 'Pilih kata sandi antara 12 dan 1024 karakter.',
      'setpw.expired': 'Tautan ini sudah kedaluwarsa atau sudah dipakai. Minta yang baru ke admin.',
      'setpw.done.body': 'Kata sandimu sudah tersimpan. Masuk dengan kata sandi itu untuk membuka perencana.',
      'people.title': 'Anggota',
      'people.lede': 'Semua orang yang punya akun di perencana ini.',
      'people.notadmin': 'Mengelola akun adalah tugas admin. Minta admin untuk melakukan perubahan itu.',
      'bar.activity': 'Aktivitas',
      'act.title': 'Aktivitas',
      'act.lede': 'Setiap perubahan pada rencana, yang terbaru di atas.',
      'act.notadmin': 'Aktivitas menyebut semua orang yang punya akun, jadi ini untuk admin. Tanyakan kepada admin apa yang ingin Anda ketahui.',
      'login.lede.activity': 'Masuk dengan akun admin untuk melihat apa yang berubah.',
      'act.empty': 'Belum ada perubahan.',
      'act.more': 'Tampilkan yang lebih lama',
      'act.retry': 'Coba lagi',
      'act.failed': 'Aktivitas tidak dapat dimuat. Coba lagi.',
      'act.s.create': 'Menambahkan {e} {l}',
      'act.s.update': 'Mengubah {e} {l}',
      'act.s.delete': 'Menghapus {e} {l}',
      'act.e.attachments': 'berkas',
      'act.e.budget_items': 'baris anggaran',
      'act.e.notes': 'catatan',
      'act.e.phases': 'fase',
      'act.e.programme_entries': 'mata acara',
      'act.e.settings': 'pengaturan',
      'act.e.sponsors': 'sponsor',
      'act.e.tasks': 'tugas',
      'act.e.users': 'akun',
      'act.k.system': 'Sistem',
      'act.k.import': 'Impor',
      'act.k.unknown': 'Tidak ada yang masuk',
      'act.k.gone': 'Akun yang sudah dihapus',
      'act.none': 'kosong',
      'act.hidden': 'diubah, tidak ditampilkan',
      'act.items': '{n} dipilih',
      'act.yes': 'ya',
      'act.no': 'tidak',
      'act.f.item': 'Item',
      'act.f.vendor': 'Vendor',
      'act.f.unit': 'Harga satuan',
      'act.f.qty': 'Jumlah',
      'act.f.paid': 'Dibayar',
      'act.f.lockBy': 'Putuskan sebelum',
      'act.f.note': 'Keterangan',
      'act.f.name': 'Nama',
      'act.f.owner': 'Penanggung jawab',
      'act.f.due': 'Tenggat',
      'act.f.status': 'Status',
      'act.f.code': 'Nama panggilan',
      'act.f.text': 'Teks',
      'act.f.ceiling': 'Batas anggaran',
      'act.f.inflationPct': 'Cadangan inflasi',
      'act.f.fxRate': 'Kurs',
      'act.f.splitEvenly': 'Bagi rata',
      'act.f.sponsorIds': 'Ditanggung oleh',
      'act.f.position': 'Urutan',
      'act.f.email': 'Email',
      'act.f.role': 'Peran',
      'act.f.passwordHash': 'Kata sandi',
      'people.empty': 'Belum ada akun.',
      'people.role': 'Peran',
      'people.add': 'Tambah orang',
      'people.roles': 'Pembaca melihat buku kas. Penyunting mengubahnya. Admin juga menentukan siapa yang punya akun.',
      'people.needemail': 'Isi alamat tujuan undangan.',
      'people.language': 'Bahasa',
      'people.language.hint': 'Bahasa undangan, dan bahasa layar yang dibukanya. Pilihan ini tidak disimpan: setelah itu perencana mengikuti browser mereka sendiri.',
      'lang.default': 'Sama dengan perencana ({name})',
      'person.language.aria': 'Bahasa email berikutnya untuk {email}',
      'e.invalid_language': 'Bahasa itu belum ada terjemahannya di sini.',
      'opt.viewer': 'Pembaca',
      'opt.editor': 'Penyunting',
      'opt.admin': 'Admin',
      'status.invited': 'diundang',
      'status.active': 'aktif',
      'status.disabled': 'tanpa akses',
      'person.you': 'kamu',
      'person.added': 'ditambahkan {date}',
      'person.role.aria': 'Peran untuk {email}',
      'person.resend': 'Kirim ulang tautan',
      'person.sendlink': 'Kirim tautan kata sandi',
      'person.access.on': 'Aktifkan lagi akses',
      'person.access.off': 'Matikan akses',
      'remove': 'Hapus',
      'person.confirm': 'Hapus akun {email}? Ini tidak bisa dibatalkan.',
      'person.conflict': 'Orang lain lebih dulu mengubah akun itu. Daftar ini sekarang menunjukkan keadaan terbarunya.',
      'person.conflict.retry': 'Orang lain lebih dulu mengubah akun itu. Periksa lalu coba lagi.',
      'person.removed': '{email} sudah dihapus.',
      'person.gone': 'Akun itu sudah tidak ada dan sudah hilang dari daftar.',
      'invite.sent': 'Tautan untuk membuat kata sandi sedang dikirim ke {email}.',
      'invite.manual': 'Email belum diatur di instalasi ini, jadi kirim sendiri tautan ini ke {email}. Tautan hanya bisa dipakai sekali, kedaluwarsa dalam 24 jam, dan tidak ditampilkan lagi.',
      'link.aria': 'Tautan untuk membuat kata sandi',
      'link.copy': 'Salin tautan',
      'link.copied': 'Tersalin',
      'link.copyfail': 'Tekan Ctrl+C untuk menyalin',
      'link.done': 'Selesai',
      'e.no_password': 'Akun itu belum pernah membuat kata sandi, jadi tidak bisa diaktifkan. Kirimi tautan saja.',
      'e.self_change': 'Ubah peran atau statusmu sendiri lewat akun admin lain.',
      'e.email_taken': 'Alamat itu sudah punya akun.',
      'e.account_disabled': 'Aktifkan lagi aksesnya sebelum mengirim tautan.',
      'e.invalid_email': 'Itu bukan alamat yang diterima server.',
      'e.link_failed': 'Akun sudah dibuat, tetapi tautan tidak bisa diterbitkan. Coba kirim lagi.',
      'account.title': 'Akunmu',
      'account.lede': '{email}, masuk sebagai {role}.',
      'pk.title': 'Kunci sandi (passkey)',
      'pk.body': 'Dengan kunci sandi kamu masuk memakai pemindai sidik jari atau kunci layar di perangkat yang sudah kamu punya. Kunci itu tidak pernah keluar dari perangkat tersebut, tidak bisa dicuri lewat phishing, dan tidak bisa dipakai di tempat lain.',
      'pk.empty': 'Belum ada kunci sandi di akun ini.',
      'pk.name': 'Beri nama perangkat ini',
      'pk.placeholder': 'Ponsel',
      'pk.add': 'Tambah kunci sandi',
      'pk.unnamed': 'Kunci sandi tanpa nama',
      'pk.this': 'kunci sandi ini',
      'pk.added': 'ditambahkan {date}',
      'pk.lastused': 'terakhir dipakai {date}',
      'pk.unused': 'belum pernah dipakai',
      'pk.confirm': 'Hapus {label}? Masuk dengan perangkat itu tidak akan bisa lagi.',
      'pk.removed': 'Sudah dihapus.',
      'pk.gone': 'Kunci sandi itu sudah tidak ada dan sudah hilang dari daftar.',
      'pk.follow': 'Ikuti petunjuk dari perangkatmu.',
      'pk.ok': 'Kunci sandi ditambahkan. Mulai sekarang kamu bisa masuk dengannya.',
      'pk.e.device': 'Perangkat ini sudah punya kunci sandi untuk akun ini.',
      'pk.e.origin': 'Alamat halaman ini tidak sama dengan alamat tempat kunci sandi diatur.',
      'pk.e.toomany': 'Akun ini sudah punya kunci sandi sebanyak yang diizinkan. Hapus satu dulu.',
      'pk.e.readsetup': 'Browser ini tidak bisa membaca pengaturan yang ditawarkan server.',
      'pk.e.readchallenge': 'Browser ini tidak bisa membaca tantangan yang ditawarkan server.',
      'pk.e.exists': 'Kunci sandi itu sudah terdaftar.',
      'pk.e.slow': 'Terlalu lama. Coba tambahkan lagi.',
      'pk.e.verify': 'Pendaftaran itu tidak bisa diverifikasi. Coba lagi.',
      'pk.e.setup': 'Perangkatmu tidak menyelesaikan pengaturan. Coba lagi.',
      'pk.e.nologin': 'Kunci sandi itu tidak berhasil memasukkanmu. Coba pakai kata sandi.',
      'pk.e.signin': 'Perangkatmu tidak menyelesaikan proses masuk. Coba lagi.',
      'pk.e.blocked': 'Browser ini menolak menampilkan permintaan kunci sandi. Ketuk tombolnya sekali lagi; kalau terus terjadi, pakai kata sandi.',
      'pk.e.dismissed': 'Permintaan kunci sandi ditutup atau kehabisan waktu sebelum selesai.',
      'pk.e.unsupported': 'Perangkat ini tidak bisa membuat jenis kunci sandi yang dipakai situs ini.',
      'pk.e.constraint': 'Perangkat ini tidak bisa menyimpan kunci sandi, atau tidak punya kunci layar untuk melindunginya.',
      'pk.e.abort': 'Permintaan kunci sandi terputus. Coba lagi.',
      'pk.e.kind': '{message} ({kind})',
      'rem.title': 'Pengingat',
      'rem.body': 'Satu notifikasi di perangkat ini kalau tenggat sudah dekat, berisi apa yang akan datang. Pengingat dikirim ke admin, dan setiap perangkat dinyalakan sendiri-sendiri.',
      'rem.on': 'Pengingat aktif di perangkat ini.',
      'rem.off': 'Pengingat tidak aktif di perangkat ini.',
      'rem.starting': 'Pengingat masih disiapkan di perangkat ini. Pertama kali memang butuh waktu sebentar; baris ini berubah sendiri kalau sudah siap.',
      'rem.noworker.failed': 'Pengingat tidak bisa dimulai di perangkat ini. Muat ulang halaman. Kalau pesan ini tetap muncul, masalahnya ada di situs ini, bukan di browser kamu: tolong beri tahu pengelola situs.',
      'rem.noworker.storage': 'Browser ini tidak mengizinkan halaman menyiapkan pengingat. Itu terjadi kalau cookie dan data situs diblokir untuk situs ini, dan di sebagian jendela pribadi. Izinkan untuk situs ini, lalu muat ulang halaman.',
      'rem.blocked': 'Notifikasi diblokir untuk situs ini. Klik atau ketuk ikon di awal bilah alamat, cari Notifikasi, lalu pilih Izinkan. Setelah itu muat ulang halaman ini. Jendela pribadi atau penyamaran selalu memblokirnya, jadi pakai jendela biasa.',
      'rem.blocked.ios': 'Notifikasi dimatikan untuk aplikasi ini. Buka app Pengaturan, ketuk Pemberitahuan, cari aplikasi ini di daftar, lalu nyalakan Izinkan Pemberitahuan. Setelah itu kembali ke sini.',
      'rem.blocked.safari': 'Notifikasi diblokir untuk situs ini. Di Safari, buka Pengaturan, pilih Situs Web, lalu Pemberitahuan, dan setel situs ini ke Izinkan. Setelah itu muat ulang halaman ini.',
      'rem.refused': 'Kamu sudah bilang ya, tapi browser ini tidak bisa menyalakan pengingat. Mungkin layanan notifikasinya sedang tidak terjangkau, atau notifikasi dari halaman web dimatikan di pengaturannya. Coba lagi sebentar lagi. Kalau tetap begini, periksa pengaturan browser.',
      'rem.refused.brave': 'Brave mematikan pengingat sampai kamu mengizinkannya. Buka pengaturan Brave, pilih Privasi dan keamanan, lalu nyalakan “Use Google services for push messaging” (layanan Google untuk pesan push). Setelah itu muat ulang halaman ini dan coba lagi.',
      'rem.unsupported': 'Browser ini tidak bisa menerima pengingat dari halaman web. Kalau ini jendela pribadi, coba jendela biasa. Kalau bukan, Chrome, Edge, Firefox, atau Safari versi terbaru bisa.',
      'rem.insecure': 'Pengingat hanya berfungsi lewat koneksi aman, yaitu kalau alamatnya diawali https.',
      'rem.ios.safari': 'iPhone atau iPad hanya mengirim pengingat ke halaman yang ada di Layar Utama. Ketuk tombol Bagikan (kotak dengan panah), pilih “Tambah ke Layar Utama”, lalu buka planner dari ikon barunya. Masuk sekali lagi di sana dan nyalakan pengingat. Kalau “Tambah ke Layar Utama” tidak terlihat, buka dulu halaman ini di Safari.',
      'rem.ios.other': 'Di iPhone atau iPad, pengingat dimulai dari Safari. Buka halaman ini di Safari, ketuk tombol Bagikan (kotak dengan panah), pilih “Tambah ke Layar Utama”, lalu buka planner dari ikon barunya. Masuk sekali lagi di sana dan nyalakan pengingat.',
      'rem.ios.old': 'iPhone atau iPad ini butuh iOS 16.4 atau yang lebih baru untuk pengingat. Perbarui lewat app Pengaturan, di Umum, Pembaruan Perangkat Lunak, lalu lihat lagi di sini.',
      'rem.turnon': 'Nyalakan',
      'rem.turnoff': 'Matikan',
      'rem.failed': 'Itu tidak berhasil. Coba lagi.',
      'tr.internal': 'tertanam di perangkat',
      'tr.hybrid': 'ponsel atau tablet',
      'tr.usb': 'kunci keamanan',
      'tr.nfc': 'kunci NFC',
      'tr.ble': 'kunci Bluetooth',
      'tr.smart-card': 'kartu pintar',
      'tr.cable': 'kunci berkabel',
      'p.offline': 'Tidak ada jawaban dari server. Periksa koneksimu lalu coba lagi.',
      'p.rate': 'Terlalu banyak percobaan. Tunggu sebentar, lalu coba lagi.',
      'p.403': 'Akunmu tidak diizinkan melakukan itu.',
      'p.401': 'Masuk lagi — sesimu sudah berakhir.',
      'p.other': 'Itu tidak berhasil. Coba lagi.'
    }
  };

  function knownLanguage(tag) {
    tag = String(tag || '').toLowerCase().replace(/_/g, '-').split('-')[0];
    return Object.prototype.hasOwnProperty.call(STRINGS, tag) ? tag : '';
  }

  var LANG = knownLanguage(document.documentElement.lang) || 'en';

  // What the deployment speaks: what a mail is written in when nobody chose.
  // The same rule the server applies to the same value.
  var DEPLOYMENT_LANGUAGE = knownLanguage(CONFIG.language) || knownLanguage(CONFIG.locale) || 'en';

  // Each in its own language, and so not in the table: somebody looking for
  // theirs in a list is looking for the word they call it by.
  var LANGUAGE_NAMES = { en: 'English', nl: 'Nederlands', id: 'Bahasa Indonesia' };

  function t(key, vars) {
    var str = STRINGS[LANG][key];
    if (str === undefined) str = STRINGS.en[key];
    if (str === undefined) return key;
    if (!vars) return str;
    return str.replace(/\{(\w+)\}/g, function (m, k) {
      return Object.prototype.hasOwnProperty.call(vars, k) ? String(vars[k]) : m;
    });
  }

  /* Static markup carries its own key, so index.html stays readable English.
   *
   * The attribute is this file's own. app.js walks the whole document for
   * [data-i18n] and answers a key it does not know with the key itself, so a
   * shared attribute would have it overwrite every label here with its id. */
  function applyStrings() {
    each(document.querySelectorAll('[data-i18n-auth]'), function (el) {
      el.textContent = t(el.getAttribute('data-i18n-auth'));
    });
    each(document.querySelectorAll('[data-i18n-auth-aria]'), function (el) {
      el.setAttribute('aria-label', t(el.getAttribute('data-i18n-auth-aria')));
    });
    each(document.querySelectorAll('[data-i18n-auth-placeholder]'), function (el) {
      el.setAttribute('placeholder', t(el.getAttribute('data-i18n-auth-placeholder')));
    });
  }

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
    if (res.status === 0) return t('p.offline');
    if (res.status === 429) return t('p.rate');
    var code = res.body && res.body.error;
    if (map && code && map[code]) return map[code];
    if (code && CODE_KEYS[code]) return t(CODE_KEYS[code]);
    // The server's own sentence is written for a person, and written in
    // English. For somebody reading in English it is the most specific thing
    // there is to say; for anybody else it is a line of a language the rest of
    // the screen is not in, and the plainer sentence below is the better one.
    if (LANG === 'en' && res.body && res.body.message) return res.body.message;
    if (res.status === 403) return t('p.403');
    if (res.status === 401) return t('p.401');
    return t('p.other');
  }

  // Every refusal code somebody can actually reach from these screens, and
  // what to say about it. A call passes its own `map` only where the same code
  // means something more specific at that moment.
  var CODE_KEYS = {
    email_taken: 'e.email_taken',
    invalid_email: 'e.invalid_email',
    account_disabled: 'e.account_disabled',
    no_password: 'e.no_password',
    self_change: 'e.self_change',
    link_failed: 'e.link_failed',
    invalid_language: 'e.invalid_language',
    weak_password: 'setpw.weak',
    invalid_token: 'setpw.expired',
    too_many_passkeys: 'pk.e.toomany',
    passkey_exists: 'pk.e.exists',
    invalid_challenge: 'pk.e.slow',
    invalid_passkey: 'pk.e.verify',
    rate_limited: 'p.rate',
    unauthenticated: 'p.401',
    forbidden: 'p.403',
    read_only: 'p.403'
  };

  /* Two refusals that are not about what was asked, and are answered by
   * changing the screen rather than by printing a sentence on it. Somebody
   * told "try again" by something that will refuse every time has been sent
   * round a loop with no way out of it.
   *
   * The first: the session ended while a screen was open. Read from the code
   * and not the status, because signing in is refused with a 401 too, and
   * there it means the password was wrong rather than that there is nothing
   * to do here but sign in again. A 401 with nothing readable behind it, a
   * proxy's own page say, is read as the session, which is the answer the
   * activity list has always given it.
   */
  function signedOut(res) {
    if (res.status !== 401) return false;
    return !res.body || res.body.error === 'unauthenticated';
  }

  /* The second: the row is not there any more. Another admin removed it, or
   * this person did in another tab, while this screen still had it drawn.
   * Every retry is refused the same way, so the row comes off the screen
   * instead of being offered once more. */
  function gone(res) {
    return res.status === 404 && !!res.body && res.body.error === 'not_found';
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

  var AUTH_ROUTES = { login: true, 'set-password': true, admin: true, account: true, activity: true };

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
   *              means anything. It is also what is drawn while the probe has
   *              had no answer, and then it is provisional: see probeSession().
   *   in       — 200: `user` is who the server says we are.
   * ------------------------------------------------------------------ */

  var state = 'unknown';
  var user = null;

  function isAdmin() { return state === 'in' && user && user.role === 'admin'; }

  function setSession(next, who, reason) {
    // A registration begun for one account is refused when another finishes
    // it, so options fetched ahead of time do not outlive whose they were.
    if ((who && who.id) !== (user && user.id)) registerCeremony.drop();

    // Whatever changed the session has answered the question a probe booked
    // by probeSession() was going to ask again. Left booked, it would spend a
    // 401 on somebody who signed in and out again inside its wait.
    if (probeTimer) { clearTimeout(probeTimer); probeTimer = null; }

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
      detail: { signedIn: next === 'in', role: user && user.role, reason: reason || '' }
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

  // And the other answer: there is an API. Whatever on screen depends on that
  // and was drawn before it was known — the reminders switch, on a page opened
  // straight at the account screen — is drawn again.
  document.addEventListener('soiree:api', function (ev) {
    if (!ev || !ev.detail || ev.detail.available !== true) return;
    if (state === 'in' && readRoute().path === 'account') renderReminders();
  });

  // And the service worker, which on a first visit is still installing when
  // the account screen asks. "Still being set up" is drawn in the meantime and
  // this is what replaces it, with the switch or with the reason there is none.
  document.addEventListener('soiree:push', function () {
    if (state === 'in' && readRoute().path === 'account') renderReminders();
  });

  // The backoff app.js gives connect(), which is asking the same origin at the
  // same moment for the same reason.
  var PROBE_RETRY_BASE_MS = 1000;
  var PROBE_RETRY_MAX_MS = 30000;
  // The wait before the probe is sent again, while there is one.
  var probeTimer = null;

  function probeSession(attempt) {
    attempt = attempt || 0;
    if (apiKnownAbsent()) {
      setSession('none');
      return Promise.resolve();
    }
    return request('GET', '/auth/session').then(function (res) {
      // Asked again, and settled some other way while this was in flight:
      // somebody signed in through the form, or the planner's own doubt was
      // answered first.
      if (attempt > 0 && state !== 'out') return;
      if (res.status === 200 && res.body) {
        setSession('in', res.body);
        return;
      }
      // 404: the routes are not mounted, so this deployment has no accounts.
      // 401: there are accounts and nobody is signed in. Both are ordinary.
      if (res.status === 404) {
        setSession('none');
        return;
      }
      // Anything else, a 500 or no answer at all, is drawn as signed out
      // rather than as a reason to break the page: the sign-in door is the
      // right thing to offer when the answer is not "you are Ada". Drawn once;
      // asking again changes nothing until somebody answers.
      if (attempt === 0) setSession('out');
      if (res.status === 401) return;

      // But it is not an answer, so it is asked again, for as long as
      // connect() goes on asking for the plan. This used to stop here, on the
      // grounds that the planner works either way. It does not: connect()
      // retries, gets its 200 and runs fully synced beside an account bar that
      // says "Sign in", with no admin links, no role for the planner to gate
      // reminders and files on, and no lock on a viewer's ledger, which is
      // only ever applied on "in". When the plan had arrived first it was
      // worse, because "out" stops a running planner and tells a person with a
      // good session to sign in again. Never after a 401, which is an answer,
      // and the one a proxy's ban rule counts.
      probeTimer = setTimeout(function () {
        probeTimer = null;
        probeSession(attempt + 1);
      }, Math.min(PROBE_RETRY_MAX_MS, PROBE_RETRY_BASE_MS * Math.pow(2, attempt)));
    });
  }

  // Ask now rather than when the backoff comes round, and start it over. Only
  // while a retry is booked, so neither of these can add a request to a load
  // that went the ordinary way.
  function probeNow() {
    if (!probeTimer) return;
    clearTimeout(probeTimer);
    probeTimer = null;
    probeSession(1);
  }

  // app.js has just heard from the origin, with either answer: "no API" is
  // read off the latch without a request. And the browser's own word that the
  // network is back, which is a hint and not a promise.
  document.addEventListener('soiree:api', probeNow);
  window.addEventListener('online', probeNow);

  /* Somebody picked another language from the switcher. app.js owns that
   * decision and has already re-said everything of its own; this has to say
   * again, in the new language, whatever it has on screen. Every render here
   * is idempotent, which is what keeps that a short list. */
  document.addEventListener('soiree:language', function (ev) {
    var tag = knownLanguage(ev && ev.detail && ev.detail.language);
    if (!tag || tag === LANG) return;
    LANG = tag;
    applyStrings();
    // No second argument: keep whatever the admin had already picked.
    fillLanguageChoice(byId('newUserLanguage'));
    renderAccountBar();
    render();
  });

  /* The planner doubts the session, or knows it is gone.
   *
   * app.js raises this in two situations, and says which.
   *
   * Definitive: a request of its own came back 401. The server has already
   * answered the question, for this browser's own cookie, so there is nothing
   * to ask: the person is signed out here and now, and shown the sign-in
   * screen with a line saying why — somebody mid-edit who is shown a login
   * form with no explanation assumes they did something wrong. This used to
   * re-probe first, which was a round trip spent on hearing the same answer
   * twice, and one more request that could be lost. When it was, the planner
   * said "sign in again" and the door never opened.
   *
   * A doubt: its event stream was refused, and an EventSource does not say
   * why. That one is asked about, and three answers are possible:
   *
   *   200 — still signed in. The stream was refused for some other reason and
   *         app.js is already waiting that out. Saying "in" again would make
   *         it refetch the plan for nothing, so nothing is said.
   *   401 — the session is gone. The same ending as above.
   *   anything else — an outage is not a sign-out. probeSession() draws a 500
   *         as "out", because at load the door is the right thing to offer
   *         meanwhile, and then asks again; here there is a signed-in person
   *         to wrongly throw out, so nothing is drawn at all.
   *
   * A doubt that arrives while one is already being asked about is not
   * dropped: it is asked again afterwards, because the answer in flight may
   * predate whatever prompted the second one.
   */
  var checking = false;
  var checkAgain = false;
  var endedNotice = false;

  function sessionEnded() {
    if (state !== 'in') return;
    endedNotice = true;
    setSession('out');
    goto('login');
  }

  function askAboutSession() {
    if (checking) { checkAgain = true; return; }
    checking = true;
    request('GET', '/auth/session').then(function (res) {
      checking = false;
      if (res.status === 200 && res.body) {
        if (state !== 'in') setSession('in', res.body);
      } else if (res.status === 401) {
        sessionEnded();
      }
      if (checkAgain) { checkAgain = false; askAboutSession(); }
    });
  }

  document.addEventListener('soiree:session-check', function (ev) {
    if (state === 'unknown' || state === 'none') return;
    if (ev && ev.detail && ev.detail.definitive) { sessionEnded(); return; }
    askAboutSession();
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
      acts.appendChild(barButton(t('signin'), 'link-btn', 'login'));
      return;
    }
    if (state !== 'in' || !user) return;

    who.appendChild(make('span', 'account-email', user.email));
    who.appendChild(make('span', 'account-role', roleWord(user.role)));

    if (isAdmin()) acts.appendChild(barButton(t('bar.people'), 'link-btn', 'admin'));
    if (isAdmin()) acts.appendChild(barButton(t('bar.activity'), 'link-btn', 'activity'));
    acts.appendChild(barButton(t('bar.account'), 'link-btn', 'account'));
    var out = make('button', 'link-btn', t('signout'));
    out.type = 'button';
    out.addEventListener('click', signOut);
    acts.appendChild(out);
  }

  function roleWord(role) {
    if (role === 'admin' || role === 'editor' || role === 'viewer') return t('role.' + role);
    return '';
  }

  /* Signing out takes this browser's copy of the plan with it (forgetPlan in
   * app.js), because that copy is a ledger of names against money and this may
   * not be the reader's own computer. So it is the one action here that can
   * destroy something, and it goes in a fixed order:
   *
   *   1. The planner is asked what has not reached the server yet. It flushes
   *      and waits a few seconds first, so the usual answer is nothing.
   *   2. Anything still unsent is put to the person as a question.
   *   3. The server is told, and only a 204 counts. An unreachable server
   *      leaves the cookie valid, and wiping the page while saying "signed
   *      out" over a session that still works would be a lie on exactly the
   *      computer where it matters.
   *   4. Then the session is announced as ended *by request*, which is what
   *      tells the planner to forget rather than to hold on.
   */
  var signingOut = false;

  function signOut() {
    if (signingOut) return;
    signingOut = true;
    var asked = (window.soiree && typeof window.soiree.beforeSignOut === 'function')
      ? window.soiree.beforeSignOut() : Promise.resolve(0);
    asked.then(function (unsent) {
      if (unsent > 0 && !window.confirm(t('signout.unsent'))) {
        signingOut = false;
        return;
      }
      request('POST', '/auth/logout').then(function (res) {
        signingOut = false;
        if (res.status !== 204) {
          window.alert(t('signout.failed'));
          return;
        }
        setSession('out', null, 'signout');
        // The sign-in screen, not the planner: what is behind it now is an
        // empty ledger, and "nothing in the ledger yet" is not true of this
        // event — it is only true of this browser.
        goto('login');
      });
    });
  }

  /* ------------------------------------------------------------------
   * The router
   * ------------------------------------------------------------------ */

  var PANEL_IDS = ['panelLogin', 'panelSetPassword', 'panelAdmin', 'panelActivity', 'panelAccount', 'panelNote'];

  function showPanel(id, title, lede) {
    each(PANEL_IDS, function (p) { show(byId(p), p === id); });
    setText(byId('authTitle'), title);
    // The lede is a live region, so it is written only when it has something
    // else to say. render() is idempotent and runs again on every session
    // probe and language switch; rewriting the same sentence would make a
    // screen reader read it out each time.
    var line = byId('authLede');
    if (line && line.textContent !== (lede || '')) setText(line, lede || '');
    drawVersion();
    enterScreen(id, title);
  }

  // What the tab is called with the planner on screen: the event's own name,
  // as the server rendered it into the document. Read once, because from here
  // on the title is this file's to set.
  var EVENT_TITLE = document.title;

  /* Arriving on a screen, for somebody who cannot see that it changed.
   *
   * Every button that swaps screens sits in the half the swap hides - the
   * account bar is in the planner, "Back to the planner" is on the accounts
   * screen - so the browser is left holding a focused element that is no
   * longer there and drops focus on <body>: nothing is announced, and the
   * next Tab starts at the top of the page. Focus goes to the heading of
   * whatever is on screen now instead, which is what tells a screen reader,
   * and a Tab key, that this is a different page.
   *
   * A screen that wants a field focused still gets it: renderLogin and
   * renderSetPassword reach this through showPanel and focus afterwards.
   *
   * Only a real change counts, for the same reason the lede above is guarded:
   * focus that moves under somebody mid-sentence is worse than focus that
   * never moved.
   */
  var onScreen;   // undefined until the first render; null is the planner

  function enterScreen(id, title) {
    document.title = id && title ? title + ' · ' + EVENT_TITLE : EVENT_TITLE;
    var moved = onScreen !== undefined && onScreen !== id;
    onScreen = id;
    if (!moved) return;
    var head = id ? byId('authTitle') : document.querySelector('.masthead h1');
    if (!head) return;
    // A heading is not focusable on its own, and this one stays out of the
    // tab order: it is somewhere to be sent, not a stop on the way through.
    head.tabIndex = -1;
    head.focus();
  }

  /* Which release this is, very small, under every screen somebody signed in
   * can open. It is what a person needs to quote when they report a problem,
   * and the people who can report one are the ones who can sign in - so the
   * server only tells them, and this asks once per page, not once per screen.
   * No translation: it is a name and a number.
   */
  var appVersion = '';
  var versionAsked = false;

  function drawVersion() {
    var el = byId('appVersion');
    if (!el) return;
    // Emptied as well as hidden, so the page behind a sign-in screen does not
    // go on saying what the last person to use this browser was told.
    if (!user) { setText(el, ''); show(el, false); return; }
    if (appVersion) { setText(el, appVersion); show(el, true); return; }
    if (versionAsked) return;
    // Once, whatever the answer. An older server has no such route, and asking
    // it again on every screen would not change that.
    versionAsked = true;
    request('GET', '/version').then(function (res) {
      var v = res.status === 200 && res.body && res.body.version ? String(res.body.version) : '';
      if (!v) return;
      appVersion = 'soiree ' + (/^[0-9]/.test(v) ? 'v' + v : v);
      drawVersion();
    });
  }

  function showNote(title, lede, line) {
    showPanel('panelNote', title, lede);
    setText(byId('authNote'), line || '');
  }

  // Where to land after signing in: back where you were headed.
  var afterLogin = '';

  // A password chosen a moment ago, on this page load. The sign-in form is
  // where redeeming a link leads, and it says so rather than opening as if
  // nothing had happened.
  var passwordSet = false;

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
      enterScreen(null, '');
      return;
    }

    // Neither of these has to wait for the probe. A set-password link is
    // redeemed with a token, not a session, and somebody who asked for the
    // sign-in screen should get the form rather than a held breath.
    if (path === 'set-password') { renderSetPassword(); return; }
    if (path === 'login') {
      afterLogin = '';
      renderLogin(t(passwordSet ? 'setpw.done.body'
        : endedNotice ? 'login.ended' : 'login.lede'));
      return;
    }

    if (state === 'unknown') {
      showNote(t('wait.title'), '', t('wait.body'));
      return;
    }
    if (state === 'out') {
      // Asked for somewhere that needs an account. Sign in first, then land
      // where they were going rather than back at the planner.
      afterLogin = path;
      renderLogin(t(path === 'admin' ? 'login.lede.admin'
        : path === 'activity' ? 'login.lede.activity' : 'login.lede.account'));
      return;
    }
    if (path === 'admin') { renderAdmin(); return; }
    if (path === 'activity') { renderActivity(); return; }
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
    showPanel('panelLogin', t('signin'), lede);
    show(byId('loginPasskey'), PASSKEYS_OFFERED);
    say(byId('loginMsg'), '');
    loginCeremony.warm();
    var email = byId('loginEmail');
    if (email && !email.value) email.focus();
  }

  function loginSucceeded(who) {
    var next = afterLogin;
    afterLogin = '';
    endedNotice = false;
    passwordSet = false;
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
        say(msg, t('login.need'), true);
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
        say(msg, problem(res, { invalid_credentials: t('login.nomatch') }), true);
      });
    });

    var forgot = byId('loginForgot');
    if (forgot) {
      forgot.addEventListener('click', function () {
        var field = byId('loginEmail');
        var email = (field.value || '').trim();
        if (!email) {
          say(msg, t('forgot.need'), true);
          field.focus();
          return;
        }
        forgot.disabled = true;
        // With the language this screen is being read in. Nobody else is
        // involved in a reset, and the server has never seen this browser.
        request('POST', '/auth/password-reset', { email: email, language: LANG }).then(function (res) {
          forgot.disabled = false;
          if (res.status === 202) {
            // Deliberately the same sentence whether or not that address has
            // an account. The server is careful not to say; neither is this.
            say(msg, t('forgot.sent'));
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
    showPanel('panelSetPassword', t('setpw.title'), t('setpw.lede'));
    var msg = byId('setPasswordMsg');
    if (!pendingToken) {
      say(msg, t('setpw.incomplete.open'), true);
      byId('setPasswordSubmit').disabled = true;
      return;
    }
    byId('setPasswordSubmit').disabled = false;
    say(msg, '');
    // Off again whenever this screen is drawn: a password still showing is a
    // password on screen for whoever looks next.
    revealPassword(false);
    var first = byId('newPassword');
    if (first) first.focus();
  }

  /* Showing what was typed.
   *
   * There are two fields here because there is no other way to catch a typo
   * in something that is drawn as dots, and a phone keyboard is where that
   * typo happens. This is the other way, and it is off until somebody asks
   * for it: the button says which of the two states pressing it leads to,
   * and aria-pressed says which one it is in. */
  function revealPassword(on) {
    var btn = byId('showPassword');
    if (!btn) return;
    btn.setAttribute('aria-pressed', on ? 'true' : 'false');
    setText(btn, t(on ? 'setpw.hide' : 'setpw.show'));
    byId('newPassword').type = on ? 'text' : 'password';
    byId('newPassword2').type = on ? 'text' : 'password';
  }

  function bindSetPassword() {
    var form = byId('setPasswordForm');
    if (!form) return;
    var msg = byId('setPasswordMsg');
    var submit = byId('setPasswordSubmit');

    var reveal = byId('showPassword');
    if (reveal) {
      reveal.addEventListener('click', function () {
        revealPassword(reveal.getAttribute('aria-pressed') !== 'true');
      });
    }

    form.addEventListener('submit', function (ev) {
      ev.preventDefault();
      var a = byId('newPassword').value || '';
      var b = byId('newPassword2').value || '';
      if (a !== b) {
        say(msg, t('setpw.mismatch'), true);
        return;
      }
      if (a.length < 12) {
        // Checked here as well as at the server so a short password costs a
        // round trip rather than one of the attempts the link is allowed.
        say(msg, t('setpw.short'), true);
        return;
      }
      if (!pendingToken) {
        say(msg, t('setpw.incomplete'), true);
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
            revealPassword(false);
            passwordSet = true;
            // Redeeming revoked every session this account had, including
            // this browser's if it had one.
            setSession('out');
            // Straight to the form they need next, which used to be a screen
            // whose one button led to it. The sentence comes along as its
            // lede, so what just happened is still on screen while they sign
            // in with what they just chose.
            goto('login');
            return;
          }
          say(msg, problem(res, {}), true);
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

  /* Activity: what everybody has been doing, newest first.
   *
   * The server has recorded every change since before there was a screen to
   * show it on; this is that screen. Admins only, and the server enforces it:
   * every entry names an account, and the list of accounts is theirs to read.
   *
   * One sentence per change, in the reader's language, then what moved from
   * what to what. A new row and a removed row get the sentence alone - the
   * twelve fields a row is born with are not news. Amounts arrive as the
   * strings the planner itself shows, so nothing here does arithmetic.
   */
  var activityNext = null;
  var activityLoading = false;

  function renderActivity() {
    if (!isAdmin()) {
      showNote(t('act.title'), '', t('act.notadmin'));
      return;
    }
    showPanel('panelActivity', t('act.title'), t('act.lede'));
    var list = byId('activityList');
    while (list.firstChild) list.removeChild(list.firstChild);
    activityNext = null;
    show(byId('activityEmpty'), false);
    show(byId('activityMore'), false);
    say(byId('activityMsg'), '');
    loadActivity();
  }

  function loadActivity() {
    if (activityLoading) return;
    activityLoading = true;
    var path = '/activity?limit=50' + (activityNext ? '&before=' + encodeURIComponent(activityNext) : '');
    request('GET', path).then(function (res) {
      activityLoading = false;
      if (signedOut(res)) { sessionEnded(); return; }
      var list = byId('activityList');
      var more = byId('activityMore');
      if (res.status !== 200 || !res.body || !Array.isArray(res.body.entries)) {
        say(byId('activityMsg'), t('act.failed'), true);
        // Something to press. That button asks for the next page, and with
        // nothing listed there is no next page to ask for, so it asks for
        // this one again and says so: "Show older" under an empty list offers
        // something that is not there.
        if (!list.firstChild) setText(more, t('act.retry'));
        show(more, true);
        return;
      }
      // A load that worked leaves nothing of the one that did not.
      say(byId('activityMsg'), '');
      setText(more, t('act.more'));
      each(res.body.entries, function (entry) { list.appendChild(activityRow(entry)); });
      activityNext = res.body.nextBefore || null;
      show(more, !!activityNext);
      show(byId('activityEmpty'), !list.firstChild);
    });
  }

  function activityRow(entry) {
    var li = make('li', 'activity');

    var head = make('p', 'activity-head');
    head.appendChild(make('span', 'activity-who', activityWho(entry.actor || {})));
    var when = make('time', 'activity-when', activityWhen(entry.at));
    when.setAttribute('datetime', entry.at || '');
    head.appendChild(when);
    li.appendChild(head);

    var noun = STRINGS.en['act.e.' + entry.entity] ? t('act.e.' + entry.entity) : String(entry.entity || '');
    var label = entry.label ? '“' + entry.label + '”' : '';
    var sentence = t('act.s.' + entry.action, { e: noun, l: label }).replace(/\s+/g, ' ').trim();
    li.appendChild(make('p', 'activity-what', sentence.charAt(0).toUpperCase() + sentence.slice(1)));

    if (entry.action === 'update' && entry.changes && entry.changes.length) {
      var changes = make('ul', 'activity-changes');
      each(entry.changes, function (c) {
        var row = make('li', '');
        row.appendChild(make('span', 'activity-field', activityField(c.field)));
        row.appendChild(make('span', 'activity-old', activityValue(c.field, c.old)));
        row.appendChild(make('span', 'activity-arrow', '→'));
        row.appendChild(make('span', 'activity-new', activityValue(c.field, c['new'])));
        changes.appendChild(row);
      });
      li.appendChild(changes);
    }
    return li;
  }

  function activityWho(actor) {
    if (actor.email) return actor.email;
    // An account did this, and the account is gone. The entry outlives it on
    // purpose; the address does not, also on purpose.
    if (actor.kind === 'user') return t('act.k.gone');
    if (actor.kind === 'system' || actor.kind === 'import') return t('act.k.' + actor.kind);
    return t('act.k.unknown');
  }

  function activityWhen(at) {
    var d = new Date(at);
    if (isNaN(d.getTime())) return '';
    try { return d.toLocaleString(LANG, { dateStyle: 'medium', timeStyle: 'short' }); } catch (e) { return d.toISOString(); }
  }

  function activityField(field) {
    return STRINGS.en['act.f.' + field] ? t('act.f.' + field) : String(field);
  }

  function activityValue(field, value) {
    if (value === null || value === undefined || value === '') return t('act.none');
    if (field === 'passwordHash') return t('act.hidden');
    if (value === true) return t('act.yes');
    if (value === false) return t('act.no');
    if (Array.isArray(value)) return value.length ? t('act.items', { n: value.length }) : t('act.none');
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value);
  }

  function renderAdmin() {
    if (!isAdmin()) {
      showNote(t('people.title'), '', t('people.notadmin'));
      return;
    }
    showPanel('panelAdmin', t('people.title'), t('people.lede'));
    // Whatever link was last handed over goes out of the page with the panel
    // it was shown in. "Shown once, and kept nowhere" has to survive somebody
    // navigating away and back, or it is not true.
    show(byId('adminLinkOut'), false);
    byId('adminLinkValue').value = '';
    setText(byId('adminLinkCopy'), t('link.copy'));
    loadPeople();
  }

  /* Focus across a redraw.
   *
   * Every action here ends in a rebuild of the whole list, which takes the
   * control that was pressed with it - after disabling it for the round trip,
   * which has already dropped focus on <body>. So what is remembered is not
   * the node but which row and which of its controls, and the rebuilt list is
   * asked for that pair again. Without it an admin changing three roles tabs
   * in from the top of the page twice.
   */
  var heldFocus = null;

  function holdFocus(p, control) {
    heldFocus = control ? { id: p.id, act: control.getAttribute('data-act') || '' } : null;
  }

  function rowIndex(list, id) {
    var rows = list.querySelectorAll('li[data-id]');
    for (var i = 0; i < rows.length; i += 1) {
      if (rows[i].getAttribute('data-id') === id) return i;
    }
    return -1;
  }

  function restoreFocus(list, held, was) {
    var again = list.querySelector('li[data-id="' + held.id.replace(/["\\]/g, '\\$&')
      + '"] [data-act="' + held.act + '"]');
    if (again) { again.focus(); return; }
    // The row is gone, so the control that was pressed is gone with it. Focus
    // goes to the row that took its place, and to its first control rather
    // than its "Remove": somebody who has just removed one person should not
    // be one keystroke from removing the next.
    var rows = list.querySelectorAll('li[data-id]');
    var row = rows[was >= 0 && was < rows.length ? was : rows.length - 1];
    var ctl = row ? row.querySelector('select, button:not(.danger)') : null;
    if (ctl) { ctl.focus(); return; }
    // Nobody left to point at: the heading over the list is where this screen
    // begins.
    var head = byId('authTitle');
    if (head) { head.tabIndex = -1; head.focus(); }
  }

  function loadPeople() {
    // Built from scratch, so there is nothing to put focus back on: a hold
    // taken before a request that ended in a sign-out must not be spent on
    // whatever list is drawn next.
    heldFocus = null;
    request('GET', '/users').then(function (res) {
      if (signedOut(res)) { sessionEnded(); return; }
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
    var held = heldFocus;
    heldFocus = null;
    var was = held ? rowIndex(list, held.id) : -1;
    while (list.firstChild) list.removeChild(list.firstChild);
    show(byId('peopleEmpty'), people.length === 0);
    people.forEach(function (p) { list.appendChild(personRow(p)); });
    if (held) restoreFocus(list, held, was);
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

  /* The languages a mail can be written in.
   *
   * The first choice is "nothing chosen", and it names the language that
   * means: an admin deciding whether to pick one needs to know what happens
   * if they do not.
   *
   * The choice is about one mail and is kept nowhere — not on the account, and
   * not here. What somebody reads is better learned from their own browser
   * each time they arrive than fixed by whoever invited them, and their
   * browser is theirs to change. */
  function fillLanguageChoice(select, current) {
    if (!select) return;
    var keep = current === undefined ? select.value : (current || '');
    while (select.firstChild) select.removeChild(select.firstChild);
    var none = document.createElement('option');
    none.value = '';
    none.textContent = t('lang.default', { name: LANGUAGE_NAMES[DEPLOYMENT_LANGUAGE] });
    select.appendChild(none);
    Object.keys(LANGUAGE_NAMES).forEach(function (tag) {
      var o = document.createElement('option');
      o.value = tag;
      o.textContent = LANGUAGE_NAMES[tag];
      select.appendChild(o);
    });
    select.value = keep;
  }

  function statusWord(status) {
    if (status === 'invited' || status === 'active' || status === 'disabled') return t('status.' + status);
    return status;
  }

  function personRow(p) {
    var self = !!(user && user.id === p.id);
    var li = make('li', 'person');
    // Which row this is. The node a person pressed does not survive the
    // redraw that follows; this pair, with the data-act below, does.
    li.setAttribute('data-id', p.id);

    var head = make('div', 'person-head');
    head.appendChild(make('span', 'person-email', p.email));
    if (self) head.appendChild(make('span', 'person-you', t('person.you')));
    li.appendChild(head);

    var meta = make('div', 'person-meta');
    var status = make('span', 'person-status status-' + p.status, statusWord(p.status));
    meta.appendChild(status);
    meta.appendChild(make('span', 'person-added', t('person.added', { date: day(p.createdAt) })));
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
      sel.setAttribute('aria-label', t('person.role.aria', { email: p.email }));
      sel.setAttribute('data-act', 'role');
      ['viewer', 'editor', 'admin'].forEach(function (role) {
        var o = document.createElement('option');
        o.value = role;
        o.textContent = t('opt.' + role);
        if (p.role === role) o.selected = true;
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
      // The language of that link's mail, chosen when it is sent. It changes
      // nothing by itself and saves nothing.
      var lang = make('select', 'person-language');
      lang.setAttribute('aria-label', t('person.language.aria', { email: p.email }));
      lang.setAttribute('data-act', 'language');
      fillLanguageChoice(lang, null);
      acts.appendChild(lang);
      acts.appendChild(actButton(
        'invite',
        t(p.status === 'invited' ? 'person.resend' : 'person.sendlink'),
        'link-btn',
        function (btn) { invite(p, btn, lang.value); }
      ));
    }

    if (!self) {
      acts.appendChild(actButton(
        'access',
        t(p.status === 'disabled' ? 'person.access.on' : 'person.access.off'),
        'link-btn',
        function (btn) {
          patchPerson(p, { status: p.status === 'disabled' ? 'active' : 'disabled' }, btn);
        }
      ));
      acts.appendChild(actButton('remove', t('remove'), 'link-btn danger', function (btn) {
        if (!window.confirm(t('person.confirm', { email: p.email }))) return;
        removePerson(p, btn);
      }));
    }

    li.appendChild(acts);
    return li;
  }

  function actButton(act, label, cls, fn) {
    var b = make('button', cls, label);
    b.type = 'button';
    b.setAttribute('data-act', act);
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
    holdFocus(p, control);
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
        adminSays(t('person.conflict'), true);
        return;
      }
      if (signedOut(res)) { sessionEnded(); return; }
      if (gone(res)) {
        dropPerson(p.id);
        adminSays(t('person.gone'), true);
        return;
      }
      adminSays(problem(res, {}), true);
      drawPeople(); // put the select back to what the server still believes
    });
  }

  function removePerson(p, control) {
    holdFocus(p, control);
    if (control) control.disabled = true;
    adminSays('');
    request('DELETE', '/users/' + p.id + '?revision=' + encodeURIComponent(p.revision))
      .then(function (res) {
        if (control) control.disabled = false;
        if (res.status === 204) {
          dropPerson(p.id);
          adminSays(t('person.removed', { email: p.email }));
          return;
        }
        if (res.status === 409 && res.body && res.body.current) {
          adoptPerson(res.body.current);
          adminSays(t('person.conflict.retry'), true);
          return;
        }
        if (signedOut(res)) { sessionEnded(); return; }
        if (gone(res)) {
          dropPerson(p.id);
          adminSays(t('person.gone'), true);
          return;
        }
        adminSays(problem(res, {}), true);
      });
  }

  function invite(p, control, language) {
    holdFocus(p, control);
    if (control) control.disabled = true;
    adminSays('');
    request('POST', '/users/' + p.id + '/invite', language ? { language: language } : {}).then(function (res) {
      if (control) control.disabled = false;
      if (res.status === 200 && res.body) {
        if (res.body.user) adoptPerson(res.body.user);
        afterInvite(res.body, p.email);
        return;
      }
      if (signedOut(res)) { sessionEnded(); return; }
      if (gone(res)) {
        dropPerson(p.id);
        adminSays(t('person.gone'), true);
        return;
      }
      adminSays(problem(res, {}), true);
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
      adminSays(t('invite.sent', { email: email }));
      return;
    }
    adminSays('');
    setText(byId('adminLinkLede'), t('invite.manual', { email: email }));
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
      var language = byId('newUserLanguage') ? byId('newUserLanguage').value : '';
      if (!email) {
        adminSays(t('people.needemail'), true);
        emailField.focus();
        return;
      }
      var submit = byId('createUserSubmit');
      submit.disabled = true;
      adminSays('');
      var made = { email: email, role: role };
      // Left out entirely when nothing was chosen, which is what "the
      // deployment's language" is on the wire.
      if (language) made.language = language;
      request('POST', '/users', made).then(function (res) {
        submit.disabled = false;
        if (res.status === 201 && res.body && res.body.user) {
          emailField.value = '';
          adoptPerson(res.body.user);
          afterInvite(res.body, res.body.user.email);
          return;
        }
        if (signedOut(res)) { sessionEnded(); return; }
        adminSays(problem(res, {}), true);
        if (res.status === 409) loadPeople();
      });
    });

    var copy = byId('adminLinkCopy');
    if (copy) {
      copy.addEventListener('click', function () {
        var field = byId('adminLinkValue');
        field.focus();
        field.select();
        var done = function (ok) { setText(copy, t(ok ? 'link.copied' : 'link.copyfail')); };
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
        setText(byId('adminLinkCopy'), t('link.copy'));
        show(byId('adminLinkOut'), false);
      });
    }
  }

  /* ------------------------------------------------------------------
   * Your account, and its passkeys
   * ------------------------------------------------------------------ */

  function renderAccount() {
    showPanel('panelAccount', t('account.title'),
      user ? t('account.lede', { email: user.email, role: roleWord(user.role) }) : '');
    show(byId('passkeySection'), PASSKEYS_OFFERED);
    say(byId('passkeyMsg'), '');
    if (PASSKEYS_OFFERED) loadPasskeys();
    registerCeremony.warm();
    renderReminders();
  }

  /* Reminders on this device.
   *
   * The planner offers them once, the first time an admin gives a task a due
   * date — a good moment to ask and a bad thing to depend on: somebody who
   * never sets a date is never asked, "Not now" had no way back, and there was
   * no way to turn them off short of the browser's own settings. This is the
   * standing version of the same switch.
   *
   * app.js owns what it does (window.soiree.push); this only draws it. Shown
   * to admins and nobody else, because the server pushes the digest to active
   * admins and nobody else, and a switch that does nothing is worse than none.
   */
  function renderReminders() {
    var section = byId('remindersSection');
    var push = window.soiree && window.soiree.push;
    if (!section || !push || !isAdmin()) { show(section, false); return; }
    push.state().then(drawReminders);
  }

  /* One sentence per situation, not one for all of them.
   *
   * There was one — "this browser cannot receive notifications… on an iPhone,
   * add this page to the Home Screen" — and it was shown for everything that
   * was not on, off or blocked. So a Windows PC whose service worker had failed
   * to start was sent to look for a Home Screen. The state says what is wrong,
   * `why` says where (app.js knows the browser; this file knows the words), and
   * a pair nobody wrote a sentence for falls back to the state's own.
   */
  var REMINDER_LINES = {
    'on': 'rem.on',
    'off': 'rem.off',
    'starting': 'rem.starting',
    'noworker': 'rem.noworker.failed',
    'noworker/storage': 'rem.noworker.storage',
    'blocked': 'rem.blocked',
    'blocked/ios-app': 'rem.blocked.ios',
    'blocked/safari': 'rem.blocked.safari',
    'refused': 'rem.refused',
    'refused/brave': 'rem.refused.brave',
    'unsupported': 'rem.unsupported',
    'unsupported/insecure': 'rem.insecure',
    'unsupported/ios-safari': 'rem.ios.safari',
    'unsupported/ios-other': 'rem.ios.other',
    'unsupported/ios-app': 'rem.ios.old'
  };

  function reminderLine(now) {
    var push = window.soiree && window.soiree.push;
    var why = push && typeof push.why === 'function' ? push.why(now) : '';
    return REMINDER_LINES[now + '/' + why] || REMINDER_LINES[now] || 'rem.off';
  }

  function drawReminders(now) {
    var section = byId('remindersSection');
    var toggle = byId('remindersToggle');
    if (!section || !toggle) return;
    // Not this deployment's feature. Nothing to explain and nothing to draw.
    if (now === 'unavailable') { show(section, false); return; }
    show(section, true);

    say(byId('remindersState'), t(reminderLine(now)), now !== 'on' && now !== 'off' && now !== 'starting');
    // Only those two are something a button here can change. "refused" keeps
    // it as well: the setting it names is fixed elsewhere, and this is the
    // button to come back to.
    show(toggle, now === 'on' || now === 'off' || now === 'refused');
    setText(toggle, t(now === 'on' ? 'rem.turnoff' : 'rem.turnon'));
    toggle.setAttribute('data-now', now);
    toggle.disabled = false;
  }

  function bindReminders() {
    var toggle = byId('remindersToggle');
    if (!toggle) return;
    toggle.addEventListener('click', function () {
      var push = window.soiree && window.soiree.push;
      if (!push) return;
      var was = toggle.getAttribute('data-now');
      toggle.disabled = true;
      // Straight from the click, with nothing awaited first: the browser only
      // shows its permission prompt for a gesture.
      (was === 'on' ? push.disable() : push.enable()).then(function (now) {
        drawReminders(now);
        // "refused" twice running is still its own sentence, which says what
        // to change; "try again" is for the failures nothing else explains.
        if (now === was && now !== 'refused') say(byId('remindersState'), t('rem.failed'), true);
      });
    });
  }

  var passkeys = [];

  function loadPasskeys() {
    request('GET', '/auth/passkeys').then(function (res) {
      if (signedOut(res)) { sessionEnded(); return; }
      if (res.status !== 200 || !res.body) {
        say(byId('passkeyMsg'), problem(res, {}), true);
        return;
      }
      passkeys = res.body.passkeys || [];
      drawPasskeys();
    });
  }

  // "internal", "hybrid", "usb" is the protocol's vocabulary, not a person's.
  var TRANSPORTS = ['internal', 'hybrid', 'usb', 'nfc', 'ble', 'smart-card', 'cable'];

  function transportWords(list) {
    var out = [];
    (list || []).forEach(function (name) {
      if (TRANSPORTS.indexOf(name) < 0) return;
      var word = t('tr.' + name);
      if (out.indexOf(word) < 0) out.push(word);
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
      li.appendChild(make('span', 'passkey-label', k.label || t('pk.unnamed')));

      var where = transportWords(k.transports);
      var line = t('pk.added', { date: day(k.createdAt) });
      if (where) line = where + ', ' + line;
      li.appendChild(make('span', 'passkey-meta', line));
      li.appendChild(make('span', 'passkey-used',
        k.lastUsedAt ? t('pk.lastused', { date: day(k.lastUsedAt) }) : t('pk.unused')));

      li.appendChild(actButton('remove', t('remove'), 'link-btn danger', function (btn) {
        if (!window.confirm(t('pk.confirm', { label: k.label || t('pk.this') }))) return;
        btn.disabled = true;
        request('DELETE', '/auth/passkeys/' + k.id).then(function (res) {
          btn.disabled = false;
          // Removed, and "it was already gone" because another device got
          // there first, end the same way: the key is not on this account any
          // more, so it comes off the list either way.
          if (res.status === 204 || gone(res)) {
            passkeys = passkeys.filter(function (x) { return x.id !== k.id; });
            drawPasskeys();
            // A row here has one control and it is the one that just removed
            // it, so there is nothing of the row to go back to. "Add a
            // passkey" is the next thing anybody does on this screen, and it
            // is not a way to remove another one by mistake.
            var add = byId('passkeyAdd');
            if (add) add.focus();
            // The options in hand still tell this device not to make a second
            // key, for a key the server has just forgotten.
            registerCeremony.renew();
            var was = res.status === 204;
            say(byId('passkeyMsg'), t(was ? 'pk.removed' : 'pk.gone'), !was);
            return;
          }
          if (signedOut(res)) { sessionEnded(); return; }
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

  /* The browser's own translation first, the written-out one if it is missing
   * or if it throws. It throws more often than it should: a password manager's
   * extension can stand in for navigator.credentials and hand back something
   * shaped like a credential whose toJSON refuses to run on it — Safari says
   * "Can only call PublicKeyCredential.toJSON on instances of
   * PublicKeyCredential" — and the buffers the manual path reads are still
   * there when that happens. */
  function native(fn, self, arg) {
    if (typeof fn !== 'function') return null;
    try { return fn.call(self, arg) || null; } catch (e) { return null; }
  }

  function creationOptions(json) {
    var PKC = window.PublicKeyCredential;
    var done = native(PKC.parseCreationOptionsFromJSON, PKC, json);
    if (done) return done;
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
    var PKC = window.PublicKeyCredential;
    var done = native(PKC.parseRequestOptionsFromJSON, PKC, json);
    if (done) return done;
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
    var done = native(cred.toJSON, cred);
    if (done) return done;
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
    var done = native(cred.toJSON, cred);
    if (done) return done;
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

  /* ---------- What the browser said, in words ----------
   *
   * A ceremony that fails on somebody's phone fails where nobody can see a
   * console, so the page has to say what kind of failure it was. Each kind the
   * specification names gets a sentence, and the kind itself goes on the end in
   * brackets: it is the one word a person can read out to whoever is helping
   * them, and it is a name from a fixed list, never the browser's own message
   * or a stack.
   *
   * NotAllowedError is the awkward one. It is what a person closing the prompt
   * produces, and also what a browser refusing to open one produces, and the
   * specification makes them the same on purpose. The clock tells them apart
   * well enough: nobody reads and dismisses a system prompt in under a second,
   * so a refusal that fast was the browser's. It used to be swallowed entirely,
   * as "the person waved it away" — which made the second case a button that
   * did nothing at all.
   */
  var PROMPT_REFUSED_WITHIN_MS = 1000;

  var CEREMONY_KINDS = {
    SecurityError: 'pk.e.origin',
    NotSupportedError: 'pk.e.unsupported',
    ConstraintError: 'pk.e.constraint',
    AbortError: 'pk.e.abort'
  };

  // Returns { text, bad }: a dismissed prompt is said, and not as a refusal.
  function ceremonyMessage(err, ceremony, startedAt) {
    // Letters only, and not many: a name, whatever handed it over.
    var kind = String((err && err.name) || 'Error').replace(/[^A-Za-z]/g, '').slice(0, 40) || 'Error';
    var key = Object.prototype.hasOwnProperty.call(CEREMONY_KINDS, kind) ? CEREMONY_KINDS[kind] : '';
    // Only at registration, where it is how an authenticator says it was on
    // the exclusion list.
    if (kind === 'InvalidStateError' && ceremony === 'register') key = 'pk.e.device';
    var bad = true;
    if (kind === 'NotAllowedError') {
      bad = Date.now() - startedAt < PROMPT_REFUSED_WITHIN_MS;
      key = bad ? 'pk.e.blocked' : 'pk.e.dismissed';
    }
    if (!key) key = ceremony === 'register' ? 'pk.e.setup' : 'pk.e.signin';
    return { text: t('pk.e.kind', { message: t(key), kind: kind }), bad: bad };
  }

  /* ---------- What the browser said ----------
   *
   * A refusal from the browser never reaches the server: no credential, so no
   * request. The person sees a sentence and the error's name, and the name is
   * NotAllowedError for a prompt that was closed, a prompt that was never
   * shown, a page without focus and a request already pending alike. What
   * tells them apart is the browser's own message, which is not for showing -
   * it is in the browser's language, not the reader's, and says nothing they
   * can act on - but is exactly what whoever runs the site needs, and cannot
   * get: the device is somebody else's, often in another country. So it goes
   * to the console, and to the server's log, which takes no address and no
   * account with it. `focused` and `prepared` are read before the call, since
   * afterwards is too late: a prompt takes the focus with it.
   */
  function reportRefusal(err, ceremony, startedAt, prepared, focused) {
    var kind = String((err && err.name) || 'Error');
    var message = String((err && err.message) || '');
    try { console.warn('passkey ' + ceremony + ' refused - ' + kind + ': ' + message); } catch (e) { /* no console */ }
    request('POST', '/auth/passkeys/report', {
      ceremony: ceremony,
      kind: kind.slice(0, 40),
      message: message.slice(0, 240),
      elapsedMs: Date.now() - startedAt,
      prepared: !!prepared,
      focused: !!focused
    }).then(null, function () { /* a report that does not arrive is not a second problem */ });
  }

  /* ---------- A ceremony that is ready before it is asked for ----------
   *
   * Safari wants navigator.credentials asked from inside the tap. Asking the
   * server for a challenge first puts a round trip between the two, and WebKit
   * has forgiven that in different ways at different times: carrying the tap
   * across a fetch for ten seconds, allowing a page one call without it,
   * rationing calls made without it. A slow link, a second read of the body, or
   * an earlier attempt on the same page can each land on the wrong side of
   * whichever rule applies, and the refusal is a NotAllowedError that looks
   * like a person closing the prompt. Chromium does not mind, which is how
   * this went unnoticed. None of it needs predicting if the round trip comes
   * first — when the screen is drawn — so that the tap finds the options
   * waiting and calls the browser in the same turn of the event loop.
   *
   * A challenge lives five minutes on the server (passkeyChallengeTTL), so one
   * older than four is discarded and fetched again for as long as the screen is
   * up. One that has been handed to the browser is never handed over twice.
   * And a tap that beats the fetch, or follows a refusal, takes the old road:
   * fetch, then call. That is no worse than it was, and it is where a refusal
   * from the server gets said, because until somebody asks there is nobody to
   * say it to.
   */
  var PASSKEY_FRESH_MS = 4 * 60 * 1000;

  function preparedCeremony(path, parse, showing) {
    var ready = null;      // { options, at }
    var pending = null;    // the begin request in flight
    var generation = 0;    // bumped when what is in flight stopped being wanted
    var timer = 0;

    function fresh() {
      return !!ready && Date.now() - ready.at < PASSKEY_FRESH_MS;
    }

    function begin() {
      return request('POST', path).then(function (res) {
        if (res.status !== 200 || !res.body || !res.body.publicKey) return { res: res };
        try {
          return { options: parse(res.body.publicKey) };
        } catch (e) {
          return { unreadable: true };
        }
      });
    }

    function warm() {
      if (fresh() || pending || !showing()) return;
      var mine = generation;
      ready = null;
      pending = begin().then(function (got) {
        if (mine !== generation) return;
        pending = null;
        if (!got.options) return;
        ready = { options: got.options, at: Date.now() };
        clearTimeout(timer);
        timer = setTimeout(function () { ready = null; warm(); }, PASSKEY_FRESH_MS);
      });
    }

    // Synchronous, because the whole point is what happens in the tap.
    function take() {
      var options = fresh() ? ready.options : null;
      ready = null;
      return options;
    }

    function later() {
      return (pending || Promise.resolve()).then(function () {
        var options = take();
        return options ? { options: options } : begin();
      });
    }

    function drop() {
      generation += 1;
      ready = null;
      pending = null;
    }

    // For when the answer would now be different — the exclusion list after a
    // passkey is added or removed — and for after any attempt, used or not.
    function renew() {
      drop();
      warm();
    }

    return { warm: warm, take: take, later: later, drop: drop, renew: renew };
  }

  function panelShowing(id) {
    var panel = byId(id);
    var screen = byId('authScreen');
    return PASSKEYS_OFFERED && !!panel && !panel.hasAttribute('hidden') &&
      !!screen && !screen.hasAttribute('hidden') && document.visibilityState !== 'hidden';
  }

  var registerCeremony = preparedCeremony('/auth/passkeys/register/begin', creationOptions,
    function () { return state === 'in' && panelShowing('panelAccount'); });
  var loginCeremony = preparedCeremony('/auth/passkeys/login/begin', requestOptions,
    function () { return panelShowing('panelLogin'); });

  // A phone that was in a pocket comes back with timers that never fired.
  document.addEventListener('visibilitychange', function () {
    registerCeremony.warm();
    loginCeremony.warm();
  });

  // Called now, in the caller's turn, and never from a then(): the tap has to
  // still be on the stack. What it buys is that a stand-in for
  // navigator.credentials which throws instead of rejecting is reported like
  // any other refusal, rather than leaving the button disabled for good.
  function asked(call) {
    try {
      return Promise.resolve(call());
    } catch (e) {
      return Promise.reject(e);
    }
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
    say(msg, t('pk.follow'));

    function create(options, prepared) {
      var startedAt = Date.now();
      var focused = document.hasFocus();
      return asked(function () { return navigator.credentials.create({ publicKey: options }); })
        .then(function (cred) {
          return request('POST', '/auth/passkeys/register/finish', {
            label: label,
            credential: registrationJSON(cred)
          });
        })
        .then(function (fin) {
          add.disabled = false;
          registerCeremony.renew();
          if (fin.status === 201 && fin.body) {
            labelField.value = '';
            passkeys.push(fin.body);
            drawPasskeys();
            say(msg, t('pk.ok'));
            return;
          }
          say(msg, problem(fin, {}), true);
        }, function (err) {
          add.disabled = false;
          registerCeremony.renew();
          reportRefusal(err, 'register', startedAt, prepared, focused);
          var said = ceremonyMessage(err, 'register', startedAt);
          say(msg, said.text, said.bad);
        });
    }

    var options = registerCeremony.take();
    if (options) {
      create(options, true);
      return;
    }
    registerCeremony.later().then(function (got) {
      if (got.options) return create(got.options, false);
      add.disabled = false;
      say(msg, got.unreadable ? t('pk.e.readsetup') : problem(got.res, {}), true);
      return null;
    });
  }

  function passkeyLogin() {
    var msg = byId('loginMsg');
    var btn = byId('loginPasskey');
    btn.disabled = true;
    say(msg, t('pk.follow'));

    function get(options, prepared) {
      var startedAt = Date.now();
      var focused = document.hasFocus();
      return asked(function () { return navigator.credentials.get({ publicKey: options }); })
        .then(function (cred) {
          return request('POST', '/auth/passkeys/login/finish', { credential: assertionJSON(cred) });
        })
        .then(function (fin) {
          btn.disabled = false;
          if (fin.status === 200 && fin.body) {
            loginSucceeded(fin.body);
            return;
          }
          loginCeremony.renew();
          say(msg, problem(fin, { invalid_credentials: t('pk.e.nologin') }), true);
        }, function (err) {
          btn.disabled = false;
          loginCeremony.renew();
          reportRefusal(err, 'login', startedAt, prepared, focused);
          var said = ceremonyMessage(err, 'login', startedAt);
          say(msg, said.text, said.bad);
        });
    }

    // No email is sent and none is needed: the server never looks one up, and
    // a list of credentials for an address — full for a real account, empty
    // for a stranger — is the account enumeration this whole surface avoids.
    // The authenticator shows its own picker.
    var options = loginCeremony.take();
    if (options) {
      get(options, true);
      return;
    }
    loginCeremony.later().then(function (got) {
      if (got.options) return get(got.options, false);
      btn.disabled = false;
      say(msg, got.unreadable ? t('pk.e.readchallenge') : problem(got.res, {}), true);
      return null;
    });
  }

  /* ------------------------------------------------------------------
   * Wiring
   * ------------------------------------------------------------------ */

  applyStrings();
  fillLanguageChoice(byId('newUserLanguage'), null);
  bindLogin();
  bindSetPassword();
  bindAdmin();
  bindPasskeys();
  bindReminders();

  var activityMore = byId('activityMore');
  if (activityMore) activityMore.addEventListener('click', loadActivity);

  var signOutBtn = byId('signOutBtn');
  if (signOutBtn) signOutBtn.addEventListener('click', signOut);

  each(document.querySelectorAll('[data-auth-goto]'), function (el) {
    el.addEventListener('click', function () { goto(el.getAttribute('data-auth-goto')); });
  });

  /* Landmarks, and the live region the swap needs.
   *
   * The page has two halves and shows one of them: the planner, or the
   * accounts screens. Whichever is up is the main content of the page while
   * it is up, and the other is out of the accessibility tree either way -
   * hidden, or display:none - so marking both leaves exactly one main
   * landmark at any moment, and "skip to the content" lands past the language
   * switcher whichever half that is.
   *
   * The lede under the auth heading is the sentence a screen leads with, and
   * the one after a link is redeemed says the password is saved: the first
   * thing an invited person is told, on a screen where focus belongs in the
   * email field. So it is announced, not only drawn.
   *
   * Markup would carry all three more plainly. They are set from here because
   * this is the file that swaps the halves and writes that sentence.
   */
  each(['plannerWrap', 'authScreen'], function (id) {
    var half = byId(id);
    if (half) half.setAttribute('role', 'main');
  });
  var authLede = byId('authLede');
  if (authLede) {
    authLede.setAttribute('role', 'status');
    authLede.setAttribute('aria-live', 'polite');
  }

  render();
  probeSession();
})();
