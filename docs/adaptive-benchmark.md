# Hidden-Ability Adaptive Benchmark

The adaptive benchmark uses deterministic synthetic learners with hidden
global, pattern, scene, retention, noise, fast-learning, and forgetful
behavior. The adaptive estimator receives only selected targets and observed
outcomes; the simulator alone retains true ability for comparison.

The default run covers beginner, stable intermediate, advanced, uneven, scene
uneven, fast, forgetful, and noisy personas for 1,000 attempts each:

```powershell
go run . benchmark adaptive --attempts 1000 --seed 42 --format json --output .acceptance-data/v24-adaptive.json
```

Metrics include global/pattern/scene ability MAE, time-to-convergence,
productive-zone rate, too-easy/appropriate/challenging/too-hard rates,
difficulty jitter and jumps, overshoot/undershoot, weak-skill precision and
recall, false-weak rate, review timing, retention error, probe behavior,
post-probe recovery, and unlock timing. These are diagnostic benchmark results;
they do not tune production policy, mastery, review intervals, probes, scene
mapping, or skill unlock rules.

Only deterministic benchmark smoke is suitable for CI. AI curriculum and
difficulty judges remain manual and cost-guarded; benchmark artifacts belong in
the ignored `.acceptance-data/` directory.
