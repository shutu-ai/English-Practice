# English Practice AI — Adaptive Learning Engine V2

在当前仓库基础上继续开发：

```text
https://github.com/shutu-ai/English-Practice
```

当前 V1 已具备：

* Go + SQLite
* Vue 3 + TypeScript
* Practice / Review / History / Progress
* Pattern Mastery
* Scene Mastery
* Review Scheduler
* Adaptive Assessment
* OpenAI / OpenAI-Compatible / Ollama
* LLM Answer Evaluation
* 本地学习历史

本阶段目标不是重写应用，也不是简单“增加 AI 出题”。

目标是把现有系统升级为真正的：

> **History-driven Adaptive Learning Engine**

核心闭环：

```text
练习历史
   ↓
Learner Model
   ↓
Skill Graph
Memory Model
Difficulty Model
Practice Policy
   ↓
Exercise Specification
   ↓
AI Exercise Generator
   ↓
用户回答
   ↓
AI Evaluation
   ↓
更新学习状态
   ↓
下一题
```

---

# 1. 核心目标

V2 必须解决以下问题：

1. 练习历史真正参与下一题决策。
2. 不再简单根据单一 `global_difficulty` 出题。
3. 错题和不熟练题型需要按记忆模型重复。
4. 重复必须有节奏，禁止无控制连续刷同一题型。
5. 重复的主要对象是：

   * Pattern
   * Skill
   * Intent
   * Scene
     而不是机械重复完全相同题目。
6. AI 负责生成具体题目，但不决定学习策略。
7. 难度必须根据真实历史动态估计。
8. 同一个 Pattern 可以在不同难度和不同场景下训练。
9. 系统应逐步判断用户：

   * 会什么
   * 不会什么
   * 是否只是短期记住
   * 是否真正长期掌握
   * 是否可以迁移到其他场景
10. 形成真正的个性化训练节奏。

---

# 2. 架构原则

必须坚持：

```text
LLM 负责语言生成与语言理解
程序负责学习策略
```

LLM 可以：

```text
生成具体中文练习题
评价用户英文答案
解释错误
生成同一能力点的不同语境变式
```

LLM 禁止直接决定：

```text
下一题选什么 Pattern
什么时候复习
用户是否升级
Mastery 值
Review Interval
Global Difficulty
是否解锁新技能
```

这些必须由 deterministic application logic 决定。

---

# 3. V2 核心组件

建立以下五个明确组件：

```text
1. Learner Model
2. Skill Graph
3. Memory Model
4. Difficulty Model
5. Practice Policy
6. AI Exercise Generator
```

其中 Learner Model 是其他模块共享的状态基础。

不要让各模块各自重新计算一套互相不一致的用户状态。

---

# 4. Learner Model

建立统一的用户学习状态。

建议新增核心实体：

```text
learner_skill_state
```

每个用户 + Pattern / Skill 至少维护：

```text
skill_id / pattern_id

mastery
acquisition
retention
transfer

attempt_count

success_count
failure_count

recent_accuracy
long_term_accuracy

current_difficulty
max_success_difficulty

consecutive_success
consecutive_failure

last_seen_at
last_success_at
last_failure_at

next_review_at

scene_coverage
intent_coverage
context_diversity

memory_strength
stability

state_version
updated_at
```

不要求 V2 第一版所有字段都采用复杂公式，但：

* 数据结构要合理
* 各字段职责清楚
* 不要只保留一个 `mastery = 0.xx`

---

# 5. 练习历史作为事实源

现有：

```text
attempts
evaluations
sessions
pattern_mastery
scene_mastery
```

需要重新审计。

历史记录应成为所有学习状态的事实来源。

每次 Attempt 至少能关联：

```text
exercise_id
pattern_id
scene_id
intent_id

exercise_difficulty

practice_mode

selection_reason

is_review
is_probe
is_new_skill

user_answer

evaluation scores
error types
error severity

submitted_at
```

`selection_reason` 建议支持：

