# English Practice AI V1 — 开发任务

请设计并实现一个用于训练用户英文主动表达能力的个人 AI 应用。

本项目不是普通翻译工具，也不是背单词、选择题或填空应用。

核心训练方式固定为：

**给出中文表达意图 → 用户独立输入完整英文句子 → AI 判断语义、语法、自然度和目标句型掌握情况 → 系统根据长期练习历史自动调整训练内容和难度。**

目标是长期训练用户的英文主动输出能力，并逐步建立个人化的句型、场景和错误能力画像。

---

# 1. 核心产品原则

必须遵守以下原则。

## 1.1 主动输出优先

训练过程始终采用：

```text
中文题目
↓
用户独立输入完整英文句子
↓
AI评估
```

禁止将训练退化为：

* 选择题
* 填空题
* 单词提示
* 首字母提示
* 关键词提示
* 自动补全答案
* 用户答错后逐词提示直到答出

即使用户回答错误，也不要通过降低任务难度让用户“拼出”正确答案。

---

## 1.2 错误反馈

用户提交答案后，可以显示：

* 是否正确
* 是否基本正确
* 语义问题
* 语法问题
* 是否自然
* 更自然的表达
* 推荐参考句
* 简短中文解释

例如：

```text
题目：
我本来想昨天给你打电话的。

你的回答：
I wanted to call you yesterday.

结果：
△ 基本正确

问题：
这句话表达了“我想打电话”，但没有准确体现“本来打算”的语义。

更准确：
I was going to call you yesterday.
```

不要要求用户立即机械抄写正确答案。

错误内容应进入后续自适应复习。

---

# 2. 产品定位

V1 为：

```text
单用户
本地优先
Web App
长期使用
可保存完整学习历史
支持自定义LLM Provider
```

暂时不需要：

* 多租户
* 复杂账户系统
* SaaS计费
* 社交功能
* 排行榜
* 教师后台
* 单词背诵
* 发音评分
* 语音输入

但架构不要阻止未来增加语音能力。

---

# 3. 推荐技术栈

如果当前仓库不存在既有技术栈，则使用：

```text
Backend:
Go

Frontend:
Vue 3
TypeScript
Vite

Database:
SQLite

Architecture:
Frontend + Go API + SQLite

Deployment:
单个本地应用优先
```

尽量保证未来可以打包为：

```text
english-practice.exe
```

启动后自动或手动访问：

```text
http://localhost:<port>
```

如果仓库已经存在成熟技术栈，则优先复用现有方案，不为了满足上述推荐而无意义重构。

---

# 4. 核心功能模块

至少实现以下模块：

```text
Adaptive Assessment
Practice Engine
Exercise Generator
Answer Evaluator
Mastery Engine
Review Scheduler
Learning Profile
Practice History
Progress Dashboard
LLM Provider Management
```

---

# 5. 首次自适应评测

首次使用不要从最低难度机械练起。

实现一个短时间 Adaptive Assessment。

目标不是测试 CEFR A1/B1/B2，而是快速估计用户当前英文主动表达能力区间。

建议：

```text
10～15题
```

但题数不是硬编码要求。

评测过程：

```text
较简单题
↓
答对
↓
快速增加复杂度
↓
继续答对
↓
继续探索
↓
开始出现明显错误
↓
在该区间附近增加少量测试
↓
建立初始能力画像
```

例如：

```text
我今天很忙。
↓
我明天可能会晚一点到。
↓
如果明天下雨，我们就不去了。
↓
我本来打算昨天给他打电话，但后来忘了。
↓
如果我早点知道，我就不会答应了。
```

不要每个 Level 连续测试很多题。

需要支持快速跳级。

评测 UI 不要强调“考试”。

推荐显示：

```text
正在了解你的表达水平
6 / 12
```

评测结束后建立初始：

```text
Global Difficulty

Pattern Mastery

Scene Mastery

Known Weak Areas
```

首次评测结果不是永久等级。

之后训练必须继续动态修正。

---

# 6. 难度模型

不要把用户简单固定为：

```text
L1
L2
L3
...
```

Level 主要描述“题目复杂度”，而不是用户身份。

可以内部定义约 8 个复杂度层次，例如：

```text
L1
基本主谓宾

L2
时间、地点、频率等修饰

L3
can / should / might / could 等情态表达

L4
because / if / when / although 等连接关系

L5
请求、建议、解释、拒绝、确认等功能表达

L6
完成时、从句、假设、委婉表达

L7
多句关系、复杂上下文、语气差异

L8
开放式真实场景表达
```

具体规则可以调整，但必须形成结构化 difficulty metadata。

