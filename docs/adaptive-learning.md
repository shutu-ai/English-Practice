# Adaptive learning

The profile stores a continuous `global_difficulty` from 1 to 8. Seed patterns cover that range. Selection uses a window around the current difficulty and can be extended with scene, weak-area, review, and recent-exercise filters.

Pattern mastery combines recent pattern score, long-term accuracy, consecutive correct answers, and error severity. Scene mastery is tracked independently from pattern mastery. Errors are counted by taxonomy (`meaning`, `tense`, `article`, `preposition`, `word_order`, `modal`, `condition`, `agreement`, `word_choice`, `missing_information`, `extra_information`, `unnatural_expression`, `target_pattern_missing`, `register`, `other`).

Review due time is policy-owned: failed or severe attempts return in hours; successful attempts expand from one day toward thirty days. The schedule uses a stable pattern key so one pattern has one current review item.
