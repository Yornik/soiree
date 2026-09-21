# Changelog

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
