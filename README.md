# English Practice AI

English Practice AI is a local-first web app for practicing active English expression. A session shows a Chinese intent, the learner writes a complete English sentence, and the evaluator returns meaning, grammar, naturalness, target-pattern scores, structured errors, a more natural answer, and a short Chinese explanation.

## Run

Requires Go 1.25+. The default data file is `data/english-practice.db`; set `ENGLISH_PRACTICE_DATA` to move it and `PORT` to change the port.

```powershell
go run -mod=mod .
# open http://localhost:8081
```

The repository contains a ready-to-serve fallback bundle in `web/dist`. The Vue 3 + TypeScript source is in `src`; with network access, run `npm install` and `npm run build` to regenerate the bundle.

## Providers

Use `POST /api/providers` or the Settings screen to save an enabled provider. Supported types are `openai`, `openai-compatible`, and `ollama`. OpenAI-compatible settings are `base_url`, `api_key`, `model`, `timeout`, `temperature`, and `max_tokens`. API keys are masked in API responses and never logged. Without an enabled provider, the app uses the deterministic local evaluator so the practice flow remains usable offline.

Provider output is normalized into one complete evaluation JSON object. Common wrappers (short prose, Markdown fences, double-encoded content, and `evaluation`/`result` envelopes) are tolerated, while ambiguous or incomplete objects still fail validation. Structured-output failures receive up to two automatic repair retries; failed attempts never update mastery.

## Adaptive Learning V2

Practice selection is history-driven. The V2 engine stores learner state, skill graph relationships, acquisition/retention/transfer, pattern and scene difficulty, review schedules, probe flags, and selection reasons in SQLite. Inspection endpoints include `/api/learner-state`, `/api/skill-graph`, and `/api/adaptive-config`; state can be rebuilt with `POST /api/rebuild-learning-state`.

Curriculum calibration is available at `GET /api/calibration/report` (with
`/api/calibration/catalog` as a catalog-focused alias). The report audits the
44-pattern catalog, assessment anchors, graph reachability, unknown-versus-weak
state handling, catalog/empirical difficulty, curriculum exposure, generator
coverage, and fallback coverage.

## Pattern curriculum

The built-in catalog contains 44 spoken-English patterns across a gradual difficulty range. It starts with low-language-difficulty foundations such as `I am ...`, `I like ...`, and `Can I ...?`, then moves through daily conversation and workplace patterns such as `Would it be possible to ...?` and `I see your point, but ...`, ending with advanced adult patterns such as `Having said that, ...` and `I'm not entirely convinced that ...`. Foundation difficulty does not imply child audience: fallback prompts use neutral/adult daily communication by default. Skill prerequisites and learner performance decide when harder patterns appear; the catalog is data-driven and can be extended without changing the selector.

## Tests

```powershell
go test -mod=mod ./...
go vet ./...
go build -mod=mod ./...
```

## CI and real-use calibration

Every push and pull request runs the offline Go quality gates in
`.github/workflows/ci.yml`: tests, vet, and a build with a fixed Go 1.25
toolchain and module caching. CI never needs an LLM API key or a production
database. Frontend rebuilds are intentionally not a CI gate because this
repository does not currently commit an npm lockfile; the checked-in
`web/dist` bundle remains the serving artifact.

Calibration inspection is on demand so practice latency is unaffected:

```powershell
go run -mod=mod . calibration-report
# optional: $env:CALIBRATION_WINDOW='7d'
```

Use `/api/calibration/report?window=all`, `7d`, `30d`, or
`current_session&session_id=...`, plus `/api/calibration/patterns`,
`/api/calibration/sessions/{id}`, and `/api/calibration/snapshot`. Reports
label small samples as `insufficient_evidence`; code readiness is separate
from real-use validation. See `docs/real-use-acceptance.md` and
`docs/calibration-report.md` for the protocol and interpretation.
Until the protocol has produced 100+ real validated attempts across the
required sessions and time span, the correct status is `CODE READY,
REAL-USE DATA PENDING` rather than a real-use PASS.

## Data and backup

SQLite migrations and seed data run on first start. Back up the SQLite file while the app is stopped, for example `Copy-Item data/english-practice.db backups/english-practice-$(Get-Date -Format yyyyMMdd).db`.
