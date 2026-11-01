# Decisions

Format: context, decision, consequence. Decisions 1 to 3 were made with the project owner; the rest are defaults chosen by the implementation and are open to change.

## Made with the project owner

**1. Embeddings come from a small Python sidecar running a local model.**
Context: Go has no mature path for running sentence embedding models, and a hosted embedding API adds cost and network noise to every benchmark.
Decision: a FastAPI service wrapping `all-MiniLM-L6-v2` (384 dimensions), called over HTTP, with an offline `hash` backend for tests.
Consequence: free and reproducible, one extra container, and the embedding call sits on the hot path of every semantic miss (measure it: it is likely the largest stage). The vector column is `vector(384)`; a different model dimension needs a schema change.

**2. Provider adapters use plain `net/http`, not vendor SDKs.**
Context: the gateway must stream bytes through and inspect them; SDKs hide the wire protocol and add dependencies.
Decision: one OpenAI compatible adapter over the standard library; Anthropic and Google adapters will follow the same pattern in the next milestone.
Consequence: full control over streaming and timeouts, fewer dependencies, but request and response translation for non OpenAI APIs must be written by hand. This departs slightly from the original description ("official SDKs"); say so honestly if asked.

**3. Core first, then routing and resilience.**
Decision: this pass delivers the cache, streaming, mock upstream, benchmarks and deployment. Routing, failover, rate limiting and tenant keys come after the code is confirmed to build.

## Defaults chosen by the implementation

**4. Cache a normalised completion, not bytes.** See DESIGN.md. Lets streamed and non streamed requests share entries.

**5. The namespace is the isolation boundary.** Tenant, model, system prompt and generation parameters all partition the cache. Stricter than many gateways, which lowers the hit rate slightly and removes a class of leaks.

**6. Semantic matching only for short single turn prompts.** Trades coverage for correctness.

**7. A rule based guard after the similarity threshold.** Chosen over a second model call because it is deterministic, free and testable. Its blind spots are measured, not hidden.

**8. Cache writes are asynchronous and droppable.** A slow database must never slow a request. L1 is updated synchronously so repeats still hit immediately.

**9. Only non streaming requests are coalesced.** Fanning one live stream out to many clients is a larger design (buffering, late joiners). Deferred.

**10. No Prometheus client library.** A small text renderer covers counters, gauges and histograms with no extra dependency. Swap it for the official client if you need exemplars or the process collectors.

**11. Default threshold 0.92 and a 24 hour TTL are placeholders.** Replace them with values justified by `bench/cache_quality` and by how fast your answers go stale.

## Decisions that are yours to make

1. **GitHub module path and license holder.** Run `scripts/rename_module.py`; put your name in `LICENSE`.
2. **Embedding model.** MiniLM (384 dimensions, fast) is the default. A larger model may separate near duplicates better but changes the vector size, the schema and the latency of every semantic lookup. Compare at least two in the benchmark.
3. **The operating point.** How many wrong answers per hundred requests are acceptable? That budget (`--max-fpr`) picks the threshold; it is a product decision, not a technical one.
4. **Whether to cache non deterministic requests.** Today requests with `temperature > 0` are cached, keyed by their parameters, so a hit repeats one sampled answer. If users expect variety, exclude them or cache only `temperature = 0`.
5. **Data handling.** Prompts and answers are stored in plain text. For real users decide retention (TTL), encryption at rest, and whether prompts containing personal data should bypass the cache.
6. **Tenant model.** Replace the trusted `X-SmartRoute-Tenant` header with per tenant API keys (first task of the next milestone).
7. **What to claim on a resume.** Only numbers you measured: see BENCHMARKS.md for the exact runs and what each one supports.