用户应有连续值，例如：

```text
Global Difficulty = 5.4
```

而不是只能：

```text
User Level = L5
```

---

# 7. Sentence Pattern 为核心学习对象

不要以 Exercise/题目作为长期学习系统的核心。

核心对象应包括：

```text
SentencePattern
CommunicationIntent
Scene
```

例如：

```text
CommunicationIntent:
委婉拒绝

SentencePatterns:
I'd love to, but...
I'm afraid I can't...
I don't think I'll be able to...
Maybe another time.

Scenes:
friends
work
invitation
appointment
```

一个 Sentence Pattern 应允许生成很多语义变式。

例如某用户：

```text
I don't think I'll be able to...
```

掌握较弱。

系统可以依次生成：

```text
恐怕我今天没办法参加会议。

我可能今晚没办法过去。

我觉得我明天没办法完成。

恐怕这周我抽不出时间。
```

目的是训练迁移能力，而不是记忆原题。

---

# 8. Mastery Model

每个用户至少维护：

```text
Pattern Mastery

Scene Mastery

Error Type Statistics
```

Sentence Pattern 示例：

```text
pattern:
be able to

attempts:
18

correct:
14

recent_accuracy:
0.83

long_term_accuracy:
0.78

consecutive_correct:
4

last_practiced:
timestamp

mastery:
0.81
```

Mastery 不应只由累计正确率决定。

至少考虑：

```text
近期正确率
历史正确率
连续正确
错误严重程度
最后练习时间
不同语境中的成功情况
题目难度
```

算法不要求一开始非常复杂，但必须封装在独立 Mastery Engine 中，禁止把算法散落到 UI 或 Controller 中。

---

# 9. 自适应选题

Practice Engine 每次负责选择：

```text
Scene
Intent
Pattern
Difficulty
```

然后 Exercise Generator 生成具体中文题目。

V1 可采用类似权重：

```text
45% 当前能力区间
25% 薄弱句型
15% 到期复习
10% 略高难度探索
5% 随机巩固
```

这些数字应配置化，不要硬编码到业务流程。

选题必须避免：

* 短时间内重复同一句
* 连续大量同一 Pattern
* 连续大量同一 Scene
* 用户明显不会时无限重复
* 用户已经完全掌握仍高频出现

目标难度应长期保持在大约：

```text
70%～85% 成功率
```

不要为了“升级”而一直变难。

---

# 10. 错题处理与变式复习

用户答错后：

```text
原题
→ 给反馈和参考表达
→ 不立即再次问完全相同问题
→ 若干题之后生成同 Pattern 不同语义题
→ 未来再次间隔复习
```

例如：

```text
第一次：

我本来想昨天给你打电话的。

目标：
was/were going to
```

之后不要简单再次显示同一句。

应该生成：

```text
我原本打算今天早点走的。

我本来准备周末去看他的。

我们原本计划上午出发。
```

必须区分：

```text
Exercise memory
```

和：

```text
Pattern mastery
```

不要把“记住某个答案”误判为掌握句型。

---

# 11. Review Scheduler

建立独立复习调度模块。

根据：

```text
mastery
recent errors
error severity
last practiced
repeated failure
long-term retention
```

确定复习优先级。

至少支持：

```text
近期错题变式
弱 Pattern
久未练习 Pattern
已掌握内容长期复查
```

V1 不必实现复杂 SM-2，但设计上应允许以后替换复习算法。

---

# 12. Scene 模型

提供常见日常交流场景。

例如：

```text
Daily Conversation
Friends
Family
Shopping
Restaurant
Travel
Hotel
Airport
Phone Call
Appointment
Work
Meeting
Problem Explanation
Request
Suggestion
Disagreement
Refusal
Small Talk
```

Scene 不应只是 UI 标签。

需要形成 Scene Mastery。

例如：

```text
Daily Conversation    0.91
Restaurant            0.88
Shopping              0.83
Phone Call            0.71
Work Meeting          0.69
Problem Explanation   0.62
Polite Refusal        0.55
```

后续练习应根据场景弱点调整。

---

# 13. Error Taxonomy

不要只记录：

```text
correct / incorrect
```

建立结构化错误分类。

V1 至少支持：

```text
meaning
tense
article
preposition
word_order
modal
condition
agreement
word_choice
missing_information
extra_information
unnatural_expression
target_pattern_missing
register
other
```

每次 Evaluation 可以返回多个错误。

错误需要保存：

```text
type
severity
explanation
```

severity 至少：

```text
minor
moderate
major
```

---

# 14. LLM 架构原则

本项目不能绑定单一 LLM 厂商。

