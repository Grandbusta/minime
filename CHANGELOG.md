# Changelog

## v0.1.0: first public release

The first version anyone can run: create an AI person, talk with them on Telegram, and
watch them live.

- **Five lives, ready to start:** Daniel, Mei, Tomás, Priya and Elsa. `minime new` picks
  one, sets up `.env`, finds your Telegram ID, can start a Postgres in Docker, and starts
  them. Your own person stays private in `self/`.
- **Talking:** a fast decision model reads each message (System 1) and a reasoning model
  answers in their own words (System 2). Memory of themselves and of each person, by
  circle; corrections and forgetting from chat; names used the way people do when texting.
- **Speaking first:** reminders, promises made ahead and sent on time, and check-ins.
- **Their own day:** a morning plan, real work in writing (making things, reading books and
  web pages), a journal at night, and a `pace` to work faster than a person.
- **Growth:** dreams, goals (including ones you ask them to take on), skills scored from
  their work, lessons, learning plans that move on as they practise, and a weekly review.
- **Earned autonomy:** every action is `auto`, `ask` or `forbid`, with approvals on Telegram.
- **Learning your judgment:** principles, priorities, boundaries and past decisions;
  calls with stakes, predictions you answer with a tap, outcomes judged apart from luck,
  similar decisions found by meaning, and your taste.
- **Seeing inside:** a private mind console and a public profile page they write themselves.
- **Running it:** one binary, Postgres, OpenAI; Docker without compose.
