# Difficulty Model

Global, pattern, and scene difficulty are stored independently. Candidate fit is `1 - abs(candidate - learner) / 4` and the policy keeps normal practice near the configured productive success range (70–85%). A probe adds a small stretch to the selected difficulty and applies a limited failure penalty.