```text
current_level
weak_skill
scheduled_review
retention_check
transfer_check
new_skill
probe
maintenance
recovery
```

这样以后可以解释：

> 为什么系统这次出了这道题。

---

# 6. Skill Graph

不要再把 Sentence Pattern 看成孤立条目。

建立：

```text
Skill Graph / Curriculum Graph
```

表示能力之间的：

```text
前置关系
同类关系
进阶关系
可替代表达关系
```

例如：

```text
basic_reason
   ↓
because
   ↓
because_of
   ↓
since / as
   ↓
reason + contrast
```

例如：

```text
want_to
   ↓
plan_to
   ↓
be_going_to
   ↓
was_going_to
```

例如：

```text
if_present
   ↓
if_past
   ↓
if_past_perfect
   ↓
would_have
```

数据模型可以采用：

```text
skills
skill_edges
sentence_patterns
```

`skill_edges` 至少支持：

```text
prerequisite
related
next
alternative
```

不要为了实现 Skill Graph 引入图数据库。

SQLite 足够。

---

# 7. Skill 解锁规则

新 Skill 不能随机进入练习。

Skill Graph 应根据：

```text
prerequisite mastery
current difficulty
recent performance
```

判断：

```text
eligible / locked
```

例如：

```text
if_present mastery = 0.90

→ if_past 可以进入探索池
```

但：

```text
if_present mastery = 0.35

→ 不应大量训练 if_past_perfect
```

允许少量 probe，但不能破坏训练梯度。

---

# 8. Memory Model

V2 必须把 Review Scheduler 升级为真正的记忆模型。

目标：

```text
错得越多
→ 越快复习

答对但不稳定
→ 中短间隔复习

稳定掌握
→ 逐步延长

长时间后仍正确
→ retention 提高
```

至少实现：

```text
immediate reinforcement
short-delay review
next-session review
long-delay review
```

建议初始策略类似：

```text
第一次失败
→ 3～5题后同能力变式复习

连续失败
→ 2～4题后再测

第一次成功
→ 10～20题后确认

短期连续成功
→ 下一个 session

长期成功
→ 1天
→ 3天
→ 7天
→ 14天
→ 30天
```

具体数值应配置化。

不要散落 hardcode。

---

# 9. 不允许机械连续重复

建立：

```text
Interleaving Policy
```

默认约束：

```text
同一 Pattern 默认不得连续出现两题

最近 5 题：
同一 Pattern 默认最多 2 次

同一 Scene：
避免连续过多

同一 Intent：
避免连续过多
```

对于 critical weak skill：

可以提高频率，但仍然不能：

```text
because
because
because
because
```

应该类似：

```text
because
could_you
past_tense
because variation
be_supposed_to
conditionals
because variation
```

---

# 10. Exact Repeat 与 Pattern Repeat 分离

明确区分：

```text
Exact Exercise Repeat
Pattern Repeat
Intent Repeat
Scene Repeat
```

默认：

### Exact Exercise Repeat

低频。

只用于：

```text
retention confirmation
特殊诊断
用户主动重做
```

### Pattern Repeat

主要训练机制。

同一 Pattern 应大量使用不同：

```text
scene
subject
verb
context
time
intent nuance
```

### Scene Repeat

用于判断真实场景迁移能力。

---

# 11. Difficulty Model

现有 `global_difficulty` 不够。

V2 至少形成：

```text
Global Difficulty
Pattern Difficulty
Scene Difficulty
```

例如：

```text
Global                 5.3

Past tense             6.0
Conditionals           4.7
Polite refusal         4.4

Daily conversation     5.9
Work meeting           5.1
Phone call             4.6
```

---

# 12. Difficulty 不等于 Pattern Level

同一个 Pattern 可以生成不同难度。

例如：

```text
because
```

可以：

```text
D2:
我没去，因为我病了。
```

```text
D4:
我昨天没有回复你，因为当时正在开会。
```

```text
D6:
我没有马上答应，是因为我不确定自己是否有足够的时间。
```

因此题目难度需要考虑：

