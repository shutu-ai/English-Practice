# LLM provider contract

`LLMClient.Chat(ctx, ChatRequest)` is the only provider boundary. The business services never call a vendor SDK directly. A provider response is expected to contain `choices[0].message.content` with this JSON shape:

```json
{
  "verdict":"mostly_correct", "meaning_score":0.91,
  "grammar_score":0.82, "naturalness_score":0.72,
  "pattern_score":0.65, "errors":[],
  "suggested_answer":"I was going to call you yesterday.",
  "explanation_zh":"..."
}
```

Scores are all in `[0,1]`; verdict is one of `correct`, `mostly_correct`, `needs_improvement`, or `incorrect`. The provider test endpoint sends a minimal request and returns latency plus success without exposing secrets.

## Evaluation pipeline diagnostics

Provider envelopes are normalized before evaluation parsing. The server accepts the common `choices[0].message.content`, Ollama-compatible content arrays, `response`, and `output_text` shapes. Assistant content may be a fenced JSON object; prose outside a JSON object is rejected. Scores are normalized from `[0,1]`, numeric `[0,100]`, percentage strings, or decimal strings, while ambiguous and out-of-range values fail validation. Verdicts, error types, and severities are normalized to canonical enums; `errors: null` becomes `[]`.

An invalid structured result gets one repair request with an explicit JSON-only instruction. Provider failures are classified as timeout, 4xx, 5xx, envelope, or structured-output failures. Attempts retain a safe diagnostic record containing request ID, provider type, model, status, latency, failure stage, response shape, and schema error. Secrets and Authorization headers are never persisted.

Failed attempts can be re-evaluated through `POST /api/attempts/{attempt_id}/reevaluate`. A successful re-evaluation writes one Evaluation and updates mastery, scene mastery, and review scheduling in the same transaction. Repeating the operation after success is idempotent.
