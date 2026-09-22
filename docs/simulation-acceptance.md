# Simulation Acceptance Protocol

## Deterministic gate

Run the smoke suite in CI and use a fixed seed for every regression. Compare
aggregate behavior and trajectories, not every generated prompt. The expected
checks are:

- same persona, seed, and attempt count produce identical metrics, trace, and
  unlock events;
- simulation virtual time advances without waiting for real days;
- beginner exposure is foundation-heavy and advanced foundation exposure is
  low;
- weak patterns receive more exposure without exceeding the configured
  consecutive streak;
- difficulty jitter remains bounded and probe recovery returns to the normal
  envelope;
- review intervals expand after success and contract after failure;
- scene-specific weakness lowers scene mastery without globally destroying
  pattern mastery;
- cross-scene transfer is recorded;
- production DB isolation and explicit production-path rejection pass.

## Manual runs

Use 1,000–2,000 deterministic attempts per persona when investigating a long-
term policy change. Use at least 20–50 attempts for each AI acceptance
scenario: intermediate daily life, intermediate meeting, advanced meeting,
weak phone call, and travel. Record the seed, provider/model, generator mode,
call counts, token usage when available, report, and health flags.

## Status vocabulary

```text
Deterministic Simulation: VALIDATED / PARTIAL / FAILED
AI Simulation:            VALIDATED / PARTIAL / NOT RUN
Human Real-use:           PENDING / IN_PROGRESS / VALIDATED
```

These three statuses are independent. A deterministic PASS does not prove AI
quality, and an AI acceptance run does not prove human real-use quality.