必须设计统一的：

```text
LLM Client abstraction
```

推荐：

```go
type LLMClient interface {
    Chat(
        ctx context.Context,
        req ChatRequest,
    ) (*ChatResponse, error)
}
```

LLM Provider 只负责：

```text
messages
→ provider API
→ response
```

不要把：

```text
GenerateExercise
EvaluateAnswer
ExplainError
```

直接实现到 Provider 内。

业务分层应类似：

```text
Practice Engine
      │
      ▼
English AI Services
      │
      ├─ ExerciseGenerator
      ├─ AnswerEvaluator
      └─ ErrorExplainer
              │
              ▼
          LLM Client
              │
     ┌────────┼────────┐
     ▼        ▼        ▼
 OpenAI   Compatible  Ollama
```

---

# 15. V1 LLM Provider

首版至少实现：

```text
OpenAI
OpenAI-Compatible
Ollama
```

预留但不要求首版完成：

```text
Anthropic
Gemini
```

OpenAI-Compatible Provider 必须允许：

```text
Base URL
API Key
Model
Timeout
Temperature
Max Tokens
```

Ollama 至少支持：

```text
Base URL
Model
```

默认例如：

```text
http://127.0.0.1:11434
```

Provider 必须支持 Connection Test。

---

# 16. Provider 配置

设置页面需要支持：

```text
Provider Name

Provider Type

Base URL

API Key

Model

Timeout

Temperature

Max Tokens

Enabled
```

API Key 必须安全存储。

至少不能：

* 写入日志
* 返回给普通 API Response
* 明文显示完整值
* 在错误页面暴露

如果本地应用没有 OS Secret Store，可先采用合理的本地保护方案，但代码架构需要允许未来升级。

---

# 17. 支持不同任务使用不同模型

从数据结构和业务设计上支持：

```text
exercise_generation

answer_evaluation

error_explanation
```

分别绑定不同模型。

例如：

```text
Exercise Generator
→ cheap / local model

Answer Evaluator
→ stronger model

Error Explainer
→ medium model
```

V1 UI 可以先默认共用一个 Provider，但数据结构不要锁死。

建议：

```text
LLMTaskConfig

task_type
provider_id
model
temperature
max_tokens
```

---

# 18. Answer Evaluator

这是整个系统中最关键的 AI 功能。

不要只判断：

```text
字符串是否相同
```

例如中文：

```text
我今天可能会晚一点到。
```

下面都可能是合理表达：

```text
I might be a little late today.

I may arrive a bit late today.

There's a chance I'll be a little late today.
```

Evaluator 至少评估：

```text
Meaning Accuracy

Grammar

Naturalness

Target Pattern Mastery
```

建议统一归一化为：

```text
0.0 ～ 1.0
```

同时返回：

```text
verdict
errors
suggested_answer
explanation_zh
```

verdict 建议：

```text
correct
mostly_correct
needs_improvement
incorrect
```

---

# 19. Structured Output

所有 LLM 业务输出必须有统一结构。

例如 Answer Evaluation：

```json
{
  "verdict": "mostly_correct",
  "meaning_score": 0.91,
  "grammar_score": 0.82,
  "naturalness_score": 0.72,
  "pattern_score": 0.65,
  "errors": [
    {
      "type": "word_choice",
      "severity": "minor",
      "explanation": "..."
    }
  ],
  "suggested_answer": "I was going to call you yesterday.",
  "explanation_zh": "..."
}
```

不要让不同 Provider 返回不同业务模型。

模型输出必须经过：

```text
parse
↓
schema validation
↓
business validation
```

然后才允许进入 Mastery Engine。

---

# 20. LLM 输出失败保护

这是硬性要求。

如果出现：

```text
invalid JSON
schema validation failure
timeout
provider error
model hallucination
missing required fields
score out of range
```

则：

```text
本次AI评估失败
```

但：

**禁止更新 Pattern Mastery、Scene Mastery 或长期学习画像。**

可以：

```text
retry once
```

如果仍失败：

```text
显示可恢复错误
允许用户重新提交
```

不能用损坏的 LLM 输出污染学习数据。

---

# 21. Exercise Generator

Exercise Generator 输入至少包含：

```text
scene
communication_intent
target_pattern
difficulty
avoid_recent_exercises
```

输出：

```text
chinese_prompt
target_pattern
acceptable_intents
difficulty
metadata
```

中文题目必须：

* 自然
* 适合日常交流
* 不故意书面化
* 不为了语法测试制造奇怪句子
* 尽量具有真实生活场景

---

# 22. Prompt 管理

不要把 Prompt 大量散落在 Go 源码中。

