# Skill Graph

`skills` contains learner capabilities, `skill_edges` contains `prerequisite`, `related`, `next`, and `alternative` relationships, and `pattern_skills` maps sentence patterns to capabilities. SQLite is sufficient; no graph database is required.

The graph is used as an eligibility signal. A new skill is selected only when its prerequisite mastery reaches the configured threshold, with assessment and probe modes allowed to inspect a locked skill in a controlled way.

