# Changelog

## [1.3.0](https://github.com/Yornik/soiree/compare/v1.2.1...v1.3.0) (2026-09-21)


### Features

* **api:** a create whose answer is lost no longer adds the line twice ([c40d677](https://github.com/Yornik/soiree/commit/c40d677883ae7c3a22a08c9c8fe757c2391df000))
* **api:** a live stream says which build is answering it ([eafa551](https://github.com/Yornik/soiree/commit/eafa551794b54e6849a99b05a58a6164c77bedc0))
* **api:** an admin can narrow the activity feed to one row ([dec0fc8](https://github.com/Yornik/soiree/commit/dec0fc89d0ac490436237802b327899595a9d484))
* **api:** an emptied plan is not filled back in by a stale browser ([fc49484](https://github.com/Yornik/soiree/commit/fc49484ccbc4f17dc33af372093d8f261a0a9e0a))
* **api:** an event's name and date can be kept off the public page ([914fe47](https://github.com/Yornik/soiree/commit/914fe47b106e3b00b64c8fc43ae8dcaeeb2acc84))
* **api:** the page arrives under the policy it is written to ([f55417e](https://github.com/Yornik/soiree/commit/f55417eaffe121ed65460eb45bee2dddc199fa40))
* **api:** the plan and the activity feed come back gzipped ([8f3da24](https://github.com/Yornik/soiree/commit/8f3da243629314270ea5005d5c619a68cbdbabf5))
* **auth:** an admin can take away every way into an account ([4aa23eb](https://github.com/Yornik/soiree/commit/4aa23ebe05542797aa6295233c93c295166bb660))
* **auth:** change your own password, and end every other session with it ([f8cc514](https://github.com/Yornik/soiree/commit/f8cc514b23f53d2699458bf8cd749ece464ed7df))
* **auth:** the invitation says what it is an invitation to ([1620c25](https://github.com/Yornik/soiree/commit/1620c252b0e5debeb6427c1fdeff2c370a10c9cb))
* **metrics:** a deadline digest that never went out can raise an alert ([3522b40](https://github.com/Yornik/soiree/commit/3522b404e72e8b64b67fa463a9ddeb28674ea65c))
* **metrics:** the routes worth watching are no longer all api-other ([1195335](https://github.com/Yornik/soiree/commit/119533562010912fea82f7280976ab492b0cf332))
* **passkeys:** the page tells the server what the browser said when it refused ([f10f19b](https://github.com/Yornik/soiree/commit/f10f19bcd8b7289563fe1ad037e858970acbeff9))
* **privacy:** erasing somebody now clears their name from the history ([d80b57b](https://github.com/Yornik/soiree/commit/d80b57b8ccf03831ba549137fa3651e0d9712f3f))
* **reminders:** the digest says where to go, in the deployment's language ([1947acd](https://github.com/Yornik/soiree/commit/1947acd8db167efa473690e0a828adf964c0a264))
* **sheetimport:** the importer speaks all three interface languages ([c4854f5](https://github.com/Yornik/soiree/commit/c4854f53756d19fbc7a891c978ac5daea845f8d3))
* **web:** a budget line can be moved, and a long grid searched ([25880cb](https://github.com/Yornik/soiree/commit/25880cbd4013041f816bee8b7b81d3bfc298adc8))
* **web:** a budget line can carry a vendor and a decide-by date ([fcdf4f2](https://github.com/Yornik/soiree/commit/fcdf4f215ce5d522dc041678945aecd7d82d678e))
* **web:** an open planner says when the site has been updated under it ([0519680](https://github.com/Yornik/soiree/commit/05196804ede67eb12503fb765eb14bd06fd6aa8d))
* **web:** the plan prints on paper, all of it, in ink you can read ([4da9310](https://github.com/Yornik/soiree/commit/4da9310fbb5e5e2d53b75102b76533af5f1a8774))
* **web:** what is still owed shows up, per line and per person ([6c47b66](https://github.com/Yornik/soiree/commit/6c47b66dedce948858ce05720a9c1d119fc193c0))


### Bug Fixes

* **api:** a date in the activity feed reads as a date, not a timestamp ([e9c0767](https://github.com/Yornik/soiree/commit/e9c07676629d9c7a0383c6c0a711c15d548bfbd4))
* **api:** a form on a sibling origin can no longer write with your session ([cd41199](https://github.com/Yornik/soiree/commit/cd4119998d476f52967ed33b56df4ac424e0bb5c))
* **api:** a live stream ends when the session behind it does ([8310aec](https://github.com/Yornik/soiree/commit/8310aecc18317af0c793ab63eaab4893415b52dd))
* **api:** a push subscription cannot name an address on this network ([478d263](https://github.com/Yornik/soiree/commit/478d263f3fb821a11543be479a943f78b2752a06))
* **api:** a quantity the column cannot hold is refused, not a 500 ([b7e4dc2](https://github.com/Yornik/soiree/commit/b7e4dc2f316a602a9dc1e7affc47fcf24855abf3))
* **api:** a rate the column cannot hold exactly is refused, not rounded ([4411115](https://github.com/Yornik/soiree/commit/44111151790220f0ad589381fa6115e174c7a2cf))
* **auth:** a database outage no longer signs everybody out ([2583020](https://github.com/Yornik/soiree/commit/25830205db9194602860ae3726524694c4cf85b8))
* **auth:** a reset request no longer discards the link an admin handed over ([26a2a8a](https://github.com/Yornik/soiree/commit/26a2a8a34b2fb4a0379c1fbde36d612ee8305631))
* **auth:** a stranger's wrong guesses no longer lock you out of signing in ([0c08e88](https://github.com/Yornik/soiree/commit/0c08e8821b2af3cfbaf35b0b1ea12d31317dd4a0))
* **auth:** the set-password floor counts characters, not bytes ([e7f616a](https://github.com/Yornik/soiree/commit/e7f616ad27c6114352280ce8f2765a34b122bb1f))
* **config:** a relay password with no relay is refused, like the sender is ([c1f6d42](https://github.com/Yornik/soiree/commit/c1f6d42be4919a149e389eb027b1a7f45c13e8d8))
* **deps:** update module github.com/go-webauthn/webauthn to v0.18.2 ([#38](https://github.com/Yornik/soiree/issues/38)) ([36e7e37](https://github.com/Yornik/soiree/commit/36e7e37a181fca6025577d2994133a3e72c782be))
* **mail:** an accented subject is folded rather than sent over the limit ([24b5b70](https://github.com/Yornik/soiree/commit/24b5b70639939cecd2d8e681363cb5b9ae136144))
* **main:** a migration gives up on a lock rather than stall the release still serving ([31adb8f](https://github.com/Yornik/soiree/commit/31adb8fc3d8909ce5f46e588c6477ca44853cec2))
* **main:** a second signal stops a shutdown that is going nowhere ([c268cc9](https://github.com/Yornik/soiree/commit/c268cc92eaef1db94af1e4d5ead99f7227b251c3))
* **main:** bind failures, handler panics and a bad VAPID pair now fail loudly ([c75edb4](https://github.com/Yornik/soiree/commit/c75edb4880caddf9ad7926152726f5f79c4cd370))
* **main:** the development stack comes up with a way to sign in ([8490591](https://github.com/Yornik/soiree/commit/8490591a9d16460ac5b50fb31f76e53a7c0692dc))
* **main:** the development stack stays on the machine it runs on ([55256a6](https://github.com/Yornik/soiree/commit/55256a6014b069abf8dbb464e3951f606c282ea3))
* **reminders:** a digest delivered during a shutdown is no longer reported as lost ([088e648](https://github.com/Yornik/soiree/commit/088e6482045ba112f3223f77120124d43ee62a70))
* **reminders:** one refused address no longer cancels everybody's digest ([6f8c92e](https://github.com/Yornik/soiree/commit/6f8c92e8fc8e84d875972efbcfacf6192702d5f7))
* **reminders:** the digest says why it arrived and how to stop it ([ebd72e6](https://github.com/Yornik/soiree/commit/ebd72e60c51e9959e170592032f438be318e59d3))
* **sheetimport:** a sheet in sections no longer imports its subtotals unflagged ([41b1188](https://github.com/Yornik/soiree/commit/41b1188099fc8f1fe6945cf6827e1b163aa366ca))
* **sheetimport:** dates in one column are read one way, not cell by cell ([ca27478](https://github.com/Yornik/soiree/commit/ca27478862d92e280125da2f5c63a8d04227015d))
* **store:** a row names the account that last wrote it, not nobody ([66fa6b3](https://github.com/Yornik/soiree/commit/66fa6b3359f4696679dfb8270529a4998fdac8f2))
* **store:** adding or removing a passkey says so in the activity ([bcef6f8](https://github.com/Yornik/soiree/commit/bcef6f857b516f8dec5fcdde9dcdecc5547462e0))
* **store:** deleting a line in a parent loop no longer hangs ([ca87f18](https://github.com/Yornik/soiree/commit/ca87f18bfbd1ce4e61637b63033bbf7eb0c4a6f1))
* **store:** revoking an account's credentials says so in the activity ([95f2155](https://github.com/Yornik/soiree/commit/95f2155b35fe3da37282f60afd9d7b6dc929165e))
* **web:** a comma or a grouped thousand is read the way it was typed ([34bdab2](https://github.com/Yornik/soiree/commit/34bdab27c7f77f7f35f32718d62d6b7da7598ede))
* **web:** a create answered a second time keeps somebody else's correction ([d4a8957](https://github.com/Yornik/soiree/commit/d4a8957b428cd87456d76eefcf10a5d3ad9c0d61))
* **web:** a delete that takes files or a split with it asks first ([375ea51](https://github.com/Yornik/soiree/commit/375ea516447e2f752b29b543b13aa7aaa44ff082))
* **web:** a deploy that half arrives no longer takes the offline copy with it ([7197174](https://github.com/Yornik/soiree/commit/7197174fe1e716d6ba9bdce5335d8f84ceb63529))
* **web:** a page opened while the server is away keeps trying, and says so ([4fffc3e](https://github.com/Yornik/soiree/commit/4fffc3e96d0590148044b27cf969b29b270c1fe6))
* **web:** a signed-out visitor is asked to sign in, not to start the plan ([d560a57](https://github.com/Yornik/soiree/commit/d560a57b1b8d09c6e7cd5857b8def1bcff47de80))
* **web:** a tap into a field no longer zooms the phone, and dark stays dark ([88e9ea1](https://github.com/Yornik/soiree/commit/88e9ea1adcc036b32d3c3adb0155edee350eca0d))
* **web:** a task reads on a phone instead of running off the screen ([da669dd](https://github.com/Yornik/soiree/commit/da669dd51ffe0bd2ed04630b949e074bea940ada))
* **web:** an edit on a line two people share is kept, or said to be gone ([1959f77](https://github.com/Yornik/soiree/commit/1959f77d2d019dd210c97dfde09ad9c246c3bbcf))
* **web:** an exchange rate below one is a rate the field takes ([81a33cc](https://github.com/Yornik/soiree/commit/81a33ccfec4d7462b8321db4a10134488b40485d))
* **web:** an unsent edit is still there after a reload ([8653815](https://github.com/Yornik/soiree/commit/8653815ec270b12e940e026f65fcc442d2a873c8))
* **web:** every field in the budget and task grid says which column it is ([55c1720](https://github.com/Yornik/soiree/commit/55c17208f35f2ab1e8a7b6ea109c1d8b632b5ad4))
* **web:** nobody is left stuck on an accounts screen or typing a password blind ([94e22b4](https://github.com/Yornik/soiree/commit/94e22b4307ae57abcb5d1179a8b95025d8940d54))
* **web:** the accounts list gives focus back after every change ([1fd8fc2](https://github.com/Yornik/soiree/commit/1fd8fc285f08c6d95628ca53c52dd449b5c74e25))
* **web:** the overdue ring answers a press when work is due today too ([2931537](https://github.com/Yornik/soiree/commit/2931537cfabe420adfae1466608e34ff7a932607))
* **web:** the service worker no longer reinstalls itself after every restart ([c9a631b](https://github.com/Yornik/soiree/commit/c9a631b60e66550bf9a6b8e7136902213ed98831))
* **web:** two tabs of one planner stop overwriting each other ([4b3ac91](https://github.com/Yornik/soiree/commit/4b3ac91398b7185dcb72eadc2023fc9930f152a9))

## [1.2.1](https://github.com/Yornik/soiree/compare/v1.2.0...v1.2.1) (2026-09-19)


### Bug Fixes

* **passkeys:** ask for the prompt inside the tap, and say why it failed ([6685703](https://github.com/Yornik/soiree/commit/668570330813291ff115f14f0491af1aa5eb575b))
* **passkeys:** stop refusing sign-ins over extension outputs nobody requested ([1be2952](https://github.com/Yornik/soiree/commit/1be2952a10d1400e31fcb2d7e11a3ebaf384218b))
* **push:** a tapped reminder no longer reloads the open planner ([f789258](https://github.com/Yornik/soiree/commit/f789258c929f9c9b48eb19167e497bd12cf6bf05))
* **push:** say what is actually wrong when reminders cannot be turned on ([2ee9a15](https://github.com/Yornik/soiree/commit/2ee9a15c0f82b594870471cc719fb564819b7c61))
* **web:** keep the run-up legible when due dates crowd together ([9e65ffc](https://github.com/Yornik/soiree/commit/9e65ffcb7e54ca59e1016b5bdd08dc4b8763eead))
* **web:** serve the service worker and the manifest as written ([b76736d](https://github.com/Yornik/soiree/commit/b76736d1f99f6379a9de1a8b921d2f8584831a1c))
* **web:** the worker answers only the planner from its cache ([39890be](https://github.com/Yornik/soiree/commit/39890be1d93d95a3490e7cbd03937edfdd4e6dc8))

## [1.2.0](https://github.com/Yornik/soiree/compare/v1.1.1...v1.2.0) (2026-09-18)


### Features

* **web:** tasks and budget lines show their state, and shares are drawn ([cbef915](https://github.com/Yornik/soiree/commit/cbef9150bccbdea5034b16657f7bca5c547620f7))
* **web:** the run-up - a new look, and an overview built around the date ([79b32e1](https://github.com/Yornik/soiree/commit/79b32e1ef419d612bb6d2810fb0a0760490cb371))
* **web:** the typeface at every size, and icons a phone will accept ([a75f498](https://github.com/Yornik/soiree/commit/a75f49863bc1efb57e9522f508931bf29f7dc650))

## [1.1.1](https://github.com/Yornik/soiree/compare/v1.1.0...v1.1.1) (2026-09-18)


### Bug Fixes

* the event is on the day it was written for, for every reader ([3b5e2e8](https://github.com/Yornik/soiree/commit/3b5e2e83e898a545be280d06d5bb47acf5c527e0))
* **web:** a long name makes its row taller instead of hiding behind a scrollbar ([df01ff9](https://github.com/Yornik/soiree/commit/df01ff91f78df5490b33d0a3c509962451d755b1))
* **web:** a paperclip that can be seen, and pressed on a line with a long name ([1f59872](https://github.com/Yornik/soiree/commit/1f59872357be3e3904086ba0cf69b577f6771830))


### Performance Improvements

* compress each asset once per process, which makes the tests twelve times faster ([eed4c0d](https://github.com/Yornik/soiree/commit/eed4c0d36a12268213a0c43fbb42b6c200d96ac2))

## [1.1.0](https://github.com/Yornik/soiree/compare/v1.0.2...v1.1.0) (2026-09-18)


### Features

* **api:** an activity feed of everything that changed, for admins ([9dc3b59](https://github.com/Yornik/soiree/commit/9dc3b592a7b5e2958613bf5d223d0de4f876b5bc))
* **attachments:** configuration - all five bucket settings or none ([584b147](https://github.com/Yornik/soiree/commit/584b147a8a84a1a51c2edb86ed53370a830a806a))
* **attachments:** four routes and a sweeper, with no file passing through ([66cf9a4](https://github.com/Yornik/soiree/commit/66cf9a4c5502e4552f556db4020de4d51a5b2dbf))
* **attachments:** the schema, and the one S3 routine everything else uses ([ffd4005](https://github.com/Yornik/soiree/commit/ffd4005b28252641304fa4cbd3019291b49c4131))
* **attachments:** the store - quota, confirmation, and what a cascade forgets ([dc9da58](https://github.com/Yornik/soiree/commit/dc9da585700329e987d4f82fc1c40f1a01cb6f0d))
* **web:** an activity screen, so an admin can see who changed what ([d190cec](https://github.com/Yornik/soiree/commit/d190cecf6eea473a8bb49ca29963d9b1fb966bd6))
* **web:** attach files to budget lines and tasks, and get them back ([6ed79e6](https://github.com/Yornik/soiree/commit/6ed79e627938697c1c5c0a50c74598b8a39b0650))
* **web:** show the release, very small, to somebody signed in ([677ae1a](https://github.com/Yornik/soiree/commit/677ae1a021ccec284ec9b452c032cca2fde9b386))


### Bug Fixes

* **attachments:** close response bodies the way the linter and the rest of the code do ([a007db0](https://github.com/Yornik/soiree/commit/a007db089e4aa28765a1a49c54572779f0e9cbcf))
* **attachments:** the expired-address test cleans up after itself ([d96f59e](https://github.com/Yornik/soiree/commit/d96f59e84d3028b05064f992b7cad4712c6f5f14))
* **attachments:** the two response bodies the linter had not got to yet ([1671063](https://github.com/Yornik/soiree/commit/16710634dc67dcbb40b2c6b56a0f925473eca1ee))
* **web:** the whole budget table fits a desktop screen, remove button included ([910fab1](https://github.com/Yornik/soiree/commit/910fab19511a6572f677f9e748a8a98b1869b5bd))

## [1.0.2](https://github.com/Yornik/soiree/compare/v1.0.1...v1.0.2) (2026-09-18)


### Bug Fixes

* **web:** an import made after signing out and back in is actually saved ([b935772](https://github.com/Yornik/soiree/commit/b9357727096e4c4453fe2e2d77469b21d3968c99))

## [1.0.1](https://github.com/Yornik/soiree/compare/v1.0.0...v1.0.1) (2026-09-18)


### Bug Fixes

* **web:** a 401 ends the session at once, without asking again ([a23f9cb](https://github.com/Yornik/soiree/commit/a23f9cba56e416f9c49cb5f494e343da3c2a884d))

## [1.0.0](https://github.com/Yornik/soiree/compare/v0.4.0...v1.0.0) (2026-09-18)


### Features

* **accounts:** a person has a language, and is written to in it ([5ec0107](https://github.com/Yornik/soiree/commit/5ec01076ca4e3a3206863d1890b2a898a6320aa3))
* **web:** a switch for reminders that is still there afterwards ([48a171a](https://github.com/Yornik/soiree/commit/48a171af2cd62c87174c697ce95c55c4e0798ebe))
* **web:** live sync, a theme toggle, and deadline notifications ([5a8812f](https://github.com/Yornik/soiree/commit/5a8812f9170110cdfe0ca6fe24b4e851b64afae9))
* **web:** signing out takes the plan off that browser ([921b17d](https://github.com/Yornik/soiree/commit/921b17db9abb9f54e22b8c13464fc2c6f1f75366))
* **web:** the accounts screens speak the planner's three languages ([16767a9](https://github.com/Yornik/soiree/commit/16767a99b6a046b084a7748192eff24dac5b62ff))
* **web:** the accounts surface gets an interface ([101da4a](https://github.com/Yornik/soiree/commit/101da4a14d1bd4d084a4734f2f76feb2ae1aded6))


### Bug Fixes

* **api:** correct what the specification still had wrong, and pin it ([6ad1f52](https://github.com/Yornik/soiree/commit/6ad1f526f1508e5169ee76d8027aa960420f2e41))
* **api:** require a session for the plan, and record who changed what ([f45a7f6](https://github.com/Yornik/soiree/commit/f45a7f623e33366d5d8f724a7f92f51de500c1ec))
* **auth:** hash four passwords at a time, not as many as are asked for ([886279b](https://github.com/Yornik/soiree/commit/886279b281b0afc168ec39bb1aa0cb7f16acd6a3))
* **deps:** update module github.com/fxamacker/cbor/v2 to v2.9.4 ([#13](https://github.com/Yornik/soiree/issues/13)) ([f0a81ac](https://github.com/Yornik/soiree/commit/f0a81acc0f9e3e71493a58be02bf4986f51ab4d0))
* **e2e:** sign the browser in, and let the planner notice ([aa96015](https://github.com/Yornik/soiree/commit/aa96015771259acd0cadfc25d9d6ca78afbd7fa1))
* **language:** ask the reader, and store nothing about it on an account ([2a40cd4](https://github.com/Yornik/soiree/commit/2a40cd4d9b8b7783984cfbce456c44648ab4bead))
* **privacy:** the event's real date was in the source, and the check missed it ([5770c73](https://github.com/Yornik/soiree/commit/5770c73963f0ee32db5f79feb3fb9b66a101bc2b))
* **web:** a copy edited while signed out is merged, not pushed over the plan ([f02013f](https://github.com/Yornik/soiree/commit/f02013ff659ac7f1e01adbb9e2a335c28e77a717))
* **web:** a session that ends mid-use is noticed, survived, and merged ([89ac26e](https://github.com/Yornik/soiree/commit/89ac26e81724437feace24b6857fa47123f6d682))
* **web:** a set-password link does not survive leaving the panel ([9ae6c3c](https://github.com/Yornik/soiree/commit/9ae6c3c72a2ac4468dd0792eec05dd9fd365565d))


### Miscellaneous Chores

* release 1.0.0 ([d5a678c](https://github.com/Yornik/soiree/commit/d5a678c89e75fe418dab293cc2a828a4051ed894))

## [0.4.0](https://github.com/Yornik/soiree/compare/v0.3.0...v0.4.0) (2026-09-18)


### Features

* **reminders:** send the digest to every active admin ([b354b01](https://github.com/Yornik/soiree/commit/b354b0137b3cf7efdc781b46fe4aa2f6b37e7071))

## [0.3.0](https://github.com/Yornik/soiree/compare/v0.2.1...v0.3.0) (2026-09-18)


### Features

* add observability, dev database stack and a schema drawn from real sheets ([08bcf87](https://github.com/Yornik/soiree/commit/08bcf8716c9fc8fbfc0e5c669263242d98c118bc))
* **api:** REST API over the store, mounted only when a DSN is set ([6b7f91c](https://github.com/Yornik/soiree/commit/6b7f91cee7810ade92fd0d4b17eae29014b5a814))
* **auth:** accounts, roles and sessions ([b43607c](https://github.com/Yornik/soiree/commit/b43607ccd153ae8f914066feac2129084810e85f))
* **auth:** give the first admin a way in that does not need mail ([6053474](https://github.com/Yornik/soiree/commit/6053474194cffd718855ee20c553ebba365a73bb))
* **ci:** sign releases with cosign, publish SBOM, pin actions by digest ([c631369](https://github.com/Yornik/soiree/commit/c63136916a839731ca0ce4838e2eb737274e8640))
* **e2e:** browser-level tests for the planner ([5165f23](https://github.com/Yornik/soiree/commit/5165f2304baa0be30b3a1f97c47c23888ffcaa4a))
* keep the site out of search engines unless asked ([c1cd301](https://github.com/Yornik/soiree/commit/c1cd301acfa6469b5b2964b69b8e591217625ca1))
* **metrics:** serve /metrics on a listener of its own ([1894c69](https://github.com/Yornik/soiree/commit/1894c69ee03462a337e86ea5d320381d34586732))
* **reminders:** weekly digest of approaching lock_by and task deadlines ([80e8fc9](https://github.com/Yornik/soiree/commit/80e8fc92e08d5fbe9287510a936a661342305dfd))
* **sheetimport:** tolerant .ods/.csv importer with an explicit mapping ([366f2d6](https://github.com/Yornik/soiree/commit/366f2d6140693d0ba5372dec510cc4dad6cc6bb5))
* start the reminder digest, and document the metrics listener ([c2e6245](https://github.com/Yornik/soiree/commit/c2e6245335ea74ac0a23a1dd678266164ca10158))
* **store:** add schema migrations and a typed pgx store layer ([90bc5ed](https://github.com/Yornik/soiree/commit/90bc5ed7f1387808eba2b50604fab118899e07fd))
* **store:** record an append-only history of every change ([ac0b4a3](https://github.com/Yornik/soiree/commit/ac0b4a3fb231382d1749cbe5d6d825bef0a3e477))
* **store:** subject export, erasure and retention purge ([434d428](https://github.com/Yornik/soiree/commit/434d4286c8147999e5d1cba47d3a11dc417cd07a))
* **web:** a phone layout for the budget grid, three languages, and an archive ([39ac1c6](https://github.com/Yornik/soiree/commit/39ac1c6f1b7a1d05e095087e77954e086cd4cdf3))
* **web:** design pass — ledger typography, honest colour, first-run screen ([7fed9e1](https://github.com/Yornik/soiree/commit/7fed9e12c47fb270f71191a41b1e374887c7274d))


### Bug Fixes

* **api,phases:** speak major units at the API boundary, give phases a revision ([c1f9fa9](https://github.com/Yornik/soiree/commit/c1f9fa94bdbb7e0c541c65e86f801174a59d46ca))
* **api:** log the cause behind a 500 instead of dropping it ([bd39d80](https://github.com/Yornik/soiree/commit/bd39d808814496bb0766484dac7aeab9e87f9cb4))
* **api:** mark the mux's own 405 and 404 no-store too ([525aea1](https://github.com/Yornik/soiree/commit/525aea1ce74bd65bdc1ef43a979c75a1ba6578a6))
* **auth:** slide the session cookie, not just the row ([68cb8be](https://github.com/Yornik/soiree/commit/68cb8be73786134462daebfd2e96598f50515fda))
* **deps:** update module golang.org/x/crypto to v0.57.0 ([#9](https://github.com/Yornik/soiree/issues/9)) ([a2fe2b0](https://github.com/Yornik/soiree/commit/a2fe2b053d9b9556e9d3ad47612598f4409c758f))
* **e2e:** give each test server its own metrics port ([889281d](https://github.com/Yornik/soiree/commit/889281d7842e7bc46e5e104a3a3729b411ce0bfc))
* **web:** return focus on Escape, and let a rejected import be retried ([125a9ce](https://github.com/Yornik/soiree/commit/125a9ce4db59517146bc49ec39fe0d13fd6d091a))

## [0.2.1](https://github.com/Yornik/soiree/compare/v0.2.0...v0.2.1) (2026-09-17)


### Bug Fixes

* **ci:** test on the same Go toolchain the image ships ([e68e0ab](https://github.com/Yornik/soiree/commit/e68e0ab44d173597037589507592718b4729e3cb))

## [0.2.0](https://github.com/Yornik/soiree/compare/v0.1.0...v0.2.0) (2026-09-17)


### Features

* soiree, a self-hostable shared event budget and task planner ([12c1c5c](https://github.com/Yornik/soiree/commit/12c1c5c5eae8699b7167c29c3b7559dbbac4e997))


### Bug Fixes

* **test:** check Body.Close return values to satisfy errcheck ([7eedc10](https://github.com/Yornik/soiree/commit/7eedc10cac54d7a4935df99e08963e41fdbdf81b))