建议：

```text
prompts/
    exercise_generator.*
    answer_evaluator.*
    error_explainer.*
```

或者建立专用 Prompt Registry。

Prompt 需要版本化。

保存 Attempt 时记录：

```text
prompt_version
model
provider
```

以便未来分析模型变化是否影响评分。

---

# 23. 数据模型

至少设计以下核心实体：

```text
UserProfile

Scene

CommunicationIntent

SentencePattern

Exercise

PracticeSession

Attempt

Evaluation

PatternMastery

SceneMastery

ReviewSchedule

LLMProvider

LLMTaskConfig
```

Attempt 至少记录：

```text
id
session_id
exercise_id
user_answer
submitted_at
provider
model
prompt_version
evaluation_status
```

Evaluation 保存完整结构化评估结果。

不要只保存最终分数。

---

# 24. Practice History

提供 History 页面。

至少支持查看：

```text
日期
中文题目
用户英文回答
结果
参考表达
错误类型
目标 Pattern
Scene
```

可以查看单次 Session。

不要只显示：

```text
今天50题
正确率82%
```

需要允许用户回看实际表达。

---

# 25. Progress Dashboard

Progress 页面至少展示：

```text
近期训练数量

近期成功率

Global Difficulty

弱 Pattern

强 Pattern

Scene Mastery

常见错误类型

最近变化趋势
```

例如：

```text
Conditionals          61%
Would have            42%
Be supposed to        73%
Wish + past           38%
```

以及：

```text
Preposition           Weak
Conditionals          Weak
Polite Refusal        Weak
Modal Expressions     Strong
```

不要把 Dashboard 做成复杂 BI。

重点是帮助用户知道：

```text
我到底哪些英文表达不会。
```

---

# 26. Practice UI

Practice 页面尽量简洁。

默认状态：

```text
────────────────────────

我本来想昨天给你打电话的。

[ 输入完整英文句子                     ]

                         Submit

────────────────────────
```

提交后：

```text
△ 基本正确

你的表达：
I wanted to call you yesterday.

更准确：
I was going to call you yesterday.

说明：
你的表达能够理解，但没有准确表达
“本来打算”的含义。

[ 下一题 ]
```

不要在主界面展示：

```text
temperature
model token
prompt
LLM trace
mastery internal formula
```

这些只属于设置或开发信息。

---

# 27. 页面结构

V1 建议：

```text
Practice

Review

Scenes

Progress

History

Settings
```

首页可以直接进入 Practice。

不要先进入复杂 Dashboard。

---

# 28. Session

允许用户开始一次练习 Session。

可以选择：

```text
Adaptive Practice

Weak Areas

Review

Scene Practice
```

V1 可以不要求用户设置练习题数。

如果提供：

```text
10 / 20 / 30题
```

也可以。

---

# 29. 数据完整性

学习状态更新必须事务化。

逻辑类似：

```text
Save Attempt
↓
Save validated Evaluation
↓
Update Pattern Mastery
↓
Update Scene Mastery
↓
Update Review Schedule
↓
Commit
```

如果中间失败：

```text
rollback
```

避免出现：

```text
Attempt存在
但Mastery更新了一半
```

---

# 30. 测试要求

必须提供自动化测试。

至少覆盖：

```text
Mastery update

Adaptive difficulty

Assessment fast-level-up

Review scheduling

Exercise deduplication

Provider configuration

OpenAI-compatible mock

Ollama mock

Invalid JSON

Schema violation

Provider timeout

Provider 4xx / 5xx

LLM evaluation failure must not update mastery

SQLite persistence

Practice history
```

LLM 测试不得依赖真实 API Key。

使用 mock/fake provider。

---

# 31. Seed Data

提供基础 Seed Data。

至少包含：

```text
Scenes

Communication Intents

Sentence Patterns

Difficulty Metadata
```

不要要求第一次启动必须让 LLM 自动生成整个课程体系。

Sentence Pattern 初始库需要有合理人工定义的基础结构。

不要求一次做几千条。

优先质量。

---

# 32. 可观测性

日志至少记录：

```text
provider
model
latency
request success/failure
evaluation validation result
```

但禁止记录：

```text
API Key
完整Authorization Header
其他Secret
```

对用户英文回答的日志应尽量避免无必要全文重复记录。

数据已经存数据库，不需要到处打印。

---

# 33. Repository 结构参考

可以参考：

