# Benchmarks

**No results have been produced yet.** This file defines the experiments; the results tables below are empty on purpose and must only be filled from saved output in `bench/results/`. Every number you quote should map to one command here.

## 1. Cache quality (most important)

Question: at a given similarity threshold, how many repeated questions are served from cache, and how many wrong answers are served?

```powershell
python bench/cache_quality/sweep.py --pairs bench/cache_quality/data/handwritten_pairs.csv --backend sentence-transformers --max-fpr 0.01 --out bench/results/cache_quality
```

Data:
* `handwritten_pairs.csv`: paraphrases plus hard negatives (entity swaps, number swaps, negation, antonyms) and `guard_blind` negatives the guard cannot catch. Because the hard negatives were written with the guard's rules in mind, **the handwritten set flatters the guard**. Treat it as a regression test, not as evidence.
* QQP (everyday duplicates) and PAWS (adversarial, high word overlap): `bench/cache_quality/prepare_datasets.py`. These are the honest tests. Report them separately; do not average them together.

Method: for each pair, `question1` is the cached prompt and `question2` the new request. A hit on a duplicate pair is correct; a hit on a non duplicate pair is a wrong answer served. The threshold is chosen on a tune half under a false positive budget and every reported number comes from the disjoint test half, with and without the guard.

Report: hit rate and false positive rate at the chosen threshold; the curve `threshold_tradeoff.png`; results per embedding model (run at least two). **Do not publish numbers from `--backend hash`: it is not semantic** (the tool prints a warning and records it in `results.json`).

ANN accuracy check (separate): compare HNSW results with exact search at 10k, 100k and 1M entries across `hnsw.ef_search` values and report recall@1. Include namespace filtered queries, because that is where HNSW with filters can lose recall.

## 2. Gateway overhead

Isolate the gateway from real provider noise with the mock upstream at a fixed latency.

```powershell
go run ./cmd/mockupstream -latency=500ms          # terminal 1
go run ./cmd/gateway                              # terminal 2 (or the compose stack)
k6 run -e BASE_URL=http://localhost:9000 -e MODE=miss     --summary-export=bench/results/direct.json          bench/k6/overhead.js
k6 run -e BASE_URL=http://localhost:8080 -e MODE=miss     --summary-export=bench/results/gateway_miss.json    bench/k6/overhead.js
k6 run -e BASE_URL=http://localhost:8080 -e MODE=exact    --summary-export=bench/results/gateway_exact.json   bench/k6/overhead.js
k6 run -e BASE_URL=http://localhost:8080 -e MODE=semantic --summary-export=bench/results/gateway_semantic.json bench/k6/overhead.js
python bench/overhead_report.py bench/results/direct.json bench/results/gateway_miss.json
```

* Overhead is gateway latency minus direct latency at matching percentiles. For hits the "direct" comparison is the 500 ms upstream you avoided, so report hit latency in absolute terms instead.
* The load is open loop (constant arrival rate), so tail latency is not understated.
* Use the `Server-Timing` header and the `smartroute_stage_duration_seconds` histogram to say which stage dominates (expect `embed` for semantic lookups).
* Run the load generator and the gateway on different machines or state clearly that they shared one. Use `--summary-trend-stats` as set in the script, run 3 to 5 times, report the median and the spread.

## 3. Throughput and scaling

Sweep the arrival rate (`RATE`) until p99 climbs or errors appear and record that knee, with gateway CPU and memory, Postgres connections, and goroutine count (add `net/http/pprof` or a runtime metrics gauge when you get to this). Repeat the semantic mode with 10k, 100k and 1M cached entries to show how vector search latency grows. Try two or three settings (`PG_MAX_CONNS`, `HNSW_EF_SEARCH`) with before and after numbers. Always record hardware and whether Postgres shared the machine.

## 4. Net cost

`net savings = (hits x avoided provider cost) - (all requests x embedding cost) - infrastructure cost`

Replay synthetic workloads with a controlled repeat rate (10, 30, 50, 70 percent) and a skewed popularity distribution, using prices you looked up and wrote down in `PRICES_JSON`. Find the break even hit rate below which the gateway costs more than it saves, and report it.

## Results (fill in from `bench/results/` only)

| Experiment | Command and date | Hardware | Result |
|---|---|---|---|
| Cache quality, QQP | | | |
| Cache quality, PAWS | | | |
| ANN recall vs exact | | | |
| Overhead, miss path | | | |
| Latency, exact hit | | | |
| Latency, semantic hit | | | |
| Throughput knee | | | |
| Break even hit rate | | | |

## Limits of these benchmarks

Synthetic traffic, a mock provider, a single node, answers judged by pair labels rather than by a human or a model reading the responses. Routing benchmarks measure cost, not answer quality.