```text
pattern complexity
vocabulary complexity
sentence length
clause count
tense complexity
information density
register
scene abstraction
number of semantic relations
```

不要求 V2 首次就建立复杂机器学习模型。

可以先用可解释公式。

但必须支持持续演进。

---

# 13. Productive Difficulty Zone

训练目标不是持续升级。

应该维持用户在一个有效训练区间。

建议目标成功率：

```text
70%～85%
```

根据历史：

```text
difficulty 4.5 → 92%
difficulty 5.0 → 84%
difficulty 5.5 → 73%
difficulty 6.0 → 46%
```

系统应估计：

```text
productive zone ≈ 5.1～5.6
```

下一题主要在这个区间附近。

---

# 14. Probe / Stretch Exercise

必须支持探索题。

不能永远只练已知能力。

新增：

```text
is_probe
```

例如：

```text
current stable difficulty = 5.1

occasionally probe:
5.5
5.8
```

Probe 失败时：

```text
不能像普通失败一样大幅降低 mastery
```

因为它本身就是探索上限。

Probe 成功则：

```text
提高对更高难度能力的置信度
```

---

# 15. Acquisition / Retention / Transfer

不要把 Mastery 理解成单一短期正确率。

至少概念上拆成：

```text
Acquisition
Retention
Transfer
```

### Acquisition

刚学会时是否能正确使用。

### Retention

间隔一段时间后是否还能主动说出来。

### Transfer

换场景、换语义以后是否还能正确使用。

例如：

```text
because

Acquisition  0.95
Retention    0.72
Transfer     0.81
```

最终可以计算综合：

```text
mastery
```

但不要丢掉三个分量。

---

# 16. Practice Policy

建立统一：

```text
Practice Policy
```

负责综合：

```text
Learner Model
Skill Graph
Memory Model
Difficulty Model
Recent History
```

决定下一题训练目标。

不要在不同 endpoint 中分别写选题 SQL。

所有：

```text
adaptive
weak
review
assessment
scene
```

模式都应复用统一 selection framework。

---

# 17. Candidate Pool

每次出题先建立 Candidate Pool。

候选来源例如：

```text
current ability
weak skills
scheduled review
new eligible skills
maintenance
probe
```

默认权重可类似：

```text
40% current productive zone
25% weak skills
15% scheduled review
10% maintenance
10% new/probe
```

但权重必须配置化。

不同练习模式可调整。

---

# 18. Candidate Scoring

不要只：

```sql
ORDER BY ABS(difficulty - ?)
LIMIT 1
```

建立 candidate score。

建议考虑：

```text
review urgency
weakness
difficulty fit
recency penalty
repeat penalty
scene diversity
intent diversity
skill graph readiness
probe budget
retention risk
```

例如：

```text
candidate_score =
    review_priority
  + weakness_weight
  + difficulty_fit
  + graph_readiness
  + diversity_bonus
  - recent_repeat_penalty
```

具体公式需要文档化。

---

# 19. AI Exercise Generator

当前如果仍存在：

```text
Pattern → fixed Chinese prompt
```

必须替换。

新的 AI Exercise Generator 输入：

```text
skill
pattern
scene
intent
target difficulty

selection reason

is_review
is_probe

lexical complexity
sentence complexity

recent exercises
recent contexts
avoid contexts
```

输出统一结构，例如：

```json
{
  "chinese_prompt": "我昨天没有回复你，因为当时正在开会。",
  "target_pattern": "because",
  "scene": "daily",
  "intent": "explain_reason",
  "estimated_difficulty": 4.2,
  "reference_answers": [
    "I didn't reply to you yesterday because I was in a meeting."
  ]
}
```

---

# 20. Reference Answers

允许 AI Generator 返回：

```text
reference_answers
```

但：

* 不得在用户答题前显示
* 不得要求用户逐字匹配
* 只能作为 Evaluator 辅助上下文
* 允许多种正确表达

---

# 21. Generator 必须考虑历史

生成 Prompt 中必须带：

