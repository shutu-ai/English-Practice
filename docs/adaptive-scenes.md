# Adaptive Scenes

V2.3 adds Scenes as a constraint on the existing adaptive learning engine. A
scene answers “where is this practice happening?”; an intent answers “what
communication goal is being completed?”; a pattern answers “which language
structure expresses it?”. Scene selection never replaces the learner model,
memory scheduler, review/probe policy, or V2.2 difficulty controller.

## Hierarchy and mappings

The seeded catalog has 13 manageable root scenes and 66 subscenes. The
`scenes.parent_id` hierarchy is complemented by three many-to-many mapping
tables: `scene_pattern_map`, `scene_intent_map`, and `scene_skill_map`.

The seed operation uses `INSERT OR IGNORE` and updates canonical metadata, so
it is safe to run repeatedly. Existing V2.2 scene ids and attempts remain
queryable; legacy aliases are migrated to their canonical root where a direct
equivalent exists.

## Scene practice

`POST /api/practice/next` accepts `scene_id`, `subscene_id`, and `session_id`.
The selector first constrains candidates to the selected scene (and its
descendants for a root selection), then runs the normal weak/review/current/
probe scoring and difficulty stabilization. A subscene shortage relaxes in
order to its parent, a related category, and finally a global fallback. The
exercise response and `decision_trace` record `scope_relaxed` and
`relaxation_reason`; exact-repeat, pattern spacing, and intent spacing remain
owned by the adaptive selector.

Scene sessions persist `scene_id`, `subscene_id`, and `practice_scope` in the
existing `sessions` table. Exercises persist their subscene and the complete
selection trace. Fallback prompts still inherit the selected scene context.

## Mastery, progress, and transfer

`scene_mastery` stores recent performance, evidence count, confidence,
effective difficulty, pattern/intent coverage, transfer, state, and last
practice time. Zero evidence is `UNKNOWN`, not `WEAK`. Scene detail and the
Scenes page use these fields to show strong areas, needs-practice areas,
subscene progress, and recent activity without exposing implementation-only
metrics.

Successful use of a pattern across distinct scenes and intents updates the
existing learner transfer value and records `scene_transfer_events`. Repeating
one scene does not count as new transfer coverage.

## APIs

- `GET /api/scenes` — enabled root scenes with subscene summaries.
- `GET /api/scenes/{id}` — scene detail, mastery, and subscenes.
- `GET /api/scenes/{id}/progress` — progress payload for a scene.
- `GET /api/calibration/scenes` — scene diagnostics.
- `GET /api/calibration/report` — includes scene coverage and mastery
  distribution in addition to the existing V2.2 calibration report.
- `GET /api/history?scene_id=...&subscene_id=...` — scene-aware history
  filtering.
- `POST /api/feedback` with `feedback_type=scene_mismatch` — records a
  context mismatch without changing mastery automatically.

## UI

The Vue app exposes Scenes as a primary entry point, scene cards with mastery,
recent status, difficulty/evidence hints, scene detail with subscenes and
coverage, scene-aware practice context, progress rows, and history filters.
Pronunciation playback continues to use the local SpeechSynthesis service and
is not part of learning-state updates.

## Scope

V2.3 does not implement AI role-play, multi-turn NPC dialogue, speech
recognition, pronunciation scoring, avatars, or scene media. Real-use
validation still requires human practice across multiple scenes; API and
automated tests establish code readiness but do not replace that evidence.
