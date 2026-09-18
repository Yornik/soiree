# Changelog

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