```text
Recent exercises for this pattern

Recent semantic contexts

Recent scenes
```

要求：

```text
Generate a substantially different exercise.
Do not paraphrase recent exercises.
```

避免：

```text
我没去，因为不舒服。
我没有去，因为身体不好。
我没能去，因为感觉不太好。
```

这种伪变化。

---

# 22. Exercise Deduplication

必须建立两级去重。

## Level 1 — Exact

计算：

```text
normalized_chinese_hash
```

禁止近期完全相同。

## Level 2 — Semantic / Context

至少根据：

```text
pattern
scene
intent
key context metadata
recent exercises
```

做去重。

如果当前没有 embedding infrastructure，不要为了 V2 强行引入向量数据库。

可以先：

```text
LLM generation constraint
+
normalized text
+
metadata similarity
```

未来再升级 semantic similarity。

---

# 23. AI 生成失败 fallback

AI Generator 失败不能导致整个 Practice 不可用。

允许 fallback：

```text
curated seed exercise pool
```

但 fallback 必须：

* 至少一个 Pattern 多个题目
* 随机/受控选择
* recent exercise guard
* 标记 `generated_by = fallback`
* 不允许永远固定同一句

---

# 24. Assessment

初始 Assessment 不应完全自由随机。

保留：

```text
anchor exercises
```

或：

```text
anchor specifications
```

用于保证难度校准。

可以采用：

```text
Skill + Difficulty Anchor
→ AI 生成受控变体
```

或者使用少量人工验证 anchor questions。

Assessment 必须和普通 Practice 使用同一个 Learner Model。

不能评测后单独维护另一套状态。

---

# 25. Selection Explainability

每道题内部保存：

```text
selection_reason
```

例如：

```text
weak_pattern
scheduled_review
retention_check
new_skill
probe
maintenance
```

开发/debug 模式允许查看：

```text
Why this exercise?
```

普通用户 UI 不需要默认展示复杂算法。

但数据必须可追踪。

---

# 26. History 页面增强

History 页面至少可以追踪：

```text
Pattern
Scene
Intent
Difficulty
Selection Reason
Evaluation
Review / Probe status
```

用于真实验收算法。

---

# 27. Progress 模型增强

Progress 不应只展示累计正确率。

至少支持：

```text
Global Difficulty Trend

Pattern Mastery

Retention

Transfer

Weak Skills

Due Reviews

Recently Improved

Repeated Failure

Error Type Distribution
```

UI 仍保持简洁。

不要做成复杂 BI。

---

# 28. Error 与 Memory 联动

不同错误严重度应影响：

```text
memory_strength
review interval
mastery
```

例如：

```text
minor grammar mistake
```

不应等价于：

```text
meaning completely wrong
```

建议：

```text
minor
moderate
major
```

有不同 penalty。

---

# 29. Successful but Unnatural

以下情况要区分：

```text
Meaning correct
Grammar correct
Naturalness lower
```

不能简单视作“错题”。

例如：

```text
I didn't go, because I didn't feel well.
```

这种表达不能进入严重弱项队列。

可能：

```text
meaning: high
grammar: high
naturalness: slightly lower
```

Memory Model 和 Mastery Update 必须正确区分。

---

# 30. Transaction Safety

一次有效 Evaluation 后，更新必须事务化：

```text
Attempt
Evaluation
LearnerSkillState
SceneState
ErrorStats
ReviewSchedule
```

失败：

```text
rollback
```

LLM Evaluation 失败：

```text
不得更新 learner state
```

继续保持 V1 原有 integrity 原则。

---

# 31. Rebuild Learner State

建议提供内部工具：

```text
rebuild learner state from history
```

因为 Learner State 本质上应该可以从历史重新计算。

这对于：

```text
算法升级
数据库迁移
Debug
恢复损坏状态
```

非常重要。

可以提供：

```text
go run ./cmd/... rebuild-learning-state
```

或内部 service/test API。

---

# 32. Configuration

将核心策略参数集中配置：

