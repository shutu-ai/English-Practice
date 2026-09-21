# Architecture

The server is a single Go process serving JSON APIs and `web/dist`. `main.go` contains the HTTP composition root, SQLite migration/seed setup, policy services, and transport adapters. The important boundaries are:

* `LLMClient` accepts a provider-neutral `ChatRequest`; OpenAI, OpenAI-compatible, and Ollama use the same HTTP adapter.
* Exercise generation selects scene, intent, pattern, and continuous difficulty. The application chooses the next item; an LLM only supplies language judgments.
* Evaluation is parsed, schema validated, and business validated before it can enter the transaction that saves an evaluation and updates learning state.
* One transaction saves a validated attempt, evaluation, pattern mastery, scene mastery, error statistics, and the review schedule. Failed evaluations only mark the attempt failed.

The static Vue source is under `src/`; `web/dist` is a dependency-free fallback bundle so a fresh checkout starts without an npm registry.
