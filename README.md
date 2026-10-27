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
  - **Remind** is recognized, but not acted on yet.
  - **Answers to the agent's own questions** go to System 2 as part of the conversation.
- Every message is stored in Postgres, along with the decisions it led to: answers,
  provider, version, cost and latency. A System 2 decision is linked to the System 1
  decision that handed it over.
- **Not built yet:** reminders, and forgetting or correcting facts.

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

### Changing what it asks

Edit [decisions/incoming_message.yaml](decisions/incoming_message.yaml): its questions,
choices and thresholds. Bump `version`, then restart. Every decision records which version
decided, so you can compare versions in the decision log.

### Reminders and check-ins

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
