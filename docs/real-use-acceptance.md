# Real-use acceptance protocol

`REAL_USE_ACCEPTANCE=1` enables acceptance-mode labeling in reports. It does
not change selection, difficulty, mastery, review, or probe policy. All
telemetry is local SQLite data and reports are generated on demand.

Run three sessions of 20–30 questions using the normal Practice UI. Vary
scenes and modes when natural, answer in your own words, and leave a realistic
gap between at least two sessions. Continue until there are at least 100
validated attempts if a calibration decision is required.

For each session record the session id, date, mode, provider/model, and any
provider failures. Use the quick feedback controls for too easy, too hard,
unnatural, evaluation inaccurate, or repetitive. Do not paste API keys or
answers into an issue or report.

Observation checklist:

- Review exact repeats, same-pattern streaks, scene/intent streaks, and spacing.
- Check difficulty before/after values, single-attempt jumps, ten-attempt
  drift, session drift, and probe deltas.
- Check weak-pattern last-20/50 exposure, last three outcomes, next review,
  overdue reviews, and review timing.
- Check UNKNOWN to Observed conversion, eligible patterns never sampled,
  foundation exposure, skill unlock evidence, and health flags.
- Compare the current policy with a historical replay or candidate config;
  replay is diagnostic only and never auto-applies a policy.

Use `GET /api/calibration/snapshot` or `go run -mod=mod .
calibration-report` to export a local JSON snapshot. Answers are omitted by
default. A report with fewer than 100 attempts, fewer than three sessions, or
less than seven days is `NOT_ENOUGH_DATA` or `PARTIAL_DATA`, not real-use
validation.
