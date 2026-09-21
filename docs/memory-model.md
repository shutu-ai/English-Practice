# Memory Model

Each pattern state tracks acquisition, retention, transfer, memory strength, stability, and the next review time. Review intervals are deterministic:

* failure: 10 minutes, or 2 hours after repeated failure;
* first success: 1 day;
* two successes: 3 days;
* three to four successes: 7 days;
* five or more successes: 30 days;
* a stable delayed review may use 7 days.

This provides immediate reinforcement, short delay, next-session, and long-delay reviews while retaining a single source of truth in `learner_skill_state`.

The interval values are stored in `adaptive_config` (`failure_short_minutes`, `failure_repeat_hours`, `success_first_days`, `success_two_days`, `success_three_days`, `success_five_days`, and `success_long_days`) and can be changed through `/api/adaptive-config`.
