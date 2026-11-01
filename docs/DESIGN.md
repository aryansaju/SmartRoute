# Design

## Request lifecycle

1. **Authenticate and identify.** Optional Bearer key (`GATEWAY_API_KEY`). Tenant comes from `X-SmartRoute-Tenant` (see the security note in the README).
2. **Parse** the body with `internal/chat`. The raw bytes are kept and forwarded unchanged.
3. **Decide cacheability.** Not cached: `n > 1`, tool or function calls, tool messages, non text content, `X-SmartRoute-Cache: bypass`, or caching disabled.
4. **Derive keys.**
   * `namespace = sha256(tenant, model, system prompt, generation parameters)`
   * `exact key = sha256(namespace, conversation text)`
   The `stream` flag is deliberately not part of either key.
5. **Lookup** (`internal/cache`): L1 LRU, then exact match in Postgres, then, only if the request is a single turn prompt of at most `SEMANTIC_MAX_CHARS`, embed the prompt and ask pgvector for the top K neighbours inside the namespace. Each candidate must clear the threshold and the guard.
6. **Hit:** replay the stored completion as JSON or as an SSE stream, record the estimated saving.
7. **Miss:** call the upstream.
   * Non streaming: identical concurrent requests are coalesced; the leader parses the answer and stores it.
   * Streaming: bytes are forwarded as they arrive (flushed per SSE event) while an accumulator rebuilds the answer. It is stored only if the stream ended with `[DONE]`, had a clean finish reason, contained no tool calls or error chunk, and the client did not disconnect.
8. **Write back** is asynchronous through a bounded queue. L1 is updated immediately, so a quick repeat hits even before the database write lands.
9. **Record** metrics and a `request_log` row (also asynchronous, also droppable).

## Why the cache stores a normalised completion, not response bytes

Streaming and non streaming responses have different wire formats. Storing `{model, content, finish_reason, token counts}` lets either kind of request be served from either kind of origin, and keeps one format for both L1 and Postgres. The cost: replayed responses are equivalent to, not byte identical with, the original (new id, new timestamp).

## Isolation: the namespace

A cached answer is valid only for the same tenant, model, system prompt and generation parameters. The semantic search is restricted to the request's namespace, so a paraphrase can never pull in another tenant's or another persona's answer. This is the property tests should defend most strongly (`TestTenantsNeverShareCachedAnswers`, `TestSemanticNeverCrossesNamespaces`).

## Semantic eligibility

Only single turn prompts (optional system message plus one user message) are matched semantically. An embedding of the last message cannot represent a conversation's history, so multi turn requests are exact match only. Prompts longer than `SEMANTIC_MAX_CHARS` are also exact only because embedding models truncate long input (MiniLM keeps roughly 256 word pieces), which would make two different long prompts look identical.

## The guard

Embedding similarity is a blunt instrument: "capital of France" and "capital of Germany" score as near duplicates. After the threshold, `GuardAllows` compares the two prompts on numbers, negation words, a small list of antonym pairs and capitalised names, and rejects the hit on any difference. It costs microseconds and is evaluated per candidate. It is conservative by design (a false reject costs one upstream call, a false accept serves a wrong answer) and it is blind to lower case names, synonyms and paraphrased negation. The Go version and the Python port in `bench/cache_quality/guard.py` are tested against the same case table.

## Storage

`cache_entries` holds one row per `(namespace, exact_key)` with a nullable `vector(384)` and an expiry; `request_log` holds per request telemetry. The HNSW index uses cosine distance (`m = 16`, `ef_construction = 64`; `hnsw.ef_search` is set per connection). Entries without an embedding (multi turn, long prompts) are not in the index. Expired rows are ignored by queries and deleted every five minutes.

**pgvector caveat.** Filtering by namespace together with an HNSW scan can return fewer rows than requested, because the filter is applied to the index's candidate list. The gateway enables `hnsw.iterative_scan = relaxed_order` where the server supports it (pgvector 0.8+). Verify recall against exact search at your real data size before trusting hit rates.

## Failure behaviour

| Failure | Behaviour |
|---|---|
| Embedder down or slow | Counted in `smartroute_embed_errors_total`; request is a miss, exact matching continues |
| Postgres lookup fails | Counted in `smartroute_store_errors_total`; request is a miss |
| Postgres write fails or queue full | Dropped and counted; the client is unaffected |
| Upstream non 200 | Passed through to the client, never cached |
| Upstream unreachable | 502 (504 on timeout); a client disconnect is recorded as 499 |
| Client disconnects mid stream | Upstream request is cancelled, nothing is cached |
| Handler panic | Recovered, 500, counted |
| Gateway shutdown | HTTP server drains, then the write queues are flushed, then the store closes |

## Concurrency notes

Shared state is the LRU (mutex), the in memory store (RW mutex), the metrics registry (mutex), the coalescing group (mutex) and the bounded work queues. Each request owns its own `call` value. The coalescing leader runs the upstream call on a context detached from its client, so one client hanging up cannot fail the others waiting for the same answer.

## What is deliberately not here yet

Routing across providers, failover, per tenant limits and keys. The `provider.Provider` interface is small on purpose so those can wrap several providers behind the same shape without touching the handler.
