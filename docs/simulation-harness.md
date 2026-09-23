# Simulation & Acceptance Harness

V2.3.4 includes the V2.3.1 deterministic simulation layer for regression and a separately
defined AI learner adapter for manual acceptance. Simulation is not human
real-use validation: it can expose selection, difficulty, memory, scene,
transfer, repetition, and curriculum problems, but it cannot establish that a
person finds the exercises comfortable, natural, or useful.

## Architecture

`SimulationRunner` owns a synthetic learner state, virtual clock, attempt
trace, aggregate metrics, health flags, unlock events, and a run manifest.
Algorithm mode reuses the production difficulty controller (`targetDifficulty`
and `difficultyConfig`) and the catalog/scene mappings. The synthetic state is
stored in a dedicated in-memory SQLite store for the run; the production DB is
never opened. A caller may provide another simulation-only path, but a path
whose basename is `english-practice.db` is rejected.

The important interfaces are deliberately separate:

```go
type LearnerSimulator interface {
    Answer(context.Context, SimulationExercise, SimulatedLearnerState) (SimulatedAnswer, error)
}
type AnswerEvaluator interface {
    Evaluate(context.Context, SimulationExercise, SimulatedAnswer) (SimulationEvaluation, error)
}
```

The learner receives only the Chinese exercise, scene, difficulty band,
persona, and history summary. Reference answers, scores, desired answers, and
mastery formulas are evaluator-owned and must not be placed in that prompt.

Full-AI exercise generation uses `generator-contract-v2`. The adaptive selector
owns scene, subscene, intent, target pattern, and target difficulty through a
`GenerationSpec`; the provider returns only bounded linguistic content
(`chinese_prompt` and optional `reference_answers`). The application binds the
authoritative metadata after validation. DeepSeek/OpenAI-compatible extraction
uses `choices[0].message.content` and never promotes `reasoning_content` to the
final answer. See [generator-contract.md](generator-contract.md) for the contract,
diagnostic taxonomy, and finish-reason handling.

## CLI

```powershell
go run . simulate --mode algorithm --persona stable-intermediate --attempts 200 --session-size 20 --seed 42
go run . simulate smoke
go run . simulate extended --persona advanced-uneven --attempts 1000
go run . simulate --mode algorithm --scene meeting --subscene polite-disagreement --time-profile daily
go run . simulate --mode llm-learner --persona intermediate --attempts 50 --dry-run --format json --output report.json
```

Supported time profiles are `same-day`, `daily`, `irregular`, and `weekly`.
AI modes are capped at 100 attempts unless `--allow-large` is explicitly
provided. AI CLI execution is intentionally not enabled without separately
injected learner and evaluator adapters; `--dry-run` prints planned calls,
provider/model, and estimated cost without calling a provider.

## Report and metrics

Reports include persona/mode/seed/version metadata, sessions and virtual days,
accuracy by difficulty/pattern/scene, difficulty jitter and adjacent jumps,
productive-zone ratio, exact and normalized duplicate rates, same-pattern
spacing and streaks, weak-skill exposure, review/probe metrics, acquisition /
retention / transfer trajectories, scene coverage/mastery, transfer events,
skill unlock events, health flags, and the complete attempt trace when JSON is
requested.

Generator reports additionally include the contract version, provider request
count, initial provider success/failure, structural/semantic/adapter extraction
failure counts, provider-empty/reasoning-only counts, deterministic and LLM repair
successes, fresh retries, fallback use, final delivery, finish reason, content
source, bounded prompt/response byte counts, and per-attempt generation source.

Health flags are diagnostic only. They do not mutate calibration, difficulty,
mastery, curriculum configuration, or CI configuration.

## CI boundary

Deterministic smoke tests are suitable for CI. Extended runs and all live AI
provider calls are manual acceptance work and must not be a PR CI dependency.
