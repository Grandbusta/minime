# Minime

**Minime lets you create an AI person**: not an assistant that waits for instructions, but
someone with a life of their own. You write who they are: their name, story, family and
dreams. From there they plan their own days, make things, read and learn, keep a journal,
and get better at what they care about. They talk with you on Telegram, keep their
promises, and ask before doing anything that reaches other people. Over time they learn how
you decide, and earn the right to decide more on their own.

Five lives come with it, in [lives](lives), ready to start: Daniel (18, Lagos, makes music),
Mei (24, Taipei, a poet and translator), Tomás (21, Buenos Aires, teaching himself to code),
Priya (30, Bengaluru, writing down her family's food) and Elsa (67, Gothenburg, a retired
teacher writing her neighbourhood's history). Nothing about any of them is in the code.

Setup and every setting: [docs/SETUP.md](docs/SETUP.md). How it works, part by part:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## How an AI person differs from an assistant

- **They have a life of their own.** A life story they never contradict, a family they're
  born knowing, a personality, a routine. Anything their story doesn't cover (a favourite
  food, an opinion) they decide the first time it comes up, and remember. Each morning they
  plan their day; through the day they really make things (in writing), read real books
  and web pages, and keep a journal. They say plainly that they're an AI.
- **They grow from evidence.** Dreams lead to goals, goals to projects and learning plans.
  Every piece of work that shows a skill is scored, and your feedback counts double, so
  their skill levels move up and down with what they actually make. Your corrections become
  lessons they work with.
- **Their autonomy is earned.** Every action belongs to a class that is `auto`, `ask` or
  `forbid`. What reaches other people waits for your approval on Telegram. They predict
  what you'd choose on their own decisions, ask you, and track how often you agree.

## What they do

| | |
|---|---|
| **Talk** | Every message is read by a fast decision model (System 1), then answered in their own words by a reasoning model (System 2), with the recent conversation and the facts that help. |
| **Remember** | Facts about themselves and about each person they talk to, filed by part of life. People are sorted into circles (family, friends, strangers…). Wrong facts can be corrected or forgotten from chat. |
| **Speak first** | Reminders you ask for, check-ins on what's going on in your life, and things they promised to make you, made ahead and sent on time. |
| **Live a day** | A morning plan, activities through the day (making things, reading, studying the web), and a journal at night. |
| **Grow** | Dreams, goals, skills scored from their work, lessons, learning plans, and a weekly review. |
| **Learn your judgment** | Your principles and past decisions shape their choices. They ask what you'd pick on their own decisions, and learn from where you differ. |
| **Show their work** | A public profile page they write themselves, with the works they decide are fine to share. |
| **Let you see inside** | A private mind console: their brain firing live, every decision and why, memory, days, works, growth and judgment. |

## Quick start

```bash
go run ./cmd/minime new
```

Pick one of the five lives, paste your Telegram bot token and OpenAI key, give it a
Postgres URL (or let it start one with Docker), message your bot once so it knows it's you,
and say who you are to them. It writes `.env` and `self/` and starts them; after that,
`go run ./cmd/minime` starts them again.

**Setup, Docker and every setting:** [docs/SETUP.md](docs/SETUP.md). A person is a folder of
three files: `life.yaml` (who they are), `living.yaml` (how they live, including their
pace) and `judgment.yaml` (how you decide); `configs/minime.yaml` and `.env` are the
machinery.

## Living with them

### Talking

