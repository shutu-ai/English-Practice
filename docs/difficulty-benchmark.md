# Independent Difficulty Benchmark

The V2.4 difficulty benchmark compares production target and realized
difficulty with an independent `IndependentDifficultyJudge`. The judge sees
only the Chinese prompt, the expected communication task, and reference
answers. It does not receive target difficulty, realized difficulty, catalog
difficulty, learner ability, or production mastery.

The report includes overall MAE, bias, correlation, D1-D2 through D6-D8
groups, pattern and scene hotspots, and generator compliance. A result over
the configured threshold is a `CALIBRATION_CANDIDATE`; the benchmark never
updates catalog difficulty or production state automatically.

```powershell
go run . benchmark difficulty --samples 500 --seed 42 --format json --output .acceptance-data/v24-difficulty.json
```

The judge dimensions cover lexical, grammar, sentence length, clauses,
tense/aspect, conditional/modal complexity, information units, abstraction,
pragmatics, expression freedom, and discourse complexity.
