# Roadmap

## Milestone 2: routing and resilience (next)

1. **Tenant API keys.** Replace the trusted tenant header with hashed keys in Postgres; tenant derives from the key. Per tenant namespace, quotas and budgets.
2. **Rate limiting.** Token bucket per tenant (and a global concurrency limit with load shedding).
3. **Provider interface.** Anthropic and Google adapters over plain HTTP, translating requests and responses to the common shape, including streaming.
4. **Failover.** Ordered fallbacks, retries with jitter on 429 and 5xx, a circuit breaker per provider, and a rule for what is safe to retry once bytes have been sent to a streaming client (answer: nothing, fail the stream cleanly).
5. **Cost aware routing.** A transparent rule table: price per token by model, estimated prompt tokens, simple task hints, always logging the decision and the reason. State plainly that it optimises cost, not answer quality, unless a quality check is added.
6. **Failover and routing benchmarks.** Mock provider returning 429, 500 and timeouts at set rates; success rate with and without fallback; time to failover.

## Milestone 3: hardening

* Coalescing for streaming requests (buffer plus late joiner replay).
* Cache invalidation API (by tenant, model or time) and entry size limits.
* Runtime and pprof metrics; Grafana dashboard JSON.
* Optional verification step for borderline semantic matches (a cheap second signal before serving).
* Encryption at rest and a retention policy for stored prompts.
* Fuzz tests for the request parser and the SSE accumulator.

## Dashboard

A small Streamlit page reading `bench/results/` and the `request_log` table (hit ratio, estimated savings, stage latencies). Deliberately last: it is the least informative part of the project.