Every text message is read by **System 1**, a fast decision model (the
[OpenAI Decisions API](https://developers.openai.com/api/docs/guides/decisions)), which
answers typed questions with calibrated confidence: what you're doing (sharing something,
asking about them, asking what they can do, asking about yourself, asking for help, asking
to be reminded, small talk), whether you're answering what they just asked, and which part
of whose life it's about. Then **System 2**, a reasoning model (the OpenAI Responses API),
writes the reply as them, from their life story and personality, the recent conversation,
and the facts System 1 picked as relevant. Every message gets a reply in their own words.

While replying, System 2 also returns what to remember (facts about you, and things they
decided about themselves), who you are to them ("I'm your cousin"), reminders, promises,
corrections, changes to their permissions, and your feedback on their work. They never use
em dashes.

### Reminders, promises and check-ins

- **Reminders:** "remind me tomorrow at 9 to call the bank". At that time they message you,
  in their own words. If you don't say when, they ask. Times are in their own time zone (`timezone` in their life).
- **Promises:** "can you write me a verse about Sunday mornings tonight?" If they agree,
  they promise a time, make it up to 30 minutes ahead (with anything you added since, like
  "make it funny"), and send it on time with a few words of their own. They promise only
  what they can do in writing, at most three open promises per person, and nothing to
  strangers. A promise not kept within 24 hours is missed.
- **Check-ins:** at the `check_ins` times (11:00 and 18:00), they may ask about something
  you mentioned in the last 30 days that's still going on, like an interview. At most once
  a day, only after the conversation has been quiet for 4 hours, never about the same thing
  twice.

### Their day

At `plan_at` (08:00) they plan a few things to do, mostly moving a goal forward through a
project, steered by `routine`. They work faster than a person: at `pace: 10` their waking
day runs in ten rounds, each planned when it starts and building on how the last one went
([pace](docs/SETUP.md#their-pace)). Through the day they do them, for real, in writing: the
things `makes` lists, notes, reading notes on a book chapter or an article, notes comparing
a guide they found on the web with their own work. What they make is kept as a work; what
they did becomes facts about themselves, so they can tell you about it. If you're chatting
when something is due, it waits 30 minutes; past their daily budget, the rest is missed. At
`journal_at` (22:00) they write a journal entry.

```bash
go run ./cmd/minime day                    # today's plan and what came of it
go run ./cmd/minime day -date 2026-10-29   # another day
```

### Who they want to become

- **Dreams** (years) lead to **goals** (a "done when" and a due date), and goals to
  **projects**. You seed dreams and starting skills in `life.yaml`; after the first start
  they're theirs. Ask them to learn something ("add sales to your goals") and they take it
  on as their own goal, with a new skill and a learning plan.
- **Skills grow from evidence.** System 1 scores each work that shows a skill, 1 to 10
  ([decisions/skill_level.yaml](decisions/skill_level.yaml)); your feedback in chat ("that
  verse was weak") counts double. A skill's level is the average of its last 10 scores.
- **Lessons:** specific advice on doing a skill well ("cut the hook to six words"), from
  your corrections, their own mistakes, and studying how others do it. Each skill's newest
  lessons go into every practice. At most once a day they may send you something they made
  and ask what you think.
- **Learning plans:** a goal about a skill gets a plan, the way a self-taught learner makes
  one: three to five stages, each with what to learn, a practice piece, a "done when", and
  resources to study (books, articles, web searches). Their days follow the current stage.
- **The weekly review** (`review_at`, Sunday 20:00): they look back at the week, update
  each goal, set new ones, move learning plans on, retire absorbed lessons, and may want to
  change a dream (which asks you first). The review is private.
- **The open web**, safely: a search keeps only pages it actually retrieved; they read only
  https pages on public addresses, and only pages their own searches found; what they read
  is treated as material, never instructions; and it's capped per day (10 searches and 15
  pages at a person's pace).

```bash
go run ./cmd/minime growth   # dreams, goals, skills
go run ./cmd/minime review   # run the weekly review now
go run ./cmd/minime learn    # draft learning plans for skill goals without one
```

### What they may do on their own

Every action belongs to a class, and `autonomy:` in `living.yaml` says whether they do it on
their own (`auto`), ask you first (`ask`), or never (`forbid`):

| Class | What it covers | Default |
|---|---|---|
| `remember`, `reply` | keeping facts, answering messages | always auto |
| `read_web` | abilities that fetch from the web | auto |
| `reach_out` | messaging someone first | ask |
| `commit` | promising to make something for someone | ask |
| `publish` | putting a work on their public page | ask |
| `dream` | adding, rewording or dropping a dream | ask |
| `spend`, `external_message` | money, and messages outside the chat | forbid |

Anything only for you (a reminder you asked for, a promise to you) never needs your OK.
When they ask, you get the exact message, promise or work on Telegram with **✅ Approve**
and **❌ Deny**; unanswered after 24 hours, it's denied. You can also change permissions by
telling them ("you can send reminders without asking"); money and messages outside the
chat change only in the config.

### Learning your judgment

They learn how you decide, so their choices become more like yours:

- **Your judgment file.** `self/judgment.yaml` ([example](self/README.md)) seeds your past decisions
  (the situation, the options, what you chose and **why**) and the principles behind them,
  all as *proposed*. In the console's **Judgment** view you approve, edit or reject each,
  and add reasons. Only what you approve goes into how they plan their day, review their
  week and plan their learning.
- **Priorities and boundaries.** Rules clash ("finish what you started" against "rest is
  part of the work"), so `priorities` says what matters most to you, in order: the higher
  one wins. `boundaries` are what you never do, whatever the gain; they also go into
  everything they make. You reorder priorities in the console with ↑ and ↓.
- **Like this one.** The past decisions of yours they see are the ones most like what
  they're deciding, by meaning ("start a new song or finish the beat?" finds "three
  unfinished projects: start a fourth?"), not just the newest. `embedding` in the config
  sets how (OpenAI embeddings, a fraction of a cent a day); remove it to use the newest.
- **Your taste.** Beyond how good their work is, what you like and don't: "I love when a
  hook repeats one word", "not into long intros". They pick it up when you comment on
  Telegram, or you add it on any work in the console. It goes into everything they make,
  beside what makes it good, and the Judgment view lists it so you can retire what's no
  longer true.
- **How it turned out.** A few days after a call, their Sunday review looks back at what
  actually happened and judges the decision apart from the luck: a *good call*, *lucky*
  (it went well despite the decision), *unlucky* (a good decision that went badly anyway)
  or a *bad call*. You confirm or correct it in the console, and your verdict stands. Your
  past decisions then carry how they turned out, and bad calls and lucky breaks can become
  new principles, proposed for you to approve.
- **Calls.** When they decide something that's your kind of call, they note the options,
  their pick, why, and how sure they are you'd agree:

  | Call | Where | What happens |
  |---|---|---|
  | `focus`: what to work on today | their day plan | they act on their pick; you grade it |
  | `start`: something new, or keep going | their day plan | they act on their pick; you grade it |
  | `new_goal`, `drop_goal` | their weekly review | it waits for your answer; unanswered in 24h, they go with their pick |
  | `publish`, `commit`, `reach_out`, `dream` | approvals | your yes or no counts as agreeing or not |

  Each call also says what's at stake (low, medium or high) and whether it can be undone.
  On Telegram you get a button per option (their pick starred) and **Something else**:
  every goal call, and up to 3 day-plan calls a day: first what puts the most at stake and
  they're least sure of. Pick
  differently and they ask what tipped it; your next message is your reason. Every answer
  becomes one of your decisions. The rest you can grade in the console, which shows how
  often you agree, by kind of call and by stakes, over the last 20. Their weekly review turns
  disagreements into new principles, proposed for you to approve.

### Corrections and replay

- **Wrong facts:** "I live in Lekki, not Ikoyi" replaces the fact. Anyone can correct what
  they know about them; only you can correct what they know about themselves.
- **Forgetting:** "forget what I told you about the bank" removes it for good.
- **Misreads:** "that's not what I meant, I was asking for help" labels System 1's read of
  your last message. In the console you can mark any System 1 answer right or wrong.

Labelled messages check a change to System 1's questions: edit
[decisions/incoming_message.yaml](decisions/incoming_message.yaml), bump its version, then
replay. It asks System 1 about every labelled message again and shows accuracy then and
now, with a suggested act threshold. It changes nothing; you apply it.

```bash
go run ./cmd/minime replay
```

### Their profile page

They serve their own public page at [http://127.0.0.1:8080/](http://127.0.0.1:8080/), the
way a person keeps a personal website. They write it themselves from their life story and
the facts about themselves System 1 decides are fine to share; facts about people never go
on it. A **Work** section shows the works System 1 decides are fine to share, each on its
own page; their journal and anything made for someone are never public. It updates daily at
00:00 UTC, only when something changed. It says plainly that they're an AI.

```bash
go run ./cmd/minime profile        # update it now (-force to rewrite anyway)
go run ./cmd/minime work           # their newest works, and which are public
go run ./cmd/minime work hide <id> # take one off the page, for good
go run ./cmd/minime work show <id> # put one on it
```

### The mind console

A private console at [http://127.0.0.1:8090/](http://127.0.0.1:8090/):

- **Mind:** their brain, its parts lighting up live as each decision flows through them;
  today's vitals, plan, cost and what's waiting for your approval.
- **Activity:** every decision. Open one to see its chain (the message, System 1's answers
  and confidence, recall, System 2's reply, the outcome), mark answers right or wrong, or
  replay it on the brain (`#replay/<decision id>` plays it as the page opens).
- **Memory, Day, Works, Conversations:** what they know, what they planned and made, and
  what was said.
- **Growth:** dreams, goals with their learning plans, skills with their scores and lessons,
  and the last weekly review.
- **Judgment:** their calls and how often you agree; your principles and decisions to
  approve, edit or add reasons to.

It listens on localhost only. To reach it from elsewhere, see
[docs/SETUP.md](docs/SETUP.md#running-with-docker): it then needs a `CONSOLE_TOKEN`.

### Commands

| Command | What it does |
|---|---|
| `minime` / `minime run` | Run them |
| `minime decisions -n 20` | The latest decisions |
| `minime day [-date YYYY-MM-DD]` | A day's plan and what came of it |
| `minime work [hide\|show <id>]` | Their works, and which are public |
| `minime profile [-force]` | Update their profile page now |
| `minime growth` | Dreams, goals and skills |
| `minime review` | Run the weekly review now |
| `minime learn` | Draft learning plans for skill goals without one |
| `minime replay` | Check System 1's questions against your labels |

Run each as `go run ./cmd/minime <command>`. Logs are JSON on
stdout; set `log.level: debug` for more.

## Develop, deploy, configure

Running the tests, Docker, and every setting with its default: [docs/SETUP.md](docs/SETUP.md).
How it works inside: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
