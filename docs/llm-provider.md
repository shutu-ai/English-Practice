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
