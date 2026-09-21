# English Practice AI

English Practice AI is a local-first web app for practicing active English expression. A session shows a Chinese intent, the learner writes a complete English sentence, and the evaluator returns meaning, grammar, naturalness, target-pattern scores, structured errors, a more natural answer, and a short Chinese explanation.

## Run

Requires Go 1.25+. The default data file is `data/english-practice.db`; set `ENGLISH_PRACTICE_DATA` to move it and `PORT` to change the port.

```powershell
go run -mod=mod .
# open http://localhost:8080
```

The repository contains a ready-to-serve fallback bundle in `web/dist`. The Vue 3 + TypeScript source is in `src`; with network access, run `npm install` and `npm run build` to regenerate the bundle.

## Providers

Use `POST /api/providers` or the Settings screen to save an enabled provider. Supported types are `openai`, `openai-compatible`, and `ollama`. OpenAI-compatible settings are `base_url`, `api_key`, `model`, `timeout`, `temperature`, and `max_tokens`. API keys are masked in API responses and never logged. Without an enabled provider, the app uses the deterministic local evaluator so the practice flow remains usable offline.

Provider output must be JSON matching the evaluation schema. Invalid JSON, schema violations, timeouts, and non-2xx responses mark the attempt as failed and do not update mastery; one retry can be performed by the caller.

## Tests

```powershell
go test -mod=mod ./...
```

## Data and backup

SQLite migrations and seed data run on first start. Back up the SQLite file while the app is stopped, for example `Copy-Item data/english-practice.db backups/english-practice-$(Get-Date -Format yyyyMMdd).db`.