```text
practice policy weights

recent pattern window

max pattern repeats

review intervals

target success range

probe ratio

weak skill threshold

mastery threshold

retention threshold
```

不要散落 magic numbers。

提供默认配置。

---

# 33. Tests

必须新增大量 deterministic test。

至少：

## Learner Model

```text
single success
single failure
minor error
major error
repeated success
repeated failure
long-gap success
long-gap failure
```

## Skill Graph

```text
locked prerequisite
eligible skill
new skill unlock
probe before full unlock
```

## Memory Model

```text
failure short review
success longer review
repeated success expansion
failure interval contraction
due review
not-due review
```

## Difficulty

```text
high success raises zone
low success lowers zone
probe failure limited penalty
pattern-specific difficulty
```

## Practice Policy

```text
candidate pool
weak skill preference
review urgency
recent repeat penalty
scene diversity
intent diversity
```

## Repetition

```text
same pattern not consecutive
weak pattern repeats after spacing
exact exercise blocked
same pattern variation allowed
```

## Generator

```text
LLM generation success
invalid output
timeout
fallback pool
recent context avoidance
```

## Integrity

```text
evaluation failure does not update state
transaction rollback
rebuild state produces same result
```

---

# 34. Real-use Scenario Test

新增至少一个模拟长期学习测试。

模拟：

```text
100～300 attempts
```

使用 Fake Evaluator。

构造一个用户：

```text
past tense strong
because strong
conditionals weak
polite refusal weak
```

验证经过训练后：

```text
weak patterns appear more often
but not continuously

review intervals change

strong patterns appear less frequently

new skills unlock

difficulty gradually adapts

exact exercise repetition remains low
```

输出统计结果供测试检查。

---

# 35. Regression Case

加入当前真实反馈：

当前系统出现：

```text
我没有去，因为我感觉不舒服。
```

连续重复。

V2 必须验证：

```text
同一 exact exercise 不得无控制连续出现
```

如果：

```text
because
```

为弱项：

允许在合理间隔后再次出现，但应优先生成不同语义，例如：

```text
我没参加会议，因为临时有事。

我没有开车，因为车坏了。

我们待在家里，因为外面一直在下雨。
```

---

# 36. Migration

当前已有真实 V1 数据库。

升级必须：

```text
schema migration
```

不能要求用户删除数据库。

旧：

```text
attempts
evaluations
pattern_mastery
scene_mastery
review_schedule
```

需要安全迁移。

历史数据尽量保留。

如果新增 Learner State，可在 migration 后从现有历史 rebuild。

---

# 37. Backward Compatibility

V2 启动后：

* V1 SQLite 可升级
* V1 History 可查看
* Provider 配置保留
* API Key 不丢失
* 旧 Attempt 不丢失

除非存在不可避免原因，不允许破坏数据。

---

# 38. 性能

本应用为单用户本地应用。

无需过度优化。

但：

```text
选下一题
```

不应扫描所有历史全文。

应该通过：

```text
indexes
aggregated learner state
recent history query
```

保持响应快速。

---

# 39. 文档

新增：

```text
docs/adaptive-learning-v2.md

docs/skill-graph.md

docs/memory-model.md

docs/difficulty-model.md

docs/practice-policy.md

docs/exercise-generation.md
```

文档必须说明实际公式和策略。

不要只写概念描述。

---

# 40. 实施阶段

建议：

```text
Phase 1
审计 V1 当前出题、Mastery、Review、History

Phase 2
schema migration
Learner Model
state rebuild

Phase 3
Skill Graph

Phase 4
Memory Model

Phase 5
Difficulty Model

Phase 6
Practice Policy + candidate scoring

Phase 7
AI Exercise Generator

Phase 8
anti-repeat + interleaving

Phase 9
Assessment integration

Phase 10
History / Progress integration

Phase 11
simulation tests

Phase 12
real Provider verification
```

每阶段：

```text
build
test
verify
commit-ready state
```

---

# 41. Codex 工作原则

开始前必须：

