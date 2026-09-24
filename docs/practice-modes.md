# Practice Modes (V2.6)

Practice has two independent controls. Difficulty is `adaptive` or `fixed`; training focus is `pattern` or `free_expression`. Omitted values preserve the historical Adaptive + Sentence Pattern experience. Preferences are saved in the browser and copied into each session and exercise snapshot with the curriculum version.

## Difficulty

Adaptive mode uses the existing learner model to choose curriculum targets and numeric exercise difficulty. Fixed mode requires one whole curriculum level from D1 to D8. It reuses V2.5 curriculum eligibility and prerequisite readiness, then selects eligible patterns in the configured numeric band around the chosen level. The session remains at the selected level. Correct or incorrect answers can update learner evidence and ability estimates, but cannot move the selected level or create cross-level probes. Change the level explicitly to move on.

Prerequisite readiness remains part of candidate selection. A pattern whose prerequisites are not ready is shown as Blocked and stays in the required completion denominator. The app does not silently switch to an earlier level. Coverage and mastery are separate: coverage counts eligible patterns with evidence; mastery counts eligible patterns meeting the existing mastery state. Unseen is never counted as Weak.

Level progress is available at `GET /api/progress/difficulty/{level}` and is shown in Fixed + Sentence Pattern mode. It reports eligible, covered, mastered, unseen, learning, weak, review-due, and blocked counts; coverage and mastery rates; completion; and current review health. A level remains historically completed when mastered patterns later become due for review. Completion never advances the selected level automatically.

When continuing a completed level, the existing selector prioritizes review and weak patterns, adds pressure for unseen eligible patterns, and retains interleaving and scene/intent variation. Already mastered patterns receive less weight unless review or retention evidence makes them relevant. Fixed mode does not use cross-level probes.

## Training focus

Sentence Pattern mode follows the existing scene, intent, curriculum, target-pattern, and numeric-difficulty generator path. Evaluation considers meaning, grammar, naturalness, communication intent, and target-pattern fit. Semantic equivalents remain valid pattern matches; evaluation distinguishes exact, semantic equivalent, partial, and unmatched results.

Free Expression sends the generator the curriculum level, scene, intent, and numeric difficulty, without a target pattern. The prompt must not imply a required structure. Evaluation scores meaning, grammar, naturalness, intent, and appropriateness, and treats target-pattern fields as not applicable. Pattern score is omitted from the user-facing score, no target-pattern penalty is applied, and Pattern Mastery or pattern review state is not updated. Scene and general difficulty evidence can still update. Free Expression may test spontaneous transfer, but it is not direct evidence of a named pattern.

Both generation paths retain application-owned curriculum metadata, curriculum validation, bounded provider retries, optional alternatives, and pronunciation playback for the learner answer and suggested answer.

## Four combinations

| Difficulty | Focus | Behavior |
| --- | --- | --- |
| Adaptive | Sentence Pattern | Existing adaptive pattern practice |
| Adaptive | Free Expression | Adaptive level and difficulty, open expression |
| Fixed | Sentence Pattern | User-selected D-level, in-level pattern mastery |
| Fixed | Free Expression | User-selected D-level, open expression |

The API accepts `difficulty_mode`, `fixed_difficulty`, and `training_focus` on `POST /api/sessions` and `POST /api/practice/next`. Fixed mode without an integer D1–D8 value returns a validation error. In Adaptive mode `fixed_difficulty` is ignored. Historical attempts without snapshots remain readable and are not backfilled with inferred modes.

The History view labels attempts by difficulty mode, fixed level when applicable, and training focus. Session and exercise snapshots retain their settings and curriculum version when preferences change; a new setting applies to the next exercise.

## Verification commands

```powershell
go run . simulate --mode algorithm --difficulty-mode adaptive --training-focus pattern --attempts 100 --seed 42 --format json
go run . simulate --mode algorithm --difficulty-mode adaptive --training-focus free_expression --attempts 100 --seed 42 --format json
go run . simulate --mode algorithm --difficulty-mode fixed --fixed-difficulty 4 --training-focus pattern --attempts 1000 --seed 42 --format json
go run . simulate --mode algorithm --difficulty-mode fixed --fixed-difficulty 4 --training-focus free_expression --attempts 500 --seed 42 --format json
```

The bounded live acceptance command uses the configured providers with an
isolated in-memory learner database, masks credentials, snapshots production
history counts before and after, and caps retries and total calls. Stage A runs
10 exercises in each mode plus D1/D6 safety samples; enable Stage B only after
Stage A passes:

```powershell
go run . v26-live --stage-a 10 --stage-b 0 --fixed-edge-samples 5 --max-provider-calls 300
go run . v26-live --stage-a 10 --stage-b 10 --fixed-edge-samples 5 --max-provider-calls 400
```

The command writes `.acceptance-data/v26-live-run.json` and
`.acceptance-data/v26-acceptance-report.md`.
