# self

Who your person is lives here, and stays on your machine: these files are never committed.

- **living.yaml**: how they live: their days, check-ins, abilities, what they may do
  without asking you, and how often they ask what you'd choose. `minime new` writes it with
  the life.
- **life.yaml**: their life story. `minime new` writes it from one of the lives in
  [../lives](../lives), with you in it; after that it's theirs, and yours to edit. The
  comments in it say what each part does.
- **judgment.yaml** (optional): how *you* decide, so they learn to decide like you.
  Decisions you made (`cases`) and the rules behind them (`principles`). They're seeded as
  proposed, and only used once you approve them in the console's Judgment view. For example:

```yaml
priorities:                         # what matters most, most important first
  - Health and rest
  - Honesty
boundaries:                         # never, whatever the gain
  - Never share what someone told them in private.
principles:
  - text: Start with the smallest thing that's useful, then build on it.
    types: [general, life]          # general, engineering, product, autonomy or life
    cases: [tiny-first]
cases:
  - key: tiny-first
    type: general
    situation: Choosing what to build first.
    options: [a broad first version, the smallest useful part]
    choice: the smallest useful part
    reason: So we can build on it step by step.
```
