# Provider Reliability (V2.3.5)

The simulation harness keeps provider failures separate from learner behavior.
All provider calls use the shared OpenAI-compatible response adapter, while
each role retains its own output contract and recovery policy.

| Role | Output contract | Recovery |
| --- | --- | --- |
| Generator | Minimal JSON containing `chinese_prompt`; application metadata remains authoritative | bounded repair, fresh retry, then application fallback |
| Learner | One plain English answer sentence | one fresh retry for transient, empty, reasoning-only, timeout, or 5xx failures |
| Evaluator | Validated structured evaluation with verdict, scores, target-pattern match, feedback, and error taxonomy | deterministic JSON extraction, one schema repair, then one fresh evaluation retry |

## Provider envelope

The adapter reads final assistant text from `content` (including
`choices[].message.content`). `reasoning_content` is diagnostic metadata only;
it is never promoted to learner text or evaluator JSON. `finish_reason` and a
bounded response shape are retained in diagnostics. Empty content and
reasoning-only responses are system failures, not incorrect learner answers.

## Failure and retry policy

Learner and evaluator failures are classified independently. The taxonomy
includes provider timeout/HTTP error, provider empty, reasoning-only, adapter
extraction empty, truncated response, malformed JSON, schema invalid, invalid
enum, missing required field, unexpected content, and unknown.

Learner retries preserve the same persona, exercise, difficulty context, and
history. The retry only reinforces the output contract; it does not reveal a
reference answer, verdict, rubric, or correctness hint.

Evaluator repair may tolerate bounded fences or surrounding prose. A repair
prompt receives the bounded invalid response and schema error, but never treats
the previous verdict as authoritative. A fresh retry reconstructs the request
from the original exercise, learner answer, target pattern, and intent.

Every provider call is counted by the simulation guard. There is no unbounded
`while invalid` loop.

## State mutation boundary

```text
Generator accepted -> no learner-state mutation
Learner answer accepted -> no mastery mutation
Evaluator accepted -> one atomic evaluation and learning-state commit
```

Failed learner chains are recorded as system failures and are not evaluated.
Failed evaluator chains remain unevaluated and do not alter mastery, scene
mastery, review scheduling, transfer, or difficulty. The production evaluator
persists evaluation, mastery, scene, review, transfer, and difficulty evidence
inside one transaction. The `evaluations.attempt_id` uniqueness constraint and
the validated-attempt guard make re-evaluation idempotent.

Simulation reports separately expose initial success, recovered retry/repair,
final failure, complete chains, and full-chain completion rate. Token usage is
reported as unavailable when the provider does not return usage data; zero is
not interpreted as a real zero-cost run.
