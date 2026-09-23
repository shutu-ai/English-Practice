# Exercise Generator Contract

V2.3.4 uses `generator-contract-v2` for exercise generation.

## Authority boundaries

- The Adaptive Engine owns the selected scene, subscene, communication intent,
  target pattern, target skill, target difficulty, session, and selection reason.
- The LLM Generator owns linguistic realization: a natural Chinese situation and
  an optional bounded English reference answer.
- The Validator owns structural and semantic acceptance.

The generator receives a `GenerationSpec` as context. It must not return a second
copy of application-owned IDs or difficulty values. The application binds the
spec back onto the accepted `SimulationExercise`.

## Minimal response

```json
{
  "chinese_prompt": "请在项目会议中礼貌地表达你的保留意见。",
  "reference_answers": [
    "I understand your point, but I am still concerned about the timeline."
  ]
}
```

`chinese_prompt` is required, bounded, adult-relevant Chinese, and must not leak
the exact English target expression. `reference_answers` is optional at the
transport boundary but, when present, contains bounded natural English answers.
Unknown legacy metadata is ignored for backward compatibility; it cannot override
the application-owned `GenerationSpec`.

## Structural and semantic validation

Structural validation covers JSON extraction, one-object parsing, required fields,
types, non-empty content, and length limits. Semantic validation covers Chinese
learner-facing content, target-expression leakage, and the authoritative
scene/intent/pattern/difficulty context. Subsequent evaluator validation remains
responsible for judging learner answers and target-pattern use.

The first provider response counts as `initial_success` only when it is extracted,
parsed, structurally accepted, semantically accepted, and bound without repair,
retry, or fallback. Diagnostics record the contract version and bounded metadata
for every provider response, never raw provider content or secrets.

## DeepSeek/OpenAI-compatible extraction

The HTTP adapter accepts final assistant content from the standard
`choices[0].message.content` field, including text-part arrays, and records
`finish_reason`, content source, response bytes, prompt bytes, and whether
`reasoning_content` was present. `reasoning_content` is never treated as the
production answer. A response with reasoning but no final content is classified as
`reasoning_only`; an empty final field is `provider_empty`; unsupported content
shapes and known text in an unsupported field have separate adapter diagnostics.

DeepSeek's documented Chat Completions response uses `choices[].message.content`
for the final answer, may include `reasoning_content` for thinking mode, and
reports `finish_reason` on each choice. JSON output uses the OpenAI-compatible
`response_format: {"type":"json_object"}` request and a prompt that explicitly
mentions JSON. See the [DeepSeek Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion)
and [JSON Output guide](https://api-docs.deepseek.com/guides/json_mode).

`finish_reason=length` is preserved in diagnostics and causes malformed structured
content to be classified as truncated rather than as generic malformed JSON.

## Bounded safety net

V2.3.3 repair, fresh retry, fallback, state isolation, and no-retry-on-provider-4xx
protections remain. The generator is still capped at one initial request, one
repair request, and one fresh request. Repair and fallback are safety nets; they
must not be used to hide a broken primary contract.
