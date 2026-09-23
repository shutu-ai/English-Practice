# CEFR-aligned curriculum foundation

English Practice uses CEFR as an external alignment reference, not as a claim
that an internal exercise or D-level is officially equivalent to a CEFR level.
The Council of Europe describes CEFR through communicative descriptors and
learner action; English-specific grammar and vocabulary placement is therefore
an internal, evidence-informed mapping. The current map is version
`cefr-foundation-v1`.

## Two separate scales

`D1-D8` is English Practice's internal productive curriculum scale. It answers
“should this capability be taught here?”. Continuous numeric difficulty still
answers “how challenging is this particular exercise for this learner?”. A
daily scene can contain D1, D4, or D7 work; scene, intent, pattern, curriculum
level, and difficulty are separate dimensions.

The initial working anchors are deliberately cautious:

| Internal level | Alignment anchor | Productive focus |
| --- | --- | --- |
| D1 | Pre-A1 / early A1 | identity, possession, simple state, likes, wants, basic ability |
| D2 | A1 | simple questions, routines, family, home, time |
| D3 | upper A1 / A1+ | past events, plans, reasons, comparisons |
| D4 | early A2 | polite requests, permission, advice, obligation, arrangements |
| D5 | A2 | transactions, choices, short narratives, feelings, opinions |
| D6 | early B1 | connected explanations, work communication, clarification |
| D7 | B1 / B1+ | negotiation, hedging, concession, nuanced disagreement |
| D8 | approaching B2 productive expression | structured argument and mixed-time hypothetical reasoning |

These are curriculum hypotheses anchored to CEFR descriptors and English-
specific sources. They are not official CEFR grammar boundaries, and the UI
uses wording such as “D4 · early-A2 aligned”, never “D4 equals A2”.

## Machine-readable metadata

Every built-in pattern has an entry in `curriculum_patterns` with:

- `app_level_min`, `cefr_anchor`, `grammar_family`, and communication
  functions;
- prerequisite skill IDs and a rationale;
- productive complexity, typical contexts, instruction complexity, and
  confidence (`high`, `medium`, or `low`);
- allowed grammar and structures that are not yet targetable.

The metadata is stored separately from `sentence_patterns.difficulty`. This
keeps historical attempts and pattern mastery intact when a curriculum review
changes a pattern's teaching boundary.

## Prerequisite graph

The progression graph is a DAG. It validates unknown IDs, cycles, orphaned
advanced nodes, reachability from foundation skills, and the rule that a
prerequisite cannot be introduced after its dependent skill. A typical route
is:

`basic statement -> simple question -> can request -> could request -> would request -> would you mind + gerund`

`Would you mind ...?` is therefore D4 / early-A2 aligned and is never an
eligible D1 target, even when the scene is an everyday home or phone request.

## Generation boundary

Selection applies constraints in this order:

1. curriculum eligibility;
2. prerequisite readiness;
3. adaptive numeric-difficulty fit;
4. review, weakness, diversity, and probe scoring.

The generator receives a small `CurriculumEnvelope` containing the current
level, allowed skill IDs, grammar, intents, complexity bounds, and vocabulary
guidance. It can choose a natural Chinese task and reference answer, but it
cannot move a pattern to another level. `CurriculumComplianceValidator`
separates structural checks (target and prerequisites) from semantic checks
(instruction complexity and obvious advanced leakage); invalid provider output
falls back through the existing bounded reliability path.

## Assessment and acceptance

The 12-question assessment now uses stable anchors across foundation, A1, A2,
B1, and higher expression. Beginners can skip repeated D1/D2 probing after
foundation evidence while still retaining anchor evidence. The independent
judge must estimate CEFR band, productive complexity, grammar features, and
communication function without seeing the selected D-level or expected label.

Deterministic acceptance reports include curriculum coverage, pattern movement,
graph validity, prerequisite violations, instruction complexity, advanced
leakage, level alignment, and the existing numeric difficulty metrics. Live
provider runs are bounded, use an isolated acceptance database, and must not
write credentials or raw provider responses to logs.

The CLI entry point is:

```text
go run . curriculum-live --run-a-samples 5 --run-b-samples 20
```

Run A covers D1/D2/D4/D6/D8 before Run B covers D1-D8. Run B is blocked when
Run A cannot complete, so a provider outage is reported as `PARTIAL` rather
than being converted into a passing result.

## Source notes

- Council of Europe, *Common European Framework of Reference for Languages:
  Learning, Teaching, Assessment — Companion volume* (2020):
  https://www.coe.int/en/web/common-european-framework-reference-languages/cefr-companion-volume-and-its-language-versions
- Council of Europe, CEFR descriptors and level descriptions:
  https://www.coe.int/en/web/common-european-framework-reference-languages/cefr-descriptors
- English Profile, *Introducing the English Profile*:
  https://www.englishprofile.org/images/pdf/theenglishprofilebooklet.pdf
- Cambridge English language specifications and handbooks are used as
  English-specific cross-checks. The project stores derived metadata and
  source names only; it does not reproduce copyrighted course or database
  content.

The product is **CEFR-aligned**, not CEFR-certified.
