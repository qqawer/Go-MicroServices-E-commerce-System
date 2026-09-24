"""Check business throughput and latency accounting using synthetic k6 points."""
import gzip
import json
from pathlib import Path
import tempfile
import unittest

from run import summarize_points


def point(metric, value, **tags):
    return {"type": "Point", "metric": metric, "data": {"value": value, "tags": tags}}


def request(case, duration, outcome="ok", status="200"):
    return [
        point("business_attempts", 1, case=case),
        point("http_reqs", 1, case=case, status=status),
        point("business_successes", int(outcome == "ok"), case=case),
        point("business_outcomes", 1, case=case, outcome=outcome),
        point("business_duration", duration, case=case),
    ]


class MetricsTest(unittest.TestCase):
    def summarize(self, points, seconds=2):
        with tempfile.TemporaryDirectory(prefix="grpctest-benchmark-metrics-") as directory:
            path = Path(directory) / "points.json.gz"
            with gzip.open(path, "wt", encoding="utf-8") as handle:
                for value in points:
                    handle.write(json.dumps(value) + "\n")
            return summarize_points(path, seconds)

    def test_http_200_with_wrong_business_data_is_not_success(self):
        result = self.summarize(request("list", 10, "invalid_data"))
        self.assertEqual(result["http_requests"], 1)
        self.assertEqual(result["attempts"], 1)
        self.assertEqual(result["successes"], 0)
        self.assertEqual(result["sent_rps"], .5)
        self.assertEqual(result["successful_qps"], 0)
        self.assertEqual(result["error_rate"], 1)
        self.assertEqual(result["outcomes"], {"invalid_data": 1})
        self.assertEqual(result["latency"]["all"]["p99_ms"], 10)

    def test_mixed_cases_classifications_percentiles_and_dropped_iterations(self):
        points = (
            request("detail", 10) + request("list", 20) +
            request("filtered", 30, "rate_limited", "429") +
            request("detail", 40, "transport_1050", "0") +
            request("list", 100, "invalid_data") +
            request("list", 200, "invalid_json")
        )
        points.extend([
            point("dropped_iterations", 2), point("dropped_iterations", 3),
            # k6 emits metric definitions as well as values. They must not be
            # counted; an unrelated timing metric is not a business sample.
            {"type": "Metric", "metric": "business_attempts", "data": {"type": "counter"}},
            point("http_req_duration", 9999, case="detail"),
        ])
        result = self.summarize(points)
        self.assertEqual(result["attempts"], 6)
        self.assertEqual(result["http_requests"], 6)
        self.assertEqual(result["successes"], 2)
        self.assertEqual(result["successful_qps"], 1)
        self.assertEqual(result["sent_rps"], 3)
        self.assertAlmostEqual(result["error_rate"], 2 / 3)
        self.assertEqual(result["dropped_iterations"], 5)
        self.assertEqual(result["outcomes"], {
            "ok": 2, "rate_limited": 1, "transport_1050": 1,
            "invalid_data": 1, "invalid_json": 1,
        })
        latency = result["latency"]
        self.assertEqual(set(latency), {"all", "detail", "list", "filtered"})
        self.assertEqual(latency["all"]["samples"], 6)
        self.assertEqual(latency["all"]["p50_ms"], 35)
        self.assertEqual(latency["all"]["p95_ms"], 175)
        self.assertAlmostEqual(latency["all"]["p99_ms"], 195)
        self.assertEqual(latency["all"]["max_ms"], 200)
        self.assertEqual(latency["detail"]["samples"], 2)
        self.assertEqual(latency["detail"]["p50_ms"], 25)
        self.assertEqual(latency["detail"]["p95_ms"], 38.5)
        self.assertEqual(latency["list"]["samples"], 3)
        self.assertEqual(latency["list"]["p50_ms"], 100)
        self.assertEqual(latency["list"]["p95_ms"], 190)
        self.assertEqual(latency["filtered"]["p99_ms"], 30)

    def test_repeated_outcomes_are_accumulated(self):
        points = request("list", 10, "rate_limited", "429") + request("list", 20, "rate_limited", "429")
        result = self.summarize(points, seconds=4)
        self.assertEqual(result["outcomes"], {"rate_limited": 2})
        self.assertEqual(result["successful_qps"], 0)
        self.assertEqual(result["sent_rps"], .5)
        self.assertEqual(result["dropped_iterations"], 0)

    def test_response_timestamp_span_preserves_twelve_minute_clock_gap(self):
        points = request("list", 10) + request("detail", 20)
        # Raw UTC timestamps are deliberately out of file order. A two-second
        # scheduled/monotonic window must not hide a twelve-minute sleep gap.
        timestamps = iter(("2026-09-24T07:12:00.123Z", "2026-09-24T07:00:00.123Z"))
        for value in points:
            if value["metric"] == "business_outcomes":
                value["data"]["time"] = next(timestamps)
        result = self.summarize(points, seconds=2)
        self.assertEqual(result["response_timestamp_span_seconds"], 720)
        self.assertEqual(result["attempts"], 2)

    def test_missing_or_extra_accounting_samples_are_rejected(self):
        original = request("detail", 10) + request("list", 20)
        for metric in ("business_attempts", "http_reqs", "business_successes", "business_outcomes", "business_duration"):
            with self.subTest(metric=metric, corruption="missing"):
                points = list(original)
                index = next(i for i, value in enumerate(points) if value["metric"] == metric)
                points.pop(index)
                with self.assertRaises(RuntimeError):
                    self.summarize(points)
            with self.subTest(metric=metric, corruption="duplicate"):
                extra = next(value for value in original if value["metric"] == metric)
                with self.assertRaises(RuntimeError):
                    self.summarize(original + [extra])

    def test_no_requests_or_false_success_counter_is_rejected(self):
        for points in ([], [point("dropped_iterations", 10)]):
            with self.subTest(points=points):
                with self.assertRaises(RuntimeError):
                    self.summarize(points)
        points = request("list", 10, "invalid_data")
        for value in points:
            if value["metric"] == "business_successes":
                value["data"]["value"] = 1
        with self.assertRaises(RuntimeError):
            self.summarize(points)


if __name__ == "__main__":
    unittest.main()
