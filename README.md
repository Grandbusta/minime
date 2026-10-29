# mini-me

A long-running Go agent that is **a person in its own right**: it has its own name and
life story, talks with people it knows, and remembers things about itself and about them.
Its mind is borrowed from how humans work: fast intuitive decisions (System 1), slow
deliberate ones (System 2), working and long-term memory. You, its creator, are someone it
knows. Design: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

**Status:**
- **Its life:** you write its name and life story in [self/life.yaml](self/life.yaml). It
  never contradicts that. Anything else about itself (favourite food, opinions…) it decides
  the first time it comes up, and remembers.
- **Two sides of memory:** facts about itself, and facts about each person it talks to.
  *I/my* and *you/your* are resolved to names before System 1 reads anything, so "your
  church" and "my church" are never confused.
- Every text message is read by **System 1**, a fast decision model. Today that's the
  [OpenAI Decisions API](https://developers.openai.com/api/docs/guides/decisions); Jev can be
  added later as another provider.
- It answers the questions in [decisions/incoming_message.yaml](decisions/incoming_message.yaml):
  - what you're doing: sharing something about yourself, asking about him, asking what he
    can do, asking about yourself, asking for help, asking to be reminded, or small talk
  - whether a time is mentioned
- A router acts on its confidence:
  - **Questions and chat** go to **System 2**, a reasoning model (OpenAI Responses API) that
    replies **as the agent itself**, with its life story in mind. Its instructions are in
    [prompts/reply.md](prompts/reply.md).
  - **Unclear messages** also go to System 2, which answers or asks what you meant.
  - **Every message gets a reply in Daniel's own words**, never a canned line. System 2
    saves lasting facts it hears about you, and facts it decides about
    itself, each tagged with whose life it is. Before System 2 replies, System 1 picks
    the facts that would help, from its own and yours only.
  - **Life areas:** every message and fact is filed by part of life (identity, family,
    home, work, health, religion, …), plus whether it's about the agent's or your own
    life, and whether it asks about one of you in general. Recall looks in the right areas
    first, skips memory for general questions, and hands over everything for "Tell me
    about yourself" or "What do you know about me?".
  - **Remind** sets a reminder (see below).
  - **Answers to the agent's own questions** go to System 2 as part of the conversation.
- Every message is stored in Postgres, along with the decisions it led to: answers,
  provider, version, cost and latency. A System 2 decision is linked to the System 1
  decision that handed it over.
- **Not built yet:** forgetting or correcting facts.

## Make your own

The agent in this repository is Daniel, but nothing about him is in the code. To make your
own person, clone it and change these files; then follow [Run locally](#run-locally).

| File | What you change |
|---|---|
| [self/life.yaml](self/life.yaml) | **Who they are:** `name`, `story` (their life, one fact per line, third person), `personality` (how they are with people), `routine` (how their days go), `makes` (what they make in their own day), `family` (who they're born knowing, including you). |
| [configs/minime.yaml](configs/minime.yaml) | **Where they live and what they can do:** `initiative.timezone` and `check_ins`, `daily_life` (when they plan and journal, a daily `budget_usd`, and `abilities`), the profile page and console addresses. |
| `.env` (from [.env.example](.env.example)) | **Secrets and accounts:** the Telegram bot token and your Telegram user ID, the Postgres URL, the OpenAI key and model. |

`makes` is what they create, in writing, for real. Each entry is a `kind` (snake_case; their
works are filed under it) and `about` (what it is and what a good one has):

```yaml
makes:
  - {kind: poem, about: "a short poem, usually free verse, about something from the day"}
  - {kind: painting_study, about: "a study for a painting: subject, composition, palette, light"}
```

Every agent can also write `notes` and short `writing`, and keeps a journal. Leave `makes`
out and they make only those.

`abilities` (in `daily_life`) is what they can do beyond writing from what they know:
`read_book` (public-domain books from Project Gutenberg, a passage a day) and `read_article`
(Wikipedia). The journal is always on.

You shouldn't need to edit the prompts ([prompts/](prompts)) or the questions System 1 asks
([decisions/](decisions)). They're written for any person and get filled in from
`life.yaml`. New abilities, like web search, are code: a package under
[internal/ability](internal/ability) and one line in `abilityMakers` in
[cmd/minime/main.go](cmd/minime/main.go).

## Run locally

You need:
- Go 1.27+
- A managed Postgres connection string
- A Telegram account
- An OpenAI API key with access to the Decisions API

1. **Give it a life.** Write its name and life story in [self/life.yaml](self/life.yaml),
   one fact per line, in the third person with its name ("Tobi is 29 years old."). It won't
   start until you do.

2. **Create a bot.** Message [@BotFather](https://t.me/BotFather), send `/newbot`, and copy
   the token it gives you.

3. **Configure.** From the repository root:

   ```bash
   cp .env.example .env
   ```

   Then set these in `.env`:
   - `TELEGRAM_BOT_TOKEN`: the token from step 2.
   - `DATABASE_URL`: your managed Postgres, password included, e.g.
     `postgres://user:password@host:5432/minime?sslmode=require`.
   - `OPENAI_API_KEY`: used by System 1 and System 2.
   - `OPENAI_MODEL`: the model System 2 reasons with, e.g. `gpt-6-luna`.

   Leave `TELEGRAM_OWNER_ID` empty for now. If a value contains shell characters like `$`,
   `&` or `#`, wrap it in single quotes, because the shell reads `.env` in the next step.

4. **Start it.** Run this from the repository root, because the config is read from
   `configs/minime.yaml`:

   ```bash
   set -a; . ./.env; set +a; go run ./cmd/minime
   ```

   On first start it creates its tables. You'll see `mini-me awake` and a warning that no
   owner is configured.

5. **Find your Telegram user ID.** Message your bot anything. The bot ignores it and logs a
   line like this:

   ```
   {"level":"INFO","msg":"ignoring message from non-owner","user_id":123456789,...}
   ```

   Stop the agent with `Ctrl+C`, put that number in `TELEGRAM_OWNER_ID` in `.env`, and start
   it again with the command from step 4.

6. **Talk to it.** Message the bot again, like you'd talk to a person: "Hi, I'm Sam. Where
   did you grow up?" It answers as itself, from its life story. Tell it about yourself and
   it remembers. Ask it something its story doesn't cover ("What's your favourite food?")
   and it decides, then sticks to it. Messages from anyone else are ignored.

### Switching the decision model

System 1's provider is chosen in [configs/minime.yaml](configs/minime.yaml):

```yaml
decision:
  provider: openai   # openai | typesafe (Jev, not implemented yet)
  model: gpt-6-luna
  api_key: ${OPENAI_API_KEY}
```

System 2 is chosen the same way, under `reasoning:`.

### Changing how it replies

Edit [prompts/reply.md](prompts/reply.md) and restart. The decision log records each
version as `reply@<hash>`.

It never uses em dashes (—). The prompts ask for none, and anything System 2 writes anyway
(replies, what it makes and remembers, its profile page) has them rewritten as commas or
spaces before it is used ([internal/providers/system2/dashes.go](internal/providers/system2/dashes.go)).

### Changing what it asks

Edit [decisions/incoming_message.yaml](decisions/incoming_message.yaml): its questions,
choices and thresholds. Bump `version`, then restart. Every decision records which version
decided, so you can compare versions in the decision log.

### Reminders, promises and check-ins

The agent can message you first. Ask it to remind you of something ("remind me tomorrow
at 9 to call the bank") and it will message you at that time, in its own words. Times are
in its time zone, `initiative.timezone` in [configs/minime.yaml](configs/minime.yaml)
(Africa/Lagos). If you don't say when, it asks.

At the `check_ins` times (11:00 and 18:00), it may also check in on you about something
you told it in the last 30 days that is still going on, like an interview or a trip.
System 1 picks what's worth asking about. It checks in at most once a day, only after the
conversation has been quiet for `quiet` (4h), and never asks about the same thing twice.
Set `check_ins: []` to turn this off. Reminders and check-ins show up in the decision log
as `remind` and `check_in`.

It also keeps its promises. Ask it to make you something ("can you write me a verse about
Sunday mornings tonight?") and if it agrees, it promises a time. Up to 30 minutes before
then it makes it, the way it makes its own things (with an ability if it needs one, like
reading up on a topic first), following anything you added after it promised ("make it
funny"), and at that time it sends it to you with a few words of its own. It never promises what it
can't do in writing, keeps at most three promises open with a person, and promises nothing
to strangers. A promise isn't put off by a conversation or the day's budget; if it can't be
kept within 24 hours, it is missed. In the decision log, making it is `task` and sending it
is `deliver`; long messages are split for Telegram.

### Its own day

The agent lives a day of its own. At `plan_at` it plans a few things to do, mostly
continuing its projects (for Daniel: a song, a beat, something he's learning), steered by
the optional `routine` lines in [self/life.yaml](self/life.yaml). Through the day it does
them for real, in writing: the things its `makes` lists (Daniel's are lyrics and beat
ideas), notes and short pieces. With its abilities it also reads, for real: public-domain
books from Project Gutenberg, a passage per sitting over many days, and Wikipedia articles
on what it's learning, and writes reading notes. It fetches only from those two sites;
`daily_life.abilities` lists the ones switched on. At `journal_at` (22:00) it writes a
journal entry. Reading and the journal are *abilities* ([internal/ability](internal/ability)):
the activity steps are shared, and an ability only gets what the activity works from, so a
new one (web search, say) is a small package and a line in `cmd/minime`.
What it makes is kept; what it did becomes facts about itself, so it can tell you about it.
If you're chatting when something is due, it waits 30 minutes; past `budget_usd` a day,
the rest waits for tomorrow. Settings are under `daily_life:` in
[configs/minime.yaml](configs/minime.yaml), and the prompts are
[prompts/plan.md](prompts/plan.md) and [prompts/activity.md](prompts/activity.md).

See what it planned and made, today or on another day:

```bash
set -a; . ./.env; set +a; go run ./cmd/minime day
set -a; . ./.env; set +a; go run ./cmd/minime day -date 2026-10-29
```

### Its profile page

While it runs, the agent serves its own public profile page at
[http://127.0.0.1:8080/](http://127.0.0.1:8080/), the way a person keeps a personal website.
It writes the page itself, in its own voice, from its life story and the facts about itself
that System 1 decides are fine to share
([decisions/public_profile.yaml](decisions/public_profile.yaml)). Facts about people never
go on it. Every day at 00:00 UTC it checks for new facts, and it rewrites the page only when
they change. It also writes the page on start if there isn't one yet.
The page is read-only, with no way to contact it yet, and it says plainly that the agent is an AI.

To update it now instead of waiting for 00:00 UTC (it works whether or not the agent is running):

```bash
set -a; . ./.env; set +a; go run ./cmd/minime profile
```

It rewrites the page only if there's something new to share; add `-force` to rewrite it anyway.

Change where it's served and when it updates (`update_at`), or turn it off (`addr: ""`), under `portfolio:` in
[configs/minime.yaml](configs/minime.yaml). Change how it writes the page in
[prompts/profile.md](prompts/profile.md).

### Its mind console

While it runs, open [http://127.0.0.1:8090/](http://127.0.0.1:8090/): a private, read-only
console that shows everything about the agent, live.
- **Mind:** its brain, the parts of its mind lighting up as each decision flows through
  them; vitals for today; a live activity feed; today's plan; and cost per day.
- **Activity:** the decision log. Click any decision to see its whole chain: the message,
  System 1's answers with their confidence, recall, System 2's reply, and the outcome.
- **Memory:** every fact (about itself and about people, with life areas and whether it's
  public) and everyone it knows.
- **Day:** what it planned on any day, what came of it, what it made, and its projects.
- **Works:** everything it made, to read in full.
- **Conversations:** your chats with it, including what it said first.

It only listens on localhost. To reach it from elsewhere (e.g. on the VPS), set `console.addr`
in [configs/minime.yaml](configs/minime.yaml) and `CONSOLE_TOKEN` in `.env` (16+
characters, e.g. `openssl rand -hex 24`), then open it once as
`http://<host>:8090/?token=<CONSOLE_TOKEN>`; the browser remembers it.

### See what it decided

```bash
set -a; . ./.env; set +a; go run ./cmd/minime decisions -n 20
```

### Logs

Logs are JSON on stdout. For more detail, set `log.level: debug` in
[configs/minime.yaml](configs/minime.yaml).

## Develop

```bash
go test ./...
```

The store test only runs when `MINIME_TEST_DATABASE_URL` points at a disposable database. It
writes test rows, so never point it at the production database.

## Deploy with Docker (later, on the VPS)

The agent runs in Docker and connects to the same managed Postgres through the
`DATABASE_URL` in `.env`.

```bash
docker compose -f deploy/docker-compose.yml --env-file .env up -d --build
docker compose -f deploy/docker-compose.yml logs -f minime
docker compose -f deploy/docker-compose.yml exec minime /minime decisions
```

Only run one copy of the agent per bot token, local or Docker. Telegram allows a single
poller, so a second copy gets `409 Conflict` errors and the two keep interrupting each other.
