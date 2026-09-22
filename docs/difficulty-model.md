# Difficulty Model

## V2.2 policy

The controller keeps three values separate:

1. `learner_ability` is a slow-moving, history-backed long-term estimate.
2. `session_difficulty_center` is initialized from learner ability and recent
   stable evidence, then moves only from rolling non-probe evidence.
3. `target_difficulty` is an exercise target. It combines the session center,
   learner ability, pattern ability, and catalog difficulty.

`realized_difficulty` is the provider estimate or deterministic heuristic for
the generated exercise. It is stored independently from the target. A
provider result outside `difficulty_mismatch_threshold` is retried and then
replaced with a curated fallback; every failed validation is recorded as
`difficulty_validation_failed`.

Normal exercises are clamped to the session envelope
`session_band_lower..session_band_upper`. Weak and review items remain inside
that envelope while preserving productive challenge. Probes are explicitly
marked and may use `center + probe_delta`; probe outcomes do not directly
lower long-term ability, the session center, or mastery.

All numeric policy limits live in `adaptive_config`, are returned by
`GET /api/adaptive-config`, and are applied by the deterministic controller:

| Key | Default | Meaning |
| --- | ---: | --- |
| `deadband_low` / `deadband_high` | 0.70 / 0.85 | neutral performance band |
| `max_ability_step` | 0.12 | maximum long-term ability movement |
| `max_session_center_step` | 0.10 | maximum center movement per update |
| `max_pattern_target_step` | 0.20 | maximum pattern-target movement |
| `recent_window_size` | 10 | rolling evidence window |
| `ewma_alpha` | 0.35 | recent evidence weighting |
| `session_band_lower` / `upper` | 0.45 / 0.45 | center envelope width |
| `probe_min_ratio` / `max_ratio` | 0.05 / 0.15 | adaptive probe bounds |
| `probe_delta` | 0.40 | isolated probe stretch |
| `difficulty_mismatch_threshold` | 0.60 | target/realized validation tolerance |

The definition of `difficulty_jitter` is the mean absolute difference between
adjacent realized exercise difficulties. Reports also expose session p25,
p50, p75, range, target/realized mismatch rate, feedback by target band,
pattern and selection reason, and the deterministic five-persona simulation.

## Inspection APIs

- `GET /api/calibration/report` — stability metrics and health flags.
- `GET /api/calibration/difficulty-trace?limit=50|100` — the latest decision
  trace with learner ability, session center, pattern ability, target,
  realized difficulty, evaluation, and selection reason.
- `GET /api/calibration/replay?candidate={...}` — diagnostic before/after
  historical replay; it never applies the candidate policy.

Global, pattern, and scene difficulty are stored independently. Candidate fit
is `1 - abs(candidate - learner) / 4`, while the V2.2 policy keeps normal
practice near the configured productive success range and applies probes as a
limited, explicitly isolated stretch.
