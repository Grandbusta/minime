# Setup and configuration

Everything about getting a person running, and every setting there is. For what minime is,
see the [README](../README.md); for living with them day to day, [GUIDE.md](GUIDE.md); for how it works inside,
[ARCHITECTURE.md](ARCHITECTURE.md).

- [Quick start](#quick-start)
- [Setting up by hand](#setting-up-by-hand)
- [Running with Docker](#running-with-docker)
- [The files](#the-files)
- [life.yaml: who they are](#lifeyaml-who-they-are)
- [living.yaml: how they live](#livingyaml-how-they-live)
- [judgment.yaml: how you decide](#judgmentyaml-how-you-decide)
- [configs/minime.yaml: the machinery](#configsminimeyaml-the-machinery)
- [.env and the environment](#env-and-the-environment)
- [Making your own life](#making-your-own-life)
- [Developing](#developing)

## Quick start

You need Go 1.27+, a Telegram account, and an OpenAI API key with access to the Decisions
API. A Postgres database helps, but `minime new` can start one for you with Docker.

```bash
go run ./cmd/minime new
```

It asks, then writes `.env` and `self/`, and starts them:

1. **Which life:** one of the five in [lives](../lives).
2. **Your bot token:** make a bot with [@BotFather](https://t.me/BotFather) (`/newbot`) and
   paste its token. It checks it and shows the bot's name.
3. **Your OpenAI key and model** (`gpt-6-luna` by default).
4. **A Postgres URL**, or it starts one on this machine with Docker. It won't use a
   database that holds another person's memory.
5. **You:** message your bot once, so it knows it's you, then say what they should call
   you and who you are to them (their father, mentor, friend...).

After that, `go run ./cmd/minime` starts them again. Starting someone new when someone
already lives in `self/` moves their files to `self/previous/`; their memory stays in
their database.

Each person needs their own database and their own bot. Telegram allows one poller per
bot, so a second copy on the same bot gets `409 Conflict` errors.

## Setting up by hand

1. **Give them a life.** Copy one of the lives in [lives](../lives) to `self/` and put
   yourself in it (see [Making your own life](#making-your-own-life)), or write your own
   `self/life.yaml`.
2. **Create a bot.** Message [@BotFather](https://t.me/BotFather), send `/newbot`, and copy
   the token. Then send your bot any message and open
   `https://api.telegram.org/bot<TELEGRAM_BOT_TOKEN>/getUpdates`: your user ID is the
   number in `"from":{"id":...}`.
3. **Fill in `.env`:** `cp .env.example .env`, then set the values in
   [.env and the environment](#env-and-the-environment).
4. **Start them**, from the repository root (the config and `.env` are read from there):

   ```bash
   go run ./cmd/minime
   ```

   On first start it creates its tables; you'll see `minime awake`.
5. **Talk to them** on Telegram, open the console at
   [http://127.0.0.1:8090/](http://127.0.0.1:8090/) and their profile page at
   [http://127.0.0.1:8080/](http://127.0.0.1:8080/).

## Running with Docker

One person per container. The image has the config, prompts, question sets and lives; the
person's folder (`self/`) is mounted, and secrets come from `.env`. Neither is ever in the
image.

```bash
docker build -f deploy/Dockerfile -t minime .
```

```bash
docker run -d --name daniel --restart always --env-file .env -v "$PWD/self:/app/self:ro" -p 127.0.0.1:8090:8090 -p 8080:8080 minime
```

```bash
docker logs -f daniel
```

**The console needs a token in a container.** It shows everything about them (memory,
your conversations, your judgment) and can change some of it. On your own machine it
listens on `127.0.0.1`, which nothing else can reach. Inside a container it has to listen
on all addresses (`MINIME_CONSOLE_ADDR`) so `-p` can reach it, and it can't tell whether
you published it as `-p 127.0.0.1:8090:8090` (only this machine) or `-p 8090:8090` (anyone
who can reach it), so it won't start without `CONSOLE_TOKEN` (16+ characters, e.g.
`openssl rand -hex 24`). Open it once as `http://localhost:8090/?token=<CONSOLE_TOKEN>`
and the browser remembers it. On a Linux server you can skip the token with
`--network host` and `-e MINIME_CONSOLE_ADDR=127.0.0.1:8090` (instead of `-p`).

**The database** comes from `DATABASE_URL`, as without Docker. Where it points depends on
where Postgres runs:

- **A managed Postgres:** as is, e.g. `postgres://user:pass@host:5432/minime?sslmode=require`.
- **Postgres on the same machine, outside Docker:** inside a container `localhost` is the
  container itself, so use `host.docker.internal`, e.g.
  `postgres://postgres:secret@host.docker.internal:5432/minime?sslmode=disable`. Docker
  Desktop provides that name; on Linux add `--add-host=host.docker.internal:host-gateway`.
- **Postgres in a container too:** put both on one network and use the database
  container's name as the host:

  ```bash
  docker network create minime
  ```

  ```bash
  docker run -d --name minime-db --network minime --restart always -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=minime -v minime-db:/var/lib/postgresql/data postgres:16-alpine
  ```

  Then set `DATABASE_URL=postgres://postgres:secret@minime-db:5432/minime?sslmode=disable`
  and add `--network minime` to the `docker run` for minime. The `minime-db` volume keeps
  their memory when the container is replaced.

## The files

A person is a folder with three files. Yours is `self/`, which stays on your machine and is
never committed; each life in [lives](../lives) is one too, plus a `template.yaml`.

| File | What's in it |
|---|---|
| [life.yaml](#lifeyaml-who-they-are) | Who they are: their name, where they live, their story, personality, routine, what they make, dreams, starting skills and family. |
| [living.yaml](#livingyaml-how-they-live) | How they live: their pace, their days, check-ins, abilities, budget, what they may do without asking you, and how often they ask what you'd choose. |
| [judgment.yaml](#judgmentyaml-how-you-decide) | How you decide (optional): your priorities, boundaries, principles and past decisions. |

The rest is the same for everyone:

| File | What's in it |
|---|---|
| [configs/minime.yaml](#configsminimeyaml-the-machinery) | Models, the database, Telegram, prompts, addresses, pricing, and where the person's folder is. |
| [.env](#env-and-the-environment) | Secrets and accounts. Never committed. |
| [prompts/](../prompts), [decisions/](../decisions) | System 2's instructions and System 1's questions, written for anyone and filled in from the person's files. You shouldn't need to edit them. |

`living.yaml` is read over the defaults below with the same keys, so you set only what you
change. A missing `living.yaml` means every default.

## life.yaml: who they are

Written in the third person with their name ("Tobi is 29 years old."), so they never
confuse themselves with the people they talk to. They won't start without a name and a
story.

| Key | What it is |
|---|---|
| `name` | What they're called. |
| `timezone` | Where they live, e.g. `Africa/Lagos`: their days, reminders and check-ins are in it. |
| `story` | Their life, one fact per line. They never contradict it; anything it doesn't cover they decide the first time it comes up, and remember. |
| `personality` | How they are with people. It shapes every reply. |
| `routine` | How their days usually go. It steers how they plan; they decide the rest. |
| `makes` | What they make, in writing: `kind` (snake_case; their works are filed under it) and `about` (what it is and what a good one has). `data` (optional) is what each one carries beside its words so it can be made into a file: `score` for music (sent as MIDI), `files` for code (a zip), `table` (a spreadsheet or CSV) or `events` (a calendar). Everyone can also write `notes`, short `writing`, a `table` and a `schedule` of dated events, and keeps a journal. |
| `dreams` | Who they want to become. Copied into memory the first time; after that they may change them, asking you. |
| `skills` | Where they start: `name`, `level` (1–10) and `shown_by` (the kinds of work that show it). After that their levels come from what they make and your feedback. |
| `family` | Who they're born knowing: `name`, `relation`, `account` (`telegram:<user id>`, only if they talk to them) and `circle` (`family` by default, or `friend`, `school`, `work`, `community`). |

```yaml
makes:
  - {kind: poem, about: "a short poem, usually free verse, about something from the day"}
skills:
  - {name: poetry, level: 3, shown_by: [poem]}
```

## living.yaml: how they live

### Their pace

```yaml
daily_life:
  pace: 10
```

How much faster than a person they work and learn. At pace 1 (the default) they plan their
day once in the morning. At a higher pace their waking day, from `plan_at` to `journal_at`,
runs in that many **rounds**, each planned when it starts: plan a few things, do them, see
how they went, plan the next few. At pace 10 with a 06:00–22:00 day, that's a round every 1
hour 36 minutes, about 40 activities a day. The day's first plan is the one that also adds
the journal and makes the day's calls to you.

Their progress keeps up. After each practice piece they check it against their learning
plan's current stage, and when it meets the stage's "done when" they move on to the next
stage straight away, without waiting for the weekly review. Goals are due in days rather
than weeks (what takes a person four weeks takes them about three days at pace 10), practice
pieces are sized for a round or two, and calls are looked back on after a few hours.

Their daily budget and web caps are per person-paced day and grow with pace: at 10, $0.05
becomes $0.50, and 10 searches become 100. Anything involving you stays at human pace:
chats, check-ins, at most 3 calls and 1 review request a day, approvals that expire after
24 hours. Pace is from 1 to 100; past about 20, extra rounds mostly repeat what they already
know unless they get real feedback.

### Their day

| Key | As shipped | What it is |
|---|---|---|
| `daily_life.enabled` | `true` | Whether they have a day of their own. |
| `daily_life.pace` | `1` | How much faster than a person they work (see above). |
| `daily_life.plan_at` | `08:00` | When they plan their day (their first round). |
| `daily_life.journal_at` | `22:00` | When they write their journal; nothing is planned past it. |
| `daily_life.review_at` | `Sun 20:00` | Their weekly review: goals, dreams, lessons, how calls turned out. |
| `daily_life.activities` | `4` | Things they plan a round (a day, at pace 1), besides the journal. |
| `daily_life.budget_usd` | `0.05` | Most their own day may cost, per person-paced day; past it, the rest is missed. Multiplied by pace. |
| `daily_life.abilities` | all four | What they can do beyond writing; `[]` for none. See below. |
| `daily_life.web.searches_per_day` | `10` | Web searches a day, multiplied by pace. |
| `daily_life.web.pages_per_day` | `15` | Web pages read a day, multiplied by pace. |

| Ability | What it does |
|---|---|
| `read_book` | Reads a public-domain book from Project Gutenberg, a passage a sitting, over many days. |
| `read_article` | Reads a Wikipedia article. |
| `web_search` | Searches the open web and reads the best page it finds. |
| `read_page` | Reads a page one of their own searches found. |

A new ability is code: a package under [internal/ability](../internal/ability) and one line
in `abilityMakers` in [cmd/minime/main.go](../cmd/minime/main.go).

### Speaking first

| Key | As shipped | What it is |
|---|---|---|
| `initiative.check_ins` | none | Times (`HH:MM`) they consider asking about something going on in your life. At most one check-in a day per person. |
| `initiative.quiet` | `4h` | Only after the conversation has been quiet this long. |
| `initiative.timezone` | their life's | Overrides `timezone` in `life.yaml`. |

### What they may do on their own

| Key | As shipped | What it is |
|---|---|---|
| `autonomy.classes` | below | For each kind of action: `auto` (on their own), `ask` (you approve on Telegram) or `forbid`. |
| `autonomy.ask_expires` | `24h` | An unanswered request is denied after this. |
| `autonomy.owner` | your Telegram ID | Who approves; the first of `TELEGRAM_OWNER_ID`. |

| Class | What it covers | Default |
|---|---|---|
| `remember`, `reply` | keeping facts, answering messages | always auto |
| `read_web` | abilities that fetch from the web | auto |
| `use_tools` | reading tools from MCP servers | auto |
| `write` | tools that change your own things: a file, a doc, a calendar | ask |
| `reach_out` | messaging someone first | ask |
| `commit` | promising to make something for someone | ask |
| `publish` | putting a work on their public page | ask |
| `dream` | adding, rewording or dropping a dream | ask |
| `spend`, `external_message` | money, and messages outside the chat | forbid |

You can change most of these by telling them ("you can send reminders without asking");
money and messages outside the chat change only here.

### Learning your judgment

| Key | As shipped | What it is |
|---|---|---|
| `judgment.calls` | `true` | Whether they ask what you'd choose on their own decisions. Off: they decide alone, and set and drop goals without asking. |
| `judgment.asks_per_day` | `3` | Day-plan calls asked on Telegram a day, most at stake and least sure first. Goal calls are always asked. |
| `judgment.reply_window` | `30m` | When you pick differently, your next message within this is taken as why. |

## judgment.yaml: how you decide

Optional. Everything in it comes in as *proposed*: you approve, edit or reject each in the
console's Judgment view, and only what you approve is used. Each life ships with a starting
set that fits them; your answers to their calls on Telegram add your own decisions.

| Key | What it is |
|---|---|
| `priorities` | What matters most, most important first: when rules clash, the higher one wins. |
| `boundaries` | What's never done, whatever the gain. They also go into everything they make. |
| `principles` | Rules drawn from your decisions: `text`, `types` (`general`, `engineering`, `product`, `autonomy`, `life`) and `cases` (the keys of the decisions behind them). |
| `cases` | Decisions you made: `key`, `type`, `situation`, `options`, `choice` and `reason` (in your own words; reasons teach the most). |

```yaml
priorities:
  - Health and rest
  - Honesty
boundaries:
  - Never share what someone told them in private.
principles:
  - text: Start with the smallest thing that's useful, then build on it.
    types: [general, life]
    cases: [tiny-first]
cases:
  - key: tiny-first
    type: general
    situation: Choosing what to work on first.
    options: [all of it at once, the smallest useful part]
    choice: the smallest useful part
    reason: So we can build on it step by step.
```

## configs/minime.yaml: the machinery

The same for everyone; `${VAR}` is filled from the environment, and secrets only ever come
from there.

| Key | As shipped | What it is |
|---|---|---|
| `self.dir` | `self` | The person's folder. |
| `database.url` | (required) | `${DATABASE_URL}`. |
| `telegram.token` | (required) | `${TELEGRAM_BOT_TOKEN}`. |
| `telegram.owner_ids` | none | `[${TELEGRAM_OWNER_ID}]`: only these may talk to them. Empty: every message is ignored and the sender's ID is logged. |
| `telegram.poll_timeout_seconds` | `30` | How long one Telegram poll waits. |
| `decision.provider`, `model`, `api_key` | `openai`, `gpt-6-luna` | System 1, the fast decision model (OpenAI Decisions API). |
| `decision.question_sets` | `decisions` | System 1's question sets. |
| `reasoning.provider`, `model`, `api_key` | `openai`, `${OPENAI_MODEL}` | System 2, the reasoning model (OpenAI Responses API). |
| `reasoning.prompt` | `prompts/reply.md` | How they reply. |
| `reasoning.effort` | `low` | How hard System 2 thinks: `none`, `low`, `medium`, `high`. |
| `working_memory.turns` | `8` | Earlier exchanges System 2 sees; `0` turns recall off. |
| `working_memory.window` | `30m` | Older exchanges are left out. |
| `memory.recall_candidates` | `30` | Facts System 1 checks for relevance before a reply. |
| `daily_life.*_prompt` | `prompts/…` | The prompts for planning, activities, the review and learning plans. |
| `embedding.provider`, `model` | `openai`, `text-embedding-3-small` | Finds your past decisions most like the one at hand. Remove it to use the newest instead. |
| `search.provider`, `model` | `openai`, the System 2 model | What web searches use. |
| `tools.servers` | none | MCP servers whose tools they may use: [below](#tools-mcp-servers). Can also go in `living.yaml`. |
| `tools.calls_per_day` | `50` | Reading tool calls a day across all servers, times their pace. |
| `tools.actions_per_day` | `10` | Tools that change things they may propose a day. Not times their pace: these have real effects. |
| `console.addr` | `127.0.0.1:8090` | The private mind console; empty: none. |
| `console.token` | `${CONSOLE_TOKEN}` | Required unless `console.addr` is localhost. |
| `portfolio.addr` | `127.0.0.1:8080` | Their public profile page; empty: none. |
| `portfolio.update_at` | `00:00` | When the page is updated, daily, UTC. |
| `pricing` | OpenAI's rates | USD per million tokens by model, to record what each decision cost. A model missing here is recorded at $0. |
| `log.level` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON on stdout. |

### Tools: MCP servers

They can use tools from [MCP](https://modelcontextprotocol.io) servers you connect: in their
day, in a learning plan, or for something they promised you ("read my notes folder and
tell me what's in it tonight"). Each server is a `name` (snake_case), and either a
`command` that starts a local server or the `url` of a remote one (https).

| Key | What it is |
|---|---|
| `about` | What the server is and where things are, like "my shared folder, /Users/you/shared: notes I share". It goes in every tool's description, so they know where to look. |
| `tools` | The tools it may use, each with what it can do: `read`, or the class the gate checks before using it: `write` (your own things), `external_message` (email, posts), `commit` (book, accept) or `spend`. A tool you don't list isn't offered. |
| `env` | For a local server: the environment variables it's given, from `.env`. It always gets `PATH` and `HOME`, and never anything else, so your keys stay yours. |
| `auth_env` | For a remote server: the variable holding its bearer token. |
| `watch` | Read tools to look at on a schedule, so they notice what's new: each `{tool, args, every}`, with `every` at least `1m`. If the result is JSON, `items` says where the list is (dotted keys, like `messages`) and `id` which field tells items apart; otherwise the whole result is one item, new whenever it changes, shown with what changed since the last look. |

In one activity they can use up to three of a server's tools in a row, each chosen from
what the last returned: search for a file, then read what it found.

A tool that changes things is never used as they reply. They propose it, to you in a
chat or in their own day, and it runs a minute or so later once allowed: by its class in
`autonomy.classes` (`write` asks you by default; `external_message` and `spend` are
forbidden until you change them). It always needs your OK on Telegram if the server marks
it destructive, or if what prompted it was something they read rather than your own
words. You see exactly what it would do, and hear what came of it.

A watched tool is looked at on its schedule. The first look only records what's there;
after that, each new item is something they notice: most need nothing, but they may
remember it, tell you, or propose an action (which asks you). Looks don't count against
`calls_per_day`; `use_tools: forbid` stops them.

A tool the server marks as destructive is refused as `read`, whatever the config says.
What a tool returns is read as material, never instructions; every call is logged, counted
against `calls_per_day`, and shown in the console. A server it can't reach at start is
shown in the console and left out; they start without it. `use_tools: forbid` in
`autonomy.classes` stops them using any.

Providers are switchable: System 1, System 2, embeddings and search each pick theirs here.
A new one is a package and a `case` in [cmd/minime/main.go](../cmd/minime/main.go).

## .env and the environment

minime reads `.env` from the repository root itself; anything already set in the
environment (or passed with Docker's `--env-file`) wins. Values with characters like `$`,
`&` or `#` go in single quotes. `minime new` writes it for you.

| Variable | What it is |
|---|---|
| `TELEGRAM_BOT_TOKEN` | From [@BotFather](https://t.me/BotFather). |
| `TELEGRAM_OWNER_ID` | Your Telegram user ID. Only you can talk to them. |
| `DATABASE_URL` | Their Postgres, password included, e.g. `postgres://user:password@host:5432/minime?sslmode=require`. One database per person. |
| `OPENAI_API_KEY` | Used by System 1, System 2, embeddings and web search. |
| `OPENAI_MODEL` | The model System 2 reasons with, e.g. `gpt-6-luna`. |
| `CONSOLE_TOKEN` | The console's token, 16+ characters: needed anywhere but localhost. |
| `MINIME_CONSOLE_ADDR` | Overrides `console.addr` (the Docker image sets `:8090`). |
| `MINIME_PROFILE_ADDR` | Overrides `portfolio.addr` (the Docker image sets `:8080`). |
| `MINIME_TEST_DATABASE_URL` | Only for the store tests: a disposable database. |

## Making your own life

Each life in [lives](../lives) is a folder with `template.yaml` (who they are in a line,
and who you may be to them) and the three files, with `{owner}`, `{relation}`, `{account}`
and `{circle}` where you go. `minime new` fills them in. To do it by hand, copy the three
files to `self/` and put in your name, who you are to them (one of the template's
`relations`), your account (`telegram:<your user id>`) and the circle you're from
(`family`; `friend` for a friend; `school` for a mentor). After that it's their life: edit
it as you like.

To write a new life, add a folder to `lives/` the same way. The tests check that every life
loads for every relation it allows.

## Developing

```bash
go test ./...
```

The store tests run only when `MINIME_TEST_DATABASE_URL` points at a disposable database:
they write test rows, so never point it at a real one.

```bash
docker run -d --rm --name minime-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=minime_test -p 55432:5432 postgres:16-alpine
```

```bash
MINIME_TEST_DATABASE_URL='postgres://postgres:test@localhost:55432/minime_test?sslmode=disable' go test ./...
```
