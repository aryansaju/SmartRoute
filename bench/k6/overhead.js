// Open loop load test for gateway overhead. Constant ARRIVAL rate, so slow responses cannot slow the
// load down (a closed loop tool would hide tail latency: "coordinated omission").
//
//   k6 run -e BASE_URL=http://localhost:9000 -e MODE=miss  --summary-export=bench/results/direct.json  bench/k6/overhead.js
//   k6 run -e BASE_URL=http://localhost:8080 -e MODE=miss  --summary-export=bench/results/gateway.json bench/k6/overhead.js
//   python bench/overhead_report.py bench/results/direct.json bench/results/gateway.json
//
// MODE: exact (same prompt every time) | semantic (paraphrases of a few seeded prompts) | miss (unique prompts)
// The first run points at the mock upstream directly; subtracting it from the gateway run isolates gateway overhead.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';
import exec from 'k6/execution';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const MODE = __ENV.MODE || 'exact';
const RATE = parseInt(__ENV.RATE || '200');
const DURATION = __ENV.DURATION || '60s';
const WARMUP = __ENV.WARMUP || '15s';
const VUS = parseInt(__ENV.PRE_ALLOCATED_VUS || '200');
const STREAM = (__ENV.STREAM || 'false') === 'true';

const outcomes = new Counter('cache_outcome');

export const options = {
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    warmup: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: WARMUP, preAllocatedVUs: VUS, maxVUs: VUS * 5, tags: { phase: 'warmup' } },
    measured: { executor: 'constant-arrival-rate', rate: RATE, timeUnit: '1s', duration: DURATION, startTime: WARMUP, preAllocatedVUs: VUS, maxVUs: VUS * 5, tags: { phase: 'measured' } },
  },
  thresholds: {
    'http_req_failed{phase:measured}': ['rate<0.01'],
    'http_req_duration{phase:measured}': ['p(99)<5000'],
  },
};

const GROUPS = [
  ['How do I reset my password?', 'How can I reset my password?', 'What is the way to reset my password?'],
  ['What is the difference between TCP and UDP?', 'How are TCP and UDP different?', 'Explain how TCP differs from UDP.'],
  ['How do I create a new branch in Git?', 'What command creates a new branch in Git?', 'How can I make a new Git branch?'],
  ['Why is the sky blue?', 'What makes the sky look blue?', 'Explain why the sky appears blue.'],
  ['What is photosynthesis?', 'Can you tell me what photosynthesis is?', 'Explain photosynthesis.'],
];

function payload(prompt) {
  return JSON.stringify({ model: 'mock-llm', stream: STREAM, messages: [{ role: 'user', content: prompt }] });
}

const params = { headers: { 'Content-Type': 'application/json', 'X-SmartRoute-Tenant': 'bench' } };

export function setup() {
  // Seed the cache so exact and semantic runs measure hits, not the first miss.
  if (MODE === 'exact') http.post(`${BASE}/v1/chat/completions`, payload('What is the capital of France?'), params);
  if (MODE === 'semantic') GROUPS.forEach((g) => http.post(`${BASE}/v1/chat/completions`, payload(g[0]), params));
}

function prompt() {
  const i = exec.scenario.iterationInTest;
  if (MODE === 'exact') return 'What is the capital of France?';
  if (MODE === 'semantic') {
    const g = GROUPS[i % GROUPS.length];
    return g[1 + (Math.floor(i / GROUPS.length) % (g.length - 1))];
  }
  return `Unique question number ${i} asked at ${Date.now()}`;
}

export default function () {
  const res = http.post(`${BASE}/v1/chat/completions`, payload(prompt()), params);
  check(res, { 'status is 200': (r) => r.status === 200 });
  for (const k of Object.keys(res.headers)) {
    if (k.toLowerCase() === 'x-smartroute-cache') outcomes.add(1, { outcome: res.headers[k] });
  }
}
