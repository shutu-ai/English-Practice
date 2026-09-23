# Practice Modes (V2.5)

Practice keeps the existing Adaptive + Sentence Pattern behavior by default,
and adds two explicit controls: difficulty mode (`adaptive` or `fixed`) and
training focus (`pattern` or `free_expression`). The four combinations are
stored on sessions, exercises, and attempts for auditability.

Fixed D1–D8 uses the configured center with a ±0.3 eligibility band. Success,
failure, probes, and reviews do not move the center or create cross-level
probes. Pattern mode still uses learner state, coverage pressure, and review
priority within the band. `UNSEEN` remains distinct from `WEAK`; completion is
reported only when every eligible built-in pattern is mastered.

Free Expression receives scene, intent, and effective difficulty but no target
pattern. The evaluator scores meaning, grammar, naturalness, and intent. The
stored target match is `not_applicable`; pattern penalties and Sentence Pattern
Mastery updates are disabled. Scene/general difficulty and session evidence may
still update normally.

The API accepts `difficulty_mode`, `fixed_difficulty`, and `training_focus` on
`POST /api/sessions` and `POST /api/practice/next`. Fixed-band progress is
available at `GET /api/progress/difficulty/{level}`.

Deterministic acceptance scenarios:

```powershell
go run . simulate fixed-d4-pattern --format json
go run . simulate fixed-d4-free --format json
go run . simulate adaptive-free --format json
```
