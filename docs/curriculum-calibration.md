# Curriculum Calibration

The Rev B calibration layer is backed by the live catalog and SQLite learner
state. It does not infer evidence for a newly seeded pattern.

## Inspection

`GET /api/calibration/report` returns the current audit, including:

- catalog size, difficulty bands, skills, scenes, intents, and graph reachability;
- assessment anchors with skill, pattern, difficulty, and selection reason;
- `UNKNOWN` versus `WEAK` counts and eligible-pattern coverage;
- pattern-specific catalog versus empirical difficulty residuals;
- generator and fallback seed coverage for every catalog pattern;
- exposure by difficulty band and skill family, plus starvation candidates.

`GET /api/calibration/catalog` is an alias intended for catalog dashboards.
`GET /api/learner-state` exposes `attempt_count`, `evidence_count`,
`state_confidence`, and the derived state (`UNKNOWN`, `LEARNING`, `WEAK`,
`STABLE`, or `MASTERED`).

## Calibration rules

- A pattern with zero validated attempts is `UNKNOWN`, even when its initial
  mastery prior is low. Weak practice only considers observed patterns.
- Existing history is copied into the unified state during seed/migration;
  new catalog rows receive zero evidence and no review schedule.
- `difficulty` in the catalog is the curriculum prior. Empirical difficulty is
  reported from validated attempts and is blended conservatively into the
  pattern effective difficulty; catalog values are never rewritten.
- Assessment uses a non-catalog-order anchor route spanning foundation, daily,
  intermediate, professional, and nuance bands.
- Fallback generation maintains at least three prompts per pattern and rotates
  unseen adult daily contexts when a curated pool is exhausted.

## Verification

Run the full Go suite from the repository root:

```powershell
$env:GOCACHE = (Join-Path (Get-Location) '.runtime-data-go-cache')
go test -mod=mod ./...
```

The calibration tests cover the 44-pattern audit, anchor spread, graph
reachability, unknown/weak separation, old-user migration, and fallback
coverage.
