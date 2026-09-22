# AI Learner Simulation

AI learner simulation is an optional acceptance aid, not a replacement for
human real-use validation. Its status must be reported independently as
`VALIDATED`, `PARTIAL`, or `NOT RUN`; it must never be merged into a human
real-use PASS.

## Provider separation

Use a dedicated `LearnerSimulator` provider/model and a separate
`AnswerEvaluator` provider/model. They may be hosted by the same vendor, but
they must receive separate prompts and contexts. The learner prompt asks for
one naturally typed English sentence and forbids explanations. It receives a
difficulty band (`easy`, `appropriate`, or `challenging`) rather than an exact
numeric target.

The learner must not see reference answers, suggested answers, evaluation
scores, the desired verdict, or the mastery/update formula. This prevents the
simulation from optimizing for the harness instead of behaving like a learner.

## Safety and failure integrity

Every AI run declares `max_attempts`, `max_tokens`, `timeout`, and an estimated
cost limit. The default AI limit is 100 attempts. Dry-run reports learner,
evaluator, generator, and total calls before any request is made. Provider
errors, timeouts, empty answers, invalid responses, and evaluator failures are
system failures; they are not learner mistakes and must not update simulated
mastery or contaminate production history.

The current CLI provides the adapter and dry-run contract. Live orchestration
is intentionally `PARTIAL` until a caller injects both learner and evaluator
providers and supplies a manual acceptance report.

## What AI can and cannot establish

AI can reveal unrealistic error patterns, scene mismatch, repetitive prompts,
abrupt difficulty changes, weak-skill overexposure, and generator/evaluator
quality problems. It cannot establish human comfort, naturalness to native
speakers, TTS quality, preference, or long-term learning outcomes.

