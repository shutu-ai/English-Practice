# English Practice AI V2.2 Difficulty Stabilization

Baseline Commit: `e299cb813c65d22c35dba2bafd7aa3e958670ced`

New Commit: `fdced8c96ca0fba63545fc735c87fc8431ff82e9`

## Root Cause Analysis

The former controller used one global difficulty value for long-term learner
ability, session selection, pattern difficulty, and generated exercise
difficulty. A single evaluation could move that value immediately. Weak,
review, scene, and probe paths then applied different implicit adjustments, and
the provider's requested difficulty was treated as the realized difficulty.
The result was high short-term variance with no reliable target/realized audit.

V2.2 separates learner ability, Session Difficulty Center, pattern target, and
exercise realized difficulty. Rolling evidence, EWMA, confidence, deadband,
bounded steps, a session envelope, and probe isolation now govern the normal
practice path.

## Implemented Policy

Difficulty Policy Version: `difficulty-v2.2`

- Learner Ability: rolling non-probe evidence, configurable EWMA and maximum
  step.
- Session Difficulty Center: initialized from long-term ability; updates only
  after rolling evidence and remains inside a configurable envelope.
- Deadband: configurable `deadband_low` / `deadband_high`.
- Bounded Step: `max_ability_step`, `max_session_center_step`, and
  `max_pattern_target_step`.
- Rolling Window: configurable `recent_window_size`, default 10.
- Pattern-specific Difficulty: combines learner ability, session center,
  pattern ability, and catalog difficulty.
- Weak / Review: controlled productive-challenge and retention-aware targets.
- Probe Isolation: probes are marked, bounded, excluded from direct center and
  long-term ability movement, and return to the normal baseline.
- Session Envelope: `session_band_lower` / `session_band_upper`.
- Target vs Realized: every new exercise stores both values, delta, validation
  status, and policy version.
- Validator: deterministic heuristic features plus configured mismatch
  threshold; failed provider estimates retry and then use a recorded fallback.
- Decision Trace: learner ability, session center, pattern ability, target,
  realized value, selection reason, and adjustment reason are persisted.

## Real History Analyzed

Source: local `data/english-practice.db`, captured after the V2.2 migration.

- Validated attempts: 64
- Sessions: 56
- Historical difficulty jitter: `1.0063`
- Historical session difficulty p25 / p50 / p75: `3.2 / 4.2 / 5.2`
- Historical session difficulty range: `2.2..6.0`
- Target/realized mismatch rate: `0` for the migrated records with known
  realized values; legacy unknown values are not fabricated.
- Probe ratio: `0.09375`
- Health flag: `DIFFICULTY_JITTER_HIGH`

The data is below the required 50–100 *new V2.2 real-use attempts* acceptance
window, so it is evidence for diagnosis, not proof of improved real-use
stability.

## Before / After Replay

Replay command: `GET /api/calibration/replay?window=all&candidate={"changes":{"max_session_center_step":0.05}}`

- Historical replay baseline jitter: `1.0063`
- Candidate replay jitter: `0.1660`
- Historical replay range: `2.2..6.0`
- Candidate replay range: `1.878..3.644`
- Historical target/realized mismatch: `0`
- Candidate target/realized mismatch: `0.8125`

This candidate is diagnostic only and was not applied. The mismatch trade-off
shows why replay output must be reviewed together with target/realized
validation instead of optimizing jitter alone.

## Simulation Results

The deterministic harness covers `stable_intermediate`, `strong_but_uneven`,
`noisy_learner`, `fast_learner`, and `struggling_learner` for 500 attempts.
It reports ability/session trajectories, jitter, productive-zone ratio, max
single-step change, probe ratio, and probe recovery rate. Tests assert
determinism and bounded movement without an external provider.

## APIs

- `GET /api/calibration/report`
- `GET /api/calibration/difficulty-trace?limit=50|100`
- `GET /api/calibration/replay?candidate=...`
- `GET /api/adaptive-config`

## Verification

- `go test ./...`: PASS
- `go vet ./...`: PASS
- `go build`: PASS
- frontend typecheck: PASS
- frontend static checks: PASS
- frontend speech tests: PASS
- frontend Vite build: PASS
- runtime health/session/next/attempt/report/trace smoke test: PASS
- GitHub Actions: PENDING until the commit is pushed

Migration: PASS; old attempts, mastery, history, and provider configuration
remain intact. Missing historical realized difficulty remains `unknown`.

Known Limitations: no new 50–100 attempt real-use window has been collected;
the local database still reports the pre-stabilization jitter signal, and the
remote CI result cannot be verified before push.

Code Status: READY

Real-Use Stability: PENDING
