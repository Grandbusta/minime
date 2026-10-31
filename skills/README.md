# Skills

Know-how you can give any person, in the Agent Skills format (the open `SKILL.md` format Claude uses):
one folder per skill, each with a `SKILL.md`.

```
skills/cold-email/
  SKILL.md        # ---, name: cold-email, description: what it's for and when, ---, then the instructions
  examples.md     # any files the instructions point to
```

Skills here are shared by every life; a person's own go in `self/skills/` (or wherever
`agent_skills.dirs` says), and win on a name clash. The one that fits what they're talking
about or doing is given to them by its description's meaning; they can also read any with
`use_skill`.

Only add skills you've read or trust: their instructions are followed. A skill gives
know-how, never permission (the autonomy gate still decides what they may do), and
scripts in it are never run.
