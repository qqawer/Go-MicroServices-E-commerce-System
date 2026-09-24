import http from 'k6/http';
import exec from 'k6/execution';
import { Counter, Rate, Trend } from 'k6/metrics';

const manifest = JSON.parse(open(__ENV.MANIFEST));
const base = __ENV.BASE_URL;
if (!/^http:\/\/127\.0\.0\.1:[1-9][0-9]*$/.test(base || '')) {
  throw new Error('benchmark accepts only an explicit IPv4 loopback HTTP endpoint');
}
const testCase = __ENV.CASE || 'mixed';
if (!['mixed', 'detail', 'hot_detail', ...Object.keys(manifest.cases)].includes(testCase)) {
  throw new Error('unknown benchmark case');
}
const rate = Number(__ENV.RATE || 20);
const seconds = Number(__ENV.SECONDS || 120);
const vus = Number(__ENV.VUS || 100);
if (![rate, seconds, vus].every(x => Number.isInteger(x) && x > 0)) throw new Error('invalid load options');

const attempts = new Counter('business_attempts');
const successes = new Counter('business_successes');
const failures = new Rate('business_failed');
const outcomes = new Counter('business_outcomes');
const durations = new Trend('business_duration', true);

export const options = {
  scenarios: { goods: { executor: 'constant-arrival-rate', rate, timeUnit: '1s',
    duration: `${seconds}s`, preAllocatedVUs: vus, maxVUs: vus, gracefulStop: '10s' } },
  thresholds: {
    business_failed: ['rate<=0.001'],
    http_req_duration: ['p(95)<=300', 'p(99)<=1000'],
    dropped_iterations: ['count==0'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  systemTags: ['status', 'method', 'name', 'scenario', 'error_code', 'expected_response'],
  discardResponseBodies: false,
  userAgent: 'grpctest-local-goods-benchmark',
};
for (const name of testCase === 'mixed' ? ['detail', 'list', 'filtered'] : [testCase]) {
  options.thresholds[`business_duration{case:${name}}`] = ['p(95)>=0'];
}

function validRow(row) {
  return row && Number.isInteger(row.id) && row.id > 0 && row.id <= manifest.size &&
    typeof row.name === 'string' && row.name.length > 0 && Number.isFinite(row.shop_price) &&
    row.brand && row.brand.id > 0 && typeof row.brand.name === 'string' && row.brand.name.length > 0 &&
    row.ctegory && row.ctegory.id > 0 && typeof row.ctegory.name === 'string' && row.ctegory.name.length > 0 &&
    row.inventory_ready === true && row.on_sale === true && Array.isArray(row.images) && row.images.length > 0;
}

function validDetail(row, expected) {
  return validRow(row) && row.id === expected.id && row.name === expected.name &&
    row.shop_price === expected.shop_price && row.brand.id === expected.brand.id &&
    row.brand.name === expected.brand.name && row.brand.logo === expected.brand.logo &&
    row.ctegory.id === expected.ctegory.id && row.ctegory.name === expected.ctegory.name;
}

function validList(body, c) {
  if (!body || body.total !== c.total || !Array.isArray(body.data)) return false;
  const length = Math.min(c.page_size, Math.max(0, c.total - (c.page - 1) * c.page_size));
  if (body.data.length !== length) return false;
  const seen = new Set();
  const p = c.predicate || {};
  return body.data.every(row => {
    if (!validRow(row) || seen.has(row.id)) return false;
    seen.add(row.id);
    return (!p.brand || row.brand.id === p.brand) &&
      (!p.min_price || row.shop_price >= p.min_price) &&
      (!p.max_price || row.shop_price <= p.max_price) &&
      (!p.category_ids || p.category_ids.includes(row.ctegory.id)) &&
      (!p.keyword || row.name.includes(p.keyword));
  });
}

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const slot = iteration % 10;
  const selected = testCase === 'mixed' ? (slot < 5 ? 'detail' : slot < 8 ? 'list' : 'filtered') : testCase;
  const detail = selected === 'detail' || selected === 'hot_detail';
  const expected = detail ? manifest.details[selected === 'hot_detail' ? 0 : (iteration * 37) % manifest.details.length] : manifest.cases[selected];
  const path = detail ? `/g/v1/goods/${expected.id}` : expected.path;
  const tags = { case: selected };
  attempts.add(1, tags);
  const response = http.get(base + path, { timeout: '5s', redirects: 0,
    tags: { ...tags, name: detail ? 'GET goods/:id' : `GET goods ${selected}` } });
  let outcome = 'ok';
  if (response.status !== 200) {
    outcome = response.status === 429 ? 'rate_limited' : response.status === 0 ? `transport_${response.error_code}` : `http_${response.status}`;
  } else {
    try {
      const body = response.json();
      if (!(detail ? validDetail(body, expected) : validList(body, expected))) outcome = 'invalid_data';
    } catch (_) { outcome = 'invalid_json'; }
  }
  const ok = outcome === 'ok';
  successes.add(ok ? 1 : 0, tags);
  failures.add(!ok, tags);
  outcomes.add(1, { ...tags, outcome });
  durations.add(response.timings.duration, tags);
}

export function handleSummary(data) {
  return { [__ENV.SUMMARY]: JSON.stringify(data, null, 2) };
}
