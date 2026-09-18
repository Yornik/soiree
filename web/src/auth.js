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
      'setpw.submit': 'Set password',
      'setpw.incomplete.open': 'This link is incomplete. Open the whole link from your email, or ask an admin for a new one.',
      'setpw.incomplete': 'This link is incomplete. Ask an admin for a new one.',
      'setpw.mismatch': 'The two passwords are not the same.',
      'setpw.short': 'A password needs at least 12 characters.',
      'setpw.weak': 'Choose a password between 12 and 1024 characters.',
      'setpw.expired': 'This link has expired or has already been used. Ask an admin for a new one.',
      'setpw.done.title': 'Password set',
      'setpw.done.body': 'Your password is saved. Sign in with it to reach the planner.',
      'people.title': 'People',
      'people.lede': 'Everyone with an account on this planner.',
      'people.notadmin': 'Managing accounts is an admin\'s job. Ask an admin to make the change for you.',
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
      'setpw.submit': 'Wachtwoord instellen',
      'setpw.incomplete.open': 'Deze link is onvolledig. Open de hele link uit je e-mail, of vraag een beheerder om een nieuwe.',
      'setpw.incomplete': 'Deze link is onvolledig. Vraag een beheerder om een nieuwe.',
      'setpw.mismatch': 'De twee wachtwoorden zijn niet gelijk.',
      'setpw.short': 'Een wachtwoord heeft minstens 12 tekens nodig.',
      'setpw.weak': 'Kies een wachtwoord van 12 tot 1024 tekens.',
      'setpw.expired': 'Deze link is verlopen of al gebruikt. Vraag een beheerder om een nieuwe.',
      'setpw.done.title': 'Wachtwoord ingesteld',
      'setpw.done.body': 'Je wachtwoord is opgeslagen. Meld je ermee aan om bij de planner te komen.',
      'people.title': 'Mensen',
      'people.lede': 'Iedereen met een account op deze planner.',
      'people.notadmin': 'Accounts beheren is het werk van een beheerder. Vraag een beheerder om de wijziging voor je te doen.',
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
      'setpw.submit': 'Simpan kata sandi',
      'setpw.incomplete.open': 'Tautan ini tidak lengkap. Buka tautan utuh dari emailmu, atau minta yang baru ke admin.',
      'setpw.incomplete': 'Tautan ini tidak lengkap. Minta yang baru ke admin.',
      'setpw.mismatch': 'Kedua kata sandi tidak sama.',
      'setpw.short': 'Kata sandi harus minimal 12 karakter.',
      'setpw.weak': 'Pilih kata sandi antara 12 dan 1024 karakter.',
      'setpw.expired': 'Tautan ini sudah kedaluwarsa atau sudah dipakai. Minta yang baru ke admin.',
      'setpw.done.title': 'Kata sandi tersimpan',
      'setpw.done.body': 'Kata sandimu sudah tersimpan. Masuk dengan kata sandi itu untuk membuka perencana.',
      'people.title': 'Anggota',
      'people.lede': 'Semua orang yang punya akun di perencana ini.',
      'people.notadmin': 'Mengelola akun adalah tugas admin. Minta admin untuk melakukan perubahan itu.',
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
      acts.appendChild(barButton(t('signin'), 'link-btn', 'login'));
      return;
    }
    if (state !== 'in' || !user) return;

    who.appendChild(make('span', 'account-email', user.email));
    who.appendChild(make('span', 'account-role', roleWord(user.role)));

    if (isAdmin()) acts.appendChild(barButton(t('bar.people'), 'link-btn', 'admin'));
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
      renderLogin(t(endedNotice ? 'login.ended' : 'login.lede'));
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
      renderLogin(t(path === 'admin' ? 'login.lede.admin' : 'login.lede.account'));
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
    showPanel('panelLogin', t('signin'), lede);
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
            // Redeeming revoked every session this account had, including
            // this browser's if it had one.
            setSession('out');
            showNote(t('setpw.done.title'), '', t('setpw.done.body'));
            var b2 = byId('authNoteAct');
            show(b2, true);
            setText(b2, t('signin'));
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
      fillLanguageChoice(lang, null);
      acts.appendChild(lang);
      acts.appendChild(actButton(
        t(p.status === 'invited' ? 'person.resend' : 'person.sendlink'),
        'link-btn',
        function (btn) { invite(p, btn, lang.value); }
      ));
    }

    if (!self) {
      acts.appendChild(actButton(
        t(p.status === 'disabled' ? 'person.access.on' : 'person.access.off'),
        'link-btn',
        function (btn) {
          patchPerson(p, { status: p.status === 'disabled' ? 'active' : 'disabled' }, btn);
        }
      ));
      acts.appendChild(actButton(t('remove'), 'link-btn danger', function (btn) {
        if (!window.confirm(t('person.confirm', { email: p.email }))) return;
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
        adminSays(t('person.conflict'), true);
        return;
      }
      adminSays(problem(res, {}), true);
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
          adminSays(t('person.removed', { email: p.email }));
          return;
        }
        if (res.status === 409 && res.body && res.body.current) {
          adoptPerson(res.body.current);
          adminSays(t('person.conflict.retry'), true);
          return;
        }
        adminSays(problem(res, {}), true);
      });
  }

  function invite(p, control, language) {
    if (control) control.disabled = true;
    adminSays('');
    request('POST', '/users/' + p.id + '/invite', language ? { language: language } : {}).then(function (res) {
      if (control) control.disabled = false;
      if (res.status === 200 && res.body) {
        if (res.body.user) adoptPerson(res.body.user);
        afterInvite(res.body, p.email);
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

      li.appendChild(actButton(t('remove'), 'link-btn danger', function (btn) {
        if (!window.confirm(t('pk.confirm', { label: k.label || t('pk.this') }))) return;
        btn.disabled = true;
        request('DELETE', '/auth/passkeys/' + k.id).then(function (res) {
          btn.disabled = false;
          if (res.status === 204) {
            passkeys = passkeys.filter(function (x) { return x.id !== k.id; });
            drawPasskeys();
            say(byId('passkeyMsg'), t('pk.removed'));
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
    if (name === 'InvalidStateError') return t('pk.e.device');
    if (name === 'SecurityError') return t('pk.e.origin');
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
    say(msg, t('pk.follow'));

    request('POST', '/auth/passkeys/register/begin').then(function (res) {
      if (res.status !== 200 || !res.body || !res.body.publicKey) {
        add.disabled = false;
        say(msg, problem(res, {}), true);
        return null;
      }
      var options;
      try {
        options = creationOptions(res.body.publicKey);
      } catch (e) {
        add.disabled = false;
        say(msg, t('pk.e.readsetup'), true);
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
            say(msg, t('pk.ok'));
            return;
          }
          say(msg, problem(fin, {}), true);
        }, function (err) {
          add.disabled = false;
          say(msg, ceremonyMessage(err, t('pk.e.setup')), true);
        });
    });
  }

  function passkeyLogin() {
    var msg = byId('loginMsg');
    var btn = byId('loginPasskey');
    btn.disabled = true;
    say(msg, t('pk.follow'));

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
        say(msg, t('pk.e.readchallenge'), true);
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
          say(msg, problem(fin, { invalid_credentials: t('pk.e.nologin') }), true);
        }, function (err) {
          btn.disabled = false;
          say(msg, ceremonyMessage(err, t('pk.e.signin')), true);
        });
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
