# Exercise Generation

The generator receives the selected skill, pattern, scene, intent, target difficulty, reason, review/probe flags, recent prompts, recent contexts, and contexts to avoid. An enabled provider may return strict JSON with a Chinese prompt, target pattern, estimated difficulty, and reference answers. Reference answers are evaluator context only and are never shown before submission.

When a provider fails or returns invalid JSON, a curated multi-example seed pool is used. Exact normalized prompt hashes and recent pattern contexts are guarded before insertion. Fallback exercises are marked `generated_by = fallback`.

