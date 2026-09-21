# Adaptive Learning Engine V2

The application owns learning policy. Providers only generate language and evaluate answers.

The initial curriculum catalog contains 44 spoken-English patterns from child-friendly foundations through adult workplace communication. Each pattern has a difficulty, intent, skill mapping, curated fallback prompts, and prerequisite relationships so the same adaptive selector can expand without hard-coding a fixed eight-pattern course.

## Closed loop

`attempts -> learner_skill_state -> skill graph + memory + difficulty -> candidate pool -> exercise -> evaluation`.

Every generated exercise stores `selection_reason`, `practice_mode`, `is_review`, `is_probe`, `is_new_skill`, `generated_by`, and a normalized Chinese hash. This makes each choice auditable from History.

## State and update rules

For a validated answer, the deterministic update is:

* `score = .45 pattern + .35 meaning + .20 grammar` (clamped to 0..1).
* Acquisition is an exponential moving average (`.65 old + .35 score`).
* Retention is updated for delayed or scheduled reviews (`.75 old + .25 score`).
* Transfer is an exponential moving average of naturalness and context coverage.
* Mastery combines acquisition, retention, transfer, and consecutive successes.

Provider failure never enters this transaction, so it cannot mutate learner state.

## Configuration

Policy weights, repeat windows, thresholds, and probe ratio live in `adaptive_config` and have safe defaults. They are exposed through `/api/adaptive-config`.