```text
english-practice/
├── cmd/
│   └── server/
│
├── internal/
│   ├── assessment/
│   ├── practice/
│   ├── mastery/
│   ├── review/
│   ├── exercise/
│   ├── evaluation/
│   ├── learningprofile/
│   ├── llm/
│   │   ├── client.go
│   │   ├── openai/
│   │   ├── compatible/
│   │   └── ollama/
│   ├── storage/
│   └── api/
│
├── prompts/
│
├── web/
│
├── migrations/
│
├── data/
│
└── docs/
```

这是参考结构，不要求机械照搬。

优先保证职责清晰。

---

# 34. 架构硬约束

必须遵守：

```text
LLM负责语言理解
程序负责学习策略
```

LLM 可以决定：

```text
意思是否准确
语法是否正确
是否自然
目标句型是否正确
错误属于什么类型
```

LLM 不应该直接决定：

```text
用户是否升级
下一题难度是多少
Mastery是多少
多久以后复习
下一题选哪个Scene
```

这些必须由确定性的 Application Logic 控制。

---

# 35. 非目标

V1 不做：

```text
多人系统

社交

在线支付

ChatGPT账号登录

云同步

发音识别

语音评分

单词记忆

考试题库

CEFR正式认证

教师管理后台

Gamification复杂系统
```

不要因为觉得“以后可能需要”而提前实现大量无关框架。

---

# 36. 文档

至少提供：

```text
README.md

docs/architecture.md

docs/adaptive-learning.md

docs/llm-provider.md

docs/data-model.md
```

README 说明：

```text
如何启动

如何构建

如何配置OpenAI

如何配置OpenAI-Compatible Provider

如何配置Ollama

如何运行测试

数据保存在哪里

如何备份学习历史
```

---

# 37. 实施方式

不要试图一次完成全部功能后再测试。

按阶段实施。

建议顺序：

```text
Phase 1
项目骨架
SQLite schema
Seed data
基础 API
基础 Vue UI

Phase 2
LLM abstraction
OpenAI
OpenAI-Compatible
Ollama
Connection Test
Structured Output

Phase 3
Exercise Generator
Answer Evaluator
基础 Practice Flow

Phase 4
Pattern Mastery
Scene Mastery
Error Taxonomy

Phase 5
Adaptive Assessment
Practice Selection
Difficulty Adjustment

Phase 6
Review Scheduler
Weak Area Practice

Phase 7
History
Progress Dashboard

Phase 8
完整测试
异常路径
文档
UX Polish
```

每一个 Phase 完成后：

```text
build
test
verify
```

不要在前一个阶段明显失败时继续堆功能。

---

# 38. Codex 工作要求

开始前：

1. 检查当前仓库结构。
2. 如果已有代码，先理解现有架构，不要破坏已有能力。
3. 如果是空仓库，则初始化项目。
4. 写出简短实施计划。
5. 然后直接开始实现。

不要只输出设计文档而不实现代码。

遇到一般性的设计细节不要停下来询问。

按照本任务给出的产品原则自行做合理选择。

只有遇到：

```text
会导致重大架构方向变化
或
存在不可恢复的数据风险
```

才需要暂停。

---

# 39. V1 完成定义

只有满足以下条件，才能认为 V1 可用：

```text
首次启动成功

能够完成自适应初始评测

能够生成中文练习题

用户能够输入完整英文句子

能够调用配置的LLM Provider判题

能够返回Meaning / Grammar / Naturalness / Pattern评价

能够正确处理多个合理英文答案

LLM异常不会污染Mastery

能够保存练习历史

能够形成Pattern Mastery

能够形成Scene Mastery

能够识别弱项

能够针对弱项生成后续训练

能够安排复习

难度可以动态变化

可以配置OpenAI

可以配置OpenAI-Compatible Provider

可以配置Ollama

可以测试Provider连接

重启程序后所有历史和能力画像仍存在

核心自动化测试通过

README和架构文档完整
```

---

# 40. 最终验收输出

完成后请给出：

```text
1. 实现结果摘要

2. 当前架构

3. 已完成模块

4. 关键文件位置

5. Database schema/version

6. LLM Provider支持情况

7. Adaptive Assessment实现方式

8. Mastery算法说明

9. Review Scheduler说明

10. 自动化测试结果

11. build结果

12. 实际启动验证结果

13. 已知限制

14. V1完成状态：
    READY
    PARTIAL
    BLOCKED
```

如果为：

```text
PARTIAL
```

必须明确列出哪些 V1 Completion Criteria 尚未满足。

不要因为 UI 能运行就标记为 READY。

核心验收重点是：

> 系统是否真的能够根据用户长期练习历史，逐渐发现“不熟悉的表达”，并自动提供针对性训练，而不是只做一个由 LLM 随机出题和判题的翻译 Demo。
