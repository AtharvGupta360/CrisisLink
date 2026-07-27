// k6 load test for CrisisLink's dispatch-decision path.
//
// WHY THIS ENDPOINT: GET /incidents/:id/candidates is the most interesting read in
// the system — it runs the PostGIS K-nearest-neighbour query, cross-checks live
// presence in Redis, and scores every candidate. It is read-only and idempotent,
// so it can be hammered without mutating state, which makes it the honest thing to
// quote a p99 on. (A write path like dispatch would exhaust the unit pool and
// measure contention, not throughput.)
//
// RUN (from project root, app + infra already up, fixtures seeded):
//   docker run --rm -i --add-host=host.docker.internal:host-gateway \
//     -e BASE_URL -e TOKEN -e INCIDENT_ID grafana/k6 run - < test/load/dispatch_decision.js
//
// The thresholds double as PASS/FAIL: k6 exits non-zero if p95/p99/error-rate blow
// their budgets, so this can gate CI, not just print numbers.

import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'http://host.docker.internal:8080';
const TOKEN = __ENV.TOKEN;
const INCIDENT = __ENV.INCIDENT_ID;

export const options = {
  scenarios: {
    // ramping-vus: climb to 50 concurrent virtual users, hold, then drain. The
    // hold is where the steady-state numbers come from; the ramp reveals whether
    // latency degrades gracefully or falls off a cliff.
    dispatch_decision: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 50 }, // warm up
        { duration: '40s', target: 50 }, // steady state — measure here
        { duration: '5s', target: 0 },   // ramp down
      ],
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],                  // <1% errors
    http_req_duration: ['p(95)<200', 'p(99)<500'],   // budgets for a ms-scale API
  },
};

const params = { headers: { Authorization: `Bearer ${TOKEN}` } };

export default function () {
  const res = http.get(`${BASE}/api/v1/incidents/${INCIDENT}/candidates`, params);
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has candidates array': (r) => r.body && r.body.includes('candidates'),
  });
}