1. 阅读现有实现。
2. 明确 V1 当前真实行为。
3. 不根据任务文档假设功能已经存在。
4. 找出：

   * 当前选题逻辑
   * 当前固定题库/seed
   * 当前 Review Scheduler
   * 当前 Mastery update
   * 当前 global difficulty
   * 当前 Assessment
5. 给出简短 gap analysis。
6. 然后直接实施。

不要只输出设计报告。

---

# 42. 禁止事项

禁止：

```text
让 LLM 自己决定下一题
完全随机出题
错题下一题立即机械重复
永远禁止 Pattern 重复
只增加大量静态题库
用单一 global level 代替 Learner Model
删除历史重新开始
为了 Skill Graph 引入不必要图数据库
为了 semantic dedup 引入重型向量数据库
```

---

# 43. V2 验收标准

只有满足以下条件才能标记 READY：

```text
练习历史真实影响下一题

存在统一 Learner Model

存在 Skill Graph

存在 Memory Model

存在 Pattern/Scene-specific Difficulty

存在 Productive Difficulty Zone

存在 Practice Policy

存在 Candidate Scoring

存在 AI Exercise Generator

AI Generator 使用当前配置 Provider

同一 Pattern 可生成多种真实语境

错题会更快复习

弱项会提高出现频率

但弱项不会无控制连续出现

Exact Exercise 不会无控制重复

支持 Interleaving

支持 Probe

Probe 失败不会严重污染 mastery

支持 Acquisition / Retention / Transfer

长期正确会延长复习间隔

长期失败会缩短复习间隔

新 Skill 按 Graph 解锁

V1 数据可迁移

Learner State 可从历史 rebuild

Evaluation failure 不污染状态

模拟长期学习测试通过

真实 Provider 出题验证通过

真实 Provider 判题验证通过

go test ./... PASS

go build PASS
```

---

# 44. 最终真实验收

完成后使用真实应用完成至少：

```text
30～50道连续练习
```

重点观察：

```text
1. 是否明显减少完全相同题目的重复

2. 是否仍能看到弱 Pattern 的受控重复

3. 同 Pattern 是否会换场景和语义

4. 连续错误后是否缩短复习间隔

5. 连续成功后是否拉长间隔

6. 难度是否会逐渐变化

7. 是否有少量合理 Probe

8. 新技能是否按前置条件进入

9. History 是否能解释每道题为什么出现
```

不能只依赖单元测试宣布 READY。

---

# 45. 完成报告

最终输出：

```text
# Adaptive Learning Engine V2

Current V1 Gap Analysis:

Architecture Changes:

Database Schema Version:

Learner Model:

Skill Graph:

Memory Model:

Difficulty Model:

Practice Policy:

Candidate Scoring:

Exercise Generator:

Anti-repeat / Interleaving:

Probe Strategy:

Assessment Integration:

Migration Result:

State Rebuild Result:

Simulation Test:

Real-use Test:

Real Provider Exercise Generation:

Real Provider Evaluation:

Tests:

Build:

Known Limitations:

Final Status:
READY / PARTIAL / BLOCKED
```

如果为 READY，必须同时给出：

```text
V2 对比 V1：

完全重复题比例
Pattern 重复比例
弱项出现频率
平均重复间隔
难度变化范围
Review 命中情况
Probe 数量
```

目标不是追求某个固定数字，而是证明新的 Adaptive Learning Engine 确实在工作。

---

# 最终产品原则

本阶段最重要的判断标准不是：

> AI 能不能生成很多不同的英语题。

而是：

> 系统是否能根据一个人的长期练习历史，知道现在最值得练什么、应该多难、什么时候应该再次出现，以及如何用新的上下文验证这个能力是否真正掌握。

最终应形成：

```text
History
   ↓
Learner Model
   ↓
Skill + Memory + Difficulty
   ↓
Practice Policy
   ↓
AI Exercise
   ↓
Evaluation
   ↓
History
```

这个闭环必须成为 English Practice AI 后续所有训练功能的核心。
