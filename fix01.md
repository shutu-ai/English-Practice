当前真实使用发现 V1 核心问题。

## Reproduction

练习题：

```text
我没有去，因为我感觉不舒服。
```

用户回答：

```text
I didn't go, because I didn't feel well.
```

应用显示：

```text
AI 评估失败，本次不会更新掌握度。
```

这个英文回答本身语义和语法均合理，因此不能把本次问题归因为用户回答错误。

请对真实 AI Evaluation pipeline 做完整审计、复现、定位和修复。

# 1. 首先复现

使用当前实际配置的 Provider 和模型复现该题。

检查完整链路：

```text
POST attempt
→ AnswerEvaluator
→ prompt construction
→ LLMClient
→ provider HTTP request
→ raw response
→ content extraction
→ JSON parsing
→ schema validation
→ business validation
→ Evaluation persistence
→ Mastery update
```

明确报告失败发生在哪一步。

不要只增加 fallback 绕过真实问题。

---

# 2. 保存安全诊断信息

增加可用于诊断但不泄露 secret 的信息：

```text
evaluation/request id
provider type
model
HTTP status
latency
failure stage
error category
schema validation error
```

禁止记录：

```text
API key
Authorization header
secret
```

开发日志中允许记录经过安全处理的 LLM response，以便诊断 structured-output 问题。

---

# 3. 检查 Structured Output

重点检查模型真实返回是否存在以下情况：

```text
Markdown fenced JSON
额外解释文字 + JSON
缺字段
字段名变化
null instead of []
score 使用 0-100
score 使用字符串
未知 verdict
未知 error type
trailing comma
provider response envelope 差异
```

需要保证至少支持合法但常见的 Provider 输出差异。

---

# 4. Parser Robustness

Structured output 必须遵循：

```text
raw provider response
↓
extract assistant content
↓
strip optional Markdown JSON fence
↓
strict JSON parse
↓
schema validation
↓
business validation
```

不要使用危险的“随便从文本猜 JSON”逻辑。

可以兼容：

````text
```json
{...}
````

````

但如果 JSON 前后出现大量自然语言，不要静默接受不可确定的数据。

---

# 5. Score normalization

系统统一要求：

```text
0.0 ～ 1.0
````

检查不同 Provider 是否可能返回：

```text
0.85
85
"0.85"
```

原则：

* 首选通过 Prompt / JSON Schema 强制 0..1。
* 对明显的 `0..100` 数值，如果可以确定安全转换，可规范化为 `/100` 并记录 normalization。
* 不接受无法确定语义的值。
* NaN、负值、>100 一律 validation failure。

---

# 6. Enum robustness

检查：

```text
verdict
error.type
severity
```

是否因模型大小写、下划线或空格差异导致整个 Evaluation 失败。

优先通过 Schema/Prompt 强制 canonical enum。

可以安全 normalize：

```text
Mostly Correct
mostly correct
mostly_correct
```

到：

```text
mostly_correct
```

但不要接受语义完全未知的 enum。

---

# 7. Empty errors

正确答案时：

```json
"errors": []
```

必须被正确接受。

如果 Provider 返回：

```json
"errors": null
```

决定是否安全 normalize 为：

```json
"errors": []
```

如果 Schema 当前因此让完全正确答案大量失败，应修复。

---

# 8. Target Pattern

检查普通自然表达正确，但没有严格使用“目标句型”的场景。

例如：

```text
我没有去，因为我感觉不舒服。
```

目标可能是：

```text
because
```

用户：

```text
I didn't go because I didn't feel well.
```

应该正常评估。

即使用户采用另一种正确表达，也不能因为 target pattern 评分较低而导致整个 Schema/业务校验失败。

`pattern_score` 是评价维度，不是 Evaluation validity gate。

---

# 9. Retry

当前要求一次 retry。

检查：

```text
第一次为什么失败
第二次是否使用完全相同请求
```

如果第一次是：

```text
invalid structured output
```

第二次应该加入明确 repair instruction，例如：

```text
Return only one valid JSON object matching the required schema.
No markdown.
No explanation outside JSON.
```

但仍然只允许有限 retry。

禁止无限 retry。

---

# 10. Provider compatibility

分别检查：

```text
OpenAI
OpenAI-Compatible
Ollama
```

不要假定所有所谓 OpenAI-compatible endpoint 都支持相同的：

```text
response_format
json_schema
tool schema
```

需要根据 Provider capability 选择：

```text
native structured output
```

或：

```text
prompt-enforced JSON + local validation
```

Provider feature mismatch 不应该直接造成所有练习无法使用。

---

# 11. 用户错误提示

不要只显示：

```text
AI 评估失败，本次不会更新掌握度。
```

用户界面保持简洁，但增加：

```text
AI 评估暂时失败，本次记录已保存，但不会影响你的掌握度。

[重新评估]
```

如果适合，可显示简短类别：

```text
模型返回格式异常
Provider连接失败
请求超时
```

不要向普通用户展示 API Key 或完整 raw response。

---

# 12. Re-evaluate

增加对失败 Attempt 的安全重新评估能力。

状态例如：

```text
evaluation_status = failed
```

允许：

```text
Re-evaluate
```

成功后事务化：

```text
save Evaluation
update Pattern Mastery
update Scene Mastery
update Review Schedule
```

必须保证幂等。

重复点击不能重复增加 attempts/mastery。

---

# 13. Regression tests

至少新增：

```text
valid correct answer
mostly correct answer
errors=[]
errors=null if normalized
Markdown fenced JSON
score 0..1
score 0..100 normalization if supported
enum normalization
missing required field
invalid JSON
provider timeout
401
429
500
first invalid + retry valid
both attempts invalid
failed evaluation does not mutate mastery
re-evaluation updates mastery exactly once
```

加入该真实 case：

```text
Chinese:
我没有去，因为我感觉不舒服。

Answer:
I didn't go, because I didn't feel well.
```

Mock LLM 正常结果必须能够完整通过 Evaluation pipeline。

---

# 14. 真实 Provider 验证

修复后，用当前实际 Provider 再次测试至少以下自然回答：

```text
我没有去，因为我感觉不舒服。
I didn't go because I didn't feel well.

我明天可能会晚一点。
I might be a little late tomorrow.

我本来打算昨天给他打电话。
I was going to call him yesterday.
```

检查：

```text
request PASS
parse PASS
validation PASS
evaluation persisted
mastery updated once
UI displays evaluation
```

---

# 15. 不允许的修复方式

禁止：

```text
捕获所有错误然后默认判 correct
解析失败时默认 score=0.5
关闭 schema validation
删除 Mastery failure protection
把失败的 LLM output 强行写入 Evaluation
遇到失败直接使用 keyword/string match 更新 Mastery
```

V1 的原则继续保持：

```text
坏的评估宁可不更新 Mastery，
也不能污染长期学习画像。
```

---

# 16. 完成后输出

报告：

```text
Root Cause:

Affected Providers:

Failure Stage:

Actual Raw Response Shape:
（脱敏摘要）

Code Changes:

Parser Changes:

Schema Changes:

Retry Changes:

UI Changes:

Re-evaluate Support:

Tests:

Real Provider Verification:

Mastery Integrity Verification:

Final Status:
FIXED / PARTIAL / BLOCKED
```

只有真实复现的问题已经修复，并且用实际 Provider 验证成功，才能标记 `FIXED`。
