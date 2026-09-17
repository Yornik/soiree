## What and why

<!--
What changes, and what problem it solves. If there is an issue, link it.
-->

## Notes for the reviewer

<!--
Optional. Anything you want looked at closely, a decision you were unsure
about, or how you tested it.
-->

---

Three things that are easy to get wrong here, all covered in
[CONTRIBUTING.md](https://github.com/Yornik/soiree/blob/main/CONTRIBUTING.md):

- The commit subject (or this pull request's title, if it is squashed) is read
  by release-please. `feat:` cuts a release; `docs:` and `chore:` do not.
- Migrations are append-only. Add a new `NNNN_name.sql`; never edit one that
  has been applied.
- No third-party origins in the frontend — no CDN font, script, icon set or
  beacon.
