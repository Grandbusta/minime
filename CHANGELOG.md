# Changelog

## v0.3.1: doctor, backup and restore

- **`minime doctor`** checks everything a person needs to run, one line each, and says
  what's wrong:
  - the database;
  - that `MINIME_KEY` opens their secrets;
  - the config with its prompts and question sets;
  - that their OpenAI key reaches each model;
  - the bot token, and that each owner has pressed Start;
  - each tool server.

  It changes nothing but bringing the database up to date. In Docker:
  `docker exec <name> /minime doctor`.
- **`minime backup [file]`** writes everything about them to one file, readable only by
  you. It's taken at one moment, so they keep running while it's made. Their secrets are
  in it sealed: keep `MINIME_KEY` safe too, apart from the backup.
- **`minime restore <file>`** brings them back into a new, empty database. It never writes
  over anyone. A backup from an older minime is brought up to date once it's loaded.

## v0.3.0: a person is their own database

A person now lives entirely in their own database. Outside it, all they need is two
lines: `DATABASE_URL` says where they are, and `MINIME_KEY` unlocks their keys. minime
is also one binary now, with releases you can download.

**A person is their own database**
- Their life, how they live, your judgment, their skills and their keys are all kept in
  their database. Two people never share a key, a database or an OpenAI account.
- Each part of them is versioned: `minime self edit life` opens it in your editor and
  refuses an edit that doesn't read. `minime self history` and `minime self rollback`
  take you back. `minime self export` and `minime self import` move them in and out as files.
- Keys and tokens are sealed with `MINIME_KEY` (AES-GCM) and are never stored as plain
  text. `minime secret list | set | unset | rekey` manages them. A value already set in
  the environment still wins.
- Skills live in the database, every file of them, under `minime skill list | add | remove`.
  Images and other files that aren't text are described rather than read as words. A
  skill can be up to 10 MB, and its scripts are never run.
- `minime new` writes a new person straight into their database. It moves an existing
  `.env` aside to `.env.<name>` and never takes over a database that belongs to someone else.

**One binary**
- The config, prompts, question sets and the five lives are built in, so the binary
  alone runs a person. A file on disk at the same path still wins, so you can change
  them without a rebuild.
- Download it from the release (Linux, macOS and Windows), or install it with
  `go install github.com/Grandbusta/minime/cmd/minime@latest`.
- The image is `ghcr.io/grandbusta/minime:v0.3.0`, for amd64 and arm64. `:latest` follows
  the newest published release.

**Upgrading**
- Someone in `self/` moves into their database once, on their first start. If `.env` has
  no `MINIME_KEY`, one is made and written there.
- **Back up `MINIME_KEY`.** Without it, their keys can't be opened again. Keep it
  somewhere safe and apart from the database.
- After the move, `self/` is no longer read. Keep it as a backup or delete it.

## v0.2.0: launch

The first release we're announcing: create an AI person, talk with them on Telegram, and
watch them live their own life. They make real things, use the tools you connect, notice
what changes, and learn how you decide, asking you before anything that matters.

**A person of their own**
- **Five lives, ready to start:** Daniel, Mei, Tomás, Priya and Elsa. `minime new` picks
  one, sets up `.env`, finds your Telegram ID, can start a Postgres in Docker, and starts
  them. Your own person stays private in `self/`.
- **Talking:** a fast decision model reads each message (System 1) and a reasoning model
  answers in their own words (System 2). Memory of themselves and of each person, by
  circle; corrections and forgetting from chat; names used the way people do when texting.
  A reply never waits on their day: talking and living run side by side.
- **Speaking first:** reminders, check-ins, and promises made ahead and sent on time. Ask
  for something promised sooner or later ("can you send it now?") and they move it; they
  never say something is coming at a time it isn't.
- **Their own day:** a morning plan, real work (making things, reading books and web
  pages), a journal at night, and a `pace` to live faster than a person.
- **Growth:** dreams, goals (including ones you ask them to take on), skills scored from
  their work, lessons, learning plans, and a weekly review. A learning stage moves on only
  when an independent check agrees the work meets it.

**What they make, as real files**
- They know their own work when they talk about it, and never make up its details.
- Anything they made can be sent as a **PDF** or a **Word** document; a table as a
  **spreadsheet** or **CSV**; dated events as a **calendar** file; music as **MIDI**,
  written note by note; code as a **zip**. Ask for something they already made and it
  comes right away with their reply; something new is promised and sent at its time.
- Every life can make a `table` and a `schedule`; a life's own crafts say what else
  their work carries (`data: score` for music, `data: files` for code).
- File formats are plug-ins: a new one is a type plus a line in main.

**Tools you connect (MCP)**
- **Read:** tools from any MCP server you list, local or remote, become things they can
  use in their day and in promises, up to three in a row (search, then read). A local
  server never sees your keys.
- **Act:** a tool that changes things (save a file, send an email, book) is only
  proposed. You see exactly what it would do on Telegram and approve it; destructive
  tools always ask, and so does anything prompted by what they read rather than your own
  words. Email and posts ask before every send; spending stays off until you allow it
  in your config.
- **Watch:** a read tool can be looked at on a schedule, like an inbox or a folder. What's
  new is noticed, with what changed; they may remember it, tell you, or propose an
  action.

**Skills**
- Give them know-how in the open Agent Skills format (a folder with a `SKILL.md`, the
  same as Claude's). The skill that fits what they're doing is used, by meaning; skills
  grant no permission, and their scripts never run.

**Learning your judgment, and staying in charge**
- Every action is `auto`, `ask` or `forbid`, with approvals on Telegram; silence is no.
- Principles, priorities, boundaries and past decisions; calls with stakes, predictions
  you answer with a tap, outcomes judged apart from luck, similar decisions found by
  meaning, and your taste.

**Seeing inside and running it**
- A private mind console (with their tools, actions and skills) and a public profile
  page they write themselves.
- One binary, Postgres and OpenAI; Docker without compose. `AGENTS.md` explains how the
  code is written and the safety rules every change keeps.

## v0.1.0: early version

Tagged before launch and not announced: the first version anyone could run, with the
five lives, talking on Telegram, speaking first, their own day, growth, autonomy,
learning your judgment, the mind console and the profile page.
