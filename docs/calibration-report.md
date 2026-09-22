# Calibration report and telemetry contract

The report is an on-demand diagnostic view over validated attempts, sessions,
mastery, reviews, probes, feedback, and unlock events. It does not run from
`/api/practice/next`, and feedback is stored as a calibration signal rather
than a direct state or difficulty mutation.

Endpoints:

- `/api/calibration/report?window=all|7d|30d|current_session&session_id=...`
- `/api/calibration/patterns`
- `/api/calibration/sessions/{id}`
- `/api/calibration/snapshot`
- `/api/calibration/replay?candidate={...}` for a diagnostic current-versus-
  candidate comparison; it never applies the candidate policy.
- `POST /api/feedback` with `too_easy`, `too_hard`, `unnatural`,
  `evaluation_inaccurate`, or `repetitive`.

The report includes attempt/session totals, state counts, current and
distributional difficulty, recent accuracy, repetition and spacing, review
and weak-skill exposure, probes, curriculum coverage, retention-gap buckets,
selection health flags, feedback count, and a readiness gate. Ratios and
retention buckets are marked `insufficient_evidence` when their sample is too
small; same-day volume cannot be treated as seven-day retention evidence.

Decision traces are versioned and size-limited JSON attached to generated
exercises. They contain selection reason, candidate score, review urgency,
weakness, difficulty fit, recency/repeat penalties, diversity, graph
readiness, and probe factor. Prompts, answers, API keys, authorization data,
and provider secrets are not included in snapshots by default.
