# Data model

SQLite schema version 2 includes `user_profile`, `scenes`, `communication_intents`, `sentence_patterns`, `exercises`, `sessions`, `attempts`, `evaluations`, `pattern_mastery`, `scene_mastery`, `error_stats`, `review_schedule`, `llm_providers`, and `llm_task_configs`. Version 2 adds `attempts.evaluation_diagnostics_json` for safe failure-stage diagnostics.

`attempts` keeps the learner answer, timestamp, provider, model, prompt version, and evaluation status. `evaluations` keeps the complete validated structured result rather than only a score. API keys are stored only in the local database and are masked on reads; a future OS secret-store adapter can replace this storage boundary.
