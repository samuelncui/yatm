"""Semantic curve/window and mocked sampler checks; no services or Tape access."""

import csv
import importlib.util
import json
from pathlib import Path
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest import mock

import physical_performance_metrics as metrics


def row(seconds, value, phase="JOB_PHASE_COPYING_TO_MEDIA", duration=0):
    return {"channel": "app", "request_start_mono_ns": int((seconds - duration / 2) * 1e9),
            "request_end_mono_ns": int((seconds + duration / 2) * 1e9),
            "response": {"progress": {"copied_bytes": str(value), "copied_file_count": "1",
                                      "stage": {"phase": phase}}}}


def entries(rate, end=180):
    points = [metrics.app_point(row(t, rate(t))) for t in range(0, end + 1, 2)]
    return [metrics.interval(a, b, 2) for a, b in zip(points, points[1:])]


class CurveTests(unittest.TestCase):
    def test_stable_low_speed_is_stable_not_vendor_failure(self):
        result = metrics.steady_window(entries(lambda t: t * 1000), 0)
        self.assertTrue(result["stable"])
        self.assertEqual(result["mean_bytes_per_second"], 1000)
        self.assertEqual(result["cv"], 0)

    def test_ramp_is_not_steady(self):
        self.assertFalse(metrics.steady_window(entries(lambda t: t * t * 1000), 0)["stable"])

    def test_periodic_stalls_can_have_stable_means_but_remain_observed(self):
        # One 2-second no-progress interval in each ten seconds, repeating identically.
        work = lambda t: (t // 10 * 8 + min(t % 10, 8)) * 1000
        data = entries(work)
        self.assertTrue(metrics.steady_window(data, 0)["stable"])
        self.assertEqual(sum(entry["no_progress_observed"] for entry in data), 18)

    def test_reset_invalidates_crossing_window(self):
        data = entries(lambda t: t * 1000 if t < 100 else (t - 100) * 1000)
        self.assertEqual(sum(entry["reason"] == "counter-reset" for entry in data), 1)
        self.assertFalse(metrics.steady_window(data, 0)["quality"])

    def test_missing_gap_not_zero_or_full_coverage(self):
        before = metrics.app_point(row(0, 0))
        after = metrics.app_point(row(20, 20000))
        entry = metrics.interval(before, after, 2)
        self.assertEqual(entry["reason"], "sample-gap")
        self.assertIsNone(entry["rate"])
        self.assertFalse(entry["no_progress_observed"])
        result = metrics.bin_estimate([entry], 0, 30)
        self.assertEqual(result["coverage"], 0)
        self.assertIsNone(result["bytes_per_second"])

    def test_irregular_overlap_preserves_units_and_uncertainty(self):
        entry = metrics.interval(metrics.app_point(row(0, 0, duration=0.2)),
                                 metrics.app_point(row(3, 3000, duration=0.4)), 2)
        result = metrics.bin_estimate([entry], 1, 1)
        self.assertEqual(result["estimated_bytes"], 1000)
        self.assertEqual(result["coverage"], 1)
        self.assertAlmostEqual(result["max_request_uncertainty_seconds"], 0.2)
        long = metrics.interval(metrics.app_point(row(0, 0)), metrics.app_point(row(2, 2000, duration=5)), 2)
        self.assertEqual(long["reason"], "request-uncertainty")

    def analyze(self, rows, directory, plot=False):
        raw = Path(directory) / "raw.jsonl"
        raw.write_text("".join(json.dumps(value) + "\n" for value in rows))
        return metrics.analyze_samples(raw, Path(directory) / "curve", plot=plot)

    def test_first_window_selected_not_fastest_and_all_candidates_saved(self):
        with tempfile.TemporaryDirectory() as directory:
            count = lambda t: t * 1000 if t <= 240 else 240000 + (t - 240) * 2000
            result = self.analyze([row(t, count(t)) for t in range(0, 602, 2)], directory)
            self.assertEqual(result["first_stable_window"]["start_seconds"], 2)
            self.assertEqual(result["first_stable_window"]["mean_bytes_per_second"], 1000)
            self.assertEqual(result["window_count"], 42)
            self.assertGreater(result["stable_window_count"], 1)
            self.assertLess(result["stable_window_count"], result["window_count"])
            with Path(directory, "curve-windows.csv").open() as source:
                rows = list(csv.DictReader(source))
            self.assertEqual(float(rows[1]["start_seconds"]), 12)
            self.assertEqual(float(rows[-1]["mean_bytes_per_second"]), 2000)
            self.assertTrue(any(row["stable"] == "False" for row in rows))

    def test_phase_boundary_excludes_final_published_adjustment(self):
        with tempfile.TemporaryDirectory() as directory:
            values = [row(t, t * 1000) for t in range(0, 202, 2)]
            values += [row(202, 10**10, "JOB_PHASE_FINALIZING_MEDIA")]
            result = self.analyze(values, directory)
            with Path(directory, "curve-intervals.csv").open() as source:
                intervals = list(csv.DictReader(source))
            self.assertEqual(intervals[-1]["reason"], "phase-boundary")
            self.assertEqual(intervals[-1]["bytes"], "")
            self.assertEqual(result["copy_observed_end_seconds"], 200)
            self.assertEqual(result["observation_end_seconds"], 202)

    def test_errors_cannot_bridge_counter_observations(self):
        with tempfile.TemporaryDirectory() as directory:
            values = [row(0, 0), row(2, 2000), {"channel": "app", "request_start_mono_ns": 3*10**9,
                "request_end_mono_ns": 3*10**9, "error": "timeout"}, row(4, 4000), row(6, 6000)]
            result = self.analyze(values, directory)
            self.assertEqual(result["invalid_intervals"], 1)
            with Path(directory, "curve-intervals.csv").open() as source:
                intervals = list(csv.DictReader(source))
            self.assertEqual(sum(float(x["bytes"] or 0) for x in intervals), 4000)

    def test_driver_proxies_not_physical_and_reposition_not_differenced(self):
        with tempfile.TemporaryDirectory() as directory:
            rows = [{"channel": "driver", "request_start_mono_ns": t*10**9,
                     "request_end_mono_ns": t*10**9, "response": {"pages": {
                         "0C": {"parameters": {"0": t * 10**9, "1": t * 5*10**8}},
                         "34": {"parameters": {"0": 2}}}}} for t in (0, 5, 10)]
            result = self.analyze(rows, directory)
            self.assertIsNone(result["first_stable_window"])
            with Path(directory, "curve-driver.csv").open() as source:
                samples = list(csv.DictReader(source))
            self.assertEqual([x["value"] for x in samples if x["page"] == "34"], ["2", "2", "2"])
            self.assertEqual(result["driver_samples"], 3)

    def test_copy_phase_edges_use_request_uncertainty_not_payload_total(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = [row(10, 0, "JOB_PHASE_PREPARING_MEDIA", duration=2),
                       row(14, 100, duration=2), row(18, 500, duration=2),
                       row(22, 999999, "JOB_PHASE_FINALIZING_MEDIA", duration=2)]
            summary = self.analyze(samples, directory)
            edges = summary["first_copy_phase_edges"]
            self.assertEqual((edges["start_lower_seconds"], edges["start_upper_seconds"]), (9, 15))
            self.assertEqual((edges["end_lower_seconds"], edges["end_upper_seconds"]), (17, 23))
            self.assertEqual((edges["duration_lower_seconds"], edges["duration_upper_seconds"]), (2, 14))
            self.assertEqual(summary["observed_copy_subset_bytes_per_second"], 100)
            self.assertEqual(edges["last_observed_bytes"], 500)
            self.assertEqual(summary["copy_phase_count"], 1)

    def test_unobserved_phase_edges_remain_unknown(self):
        with tempfile.TemporaryDirectory() as directory:
            summary = self.analyze([row(10, 100), row(12, 300)], directory)
            edges = summary["first_copy_phase_edges"]
            self.assertIsNone(edges["start_lower_seconds"])
            self.assertIsNone(edges["end_upper_seconds"])
            self.assertIsNone(edges["duration_upper_seconds"])
            self.assertEqual(summary["observed_copy_subset_bytes_per_second"], 100)

    def test_file_entry_reset_does_not_invent_negative_rate(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = [row(0, 0), row(2, 200), row(4, 400), row(6, 600)]
            for sample, count in zip(samples, (0, 4, 1, 5)):
                sample["response"]["progress"]["copied_file_count"] = str(count)
            summary = self.analyze(samples, directory)
            with Path(directory, "curve-files.csv").open() as source:
                intervals = list(csv.DictReader(source))
            self.assertEqual(intervals[0]["file_work_entries_per_second"], "2.0")
            self.assertEqual(intervals[1]["reason"], "counter-reset")
            self.assertEqual(intervals[1]["file_work_entries_per_second"], "")
            self.assertEqual(intervals[2]["file_work_entries_per_second"], "2.0")
            self.assertEqual(summary["file_work_entry_counter_resets"], 1)
            self.assertEqual(summary["byte_counter_resets"], 0)

    def test_multiple_copy_epochs_have_no_single_subset_rate(self):
        with tempfile.TemporaryDirectory() as directory:
            samples = [row(0, 0), row(2, 200), row(4, 200, "JOB_PHASE_QUEUED"),
                       row(6, 200), row(8, 400), row(10, 400, "JOB_PHASE_FINALIZING_MEDIA")]
            summary = self.analyze(samples, directory)
            self.assertEqual(summary["copy_phase_count"], 2)
            self.assertIsNone(summary["observed_copy_subset_bytes_per_second"])

    def test_driver_owner_change_breaks_delta_even_when_counter_increases(self):
        with tempfile.TemporaryDirectory() as directory:
            values = [{"channel": "driver", "request_start_mono_ns": t * 10**9,
                       "request_end_mono_ns": t * 10**9, "response": {"pid": pid, "mount": "/mount",
                       "pages": {"0C": {"parameters": {"0": amount}}}}}
                      for t, pid, amount in ((0, 1, 0), (5, 2, 1000), (10, 2, 2000))]
            self.analyze(values, directory)
            with Path(directory, "curve-intervals.csv").open() as source:
                intervals = list(csv.DictReader(source))
            self.assertEqual(len(intervals), 1)
            self.assertEqual(float(intervals[0]["bytes"]), 1000)

    def test_milestones_retain_total_wait_extent(self):
        with tempfile.TemporaryDirectory() as directory:
            values = [{"channel": "milestone", "name": "admitted", "request_start_mono_ns": 0,
                       "request_end_mono_ns": 0}, row(20, 0), row(22, 100),
                      {"channel": "milestone", "name": "completed", "request_start_mono_ns": 100*10**9,
                       "request_end_mono_ns": 100*10**9}]
            summary = self.analyze(values, directory)
            self.assertEqual(summary["observation_start_seconds"], 0)
            self.assertEqual(summary["observation_end_seconds"], 100)
            with Path(directory, "curve-milestones.csv").open() as source:
                self.assertEqual([row["name"] for row in csv.DictReader(source)], ["admitted", "completed"])

    @unittest.skipUnless(importlib.util.find_spec("matplotlib"), "SVG rendering requires optional matplotlib")
    def test_plot_artifact_contains_real_curve_and_wait_extent(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.analyze([row(t, t * 1000000) for t in range(0, 202, 2)], directory, plot=True)
            svg = Path(result["artifacts"]["curve"]).read_text()
            self.assertIn("ACP supply", svg)
            self.assertIn("First qualifying", svg)


class SamplerTests(unittest.TestCase):
    def test_driver_off_and_resources_are_execution_host_commands(self):
        commands = []
        def remote(argv, timeout):
            commands.append(argv)
            return argv
        test = SimpleNamespace(install="/owned/install", url="http://example", remote_arguments=remote)
        def request(argv, timeout):
            return {"exit_code": 0, "stdout": json.dumps({"progress": {"copied_bytes": "0"}})}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "samples.jsonl"
            with metrics.PerformanceSampler(test, {"id": 7}, "/owned/fixture.py", "/owned/job/tape", path,
                                            driver=False, request=request, resource_helper_path="/owned/metrics.py"):
                time.sleep(0.04)
            rows = [json.loads(line) for line in path.read_text().splitlines()]
            self.assertEqual({value["channel"] for value in rows}, {"app", "resources"})
            self.assertTrue(all(value["request_end_mono_ns"] >= value["request_start_mono_ns"] for value in rows))
            self.assertFalse(any("sample" in argv for argv in commands))
            self.assertIn(["python3", "/owned/metrics.py", "resources", "/owned/install", "/owned/job/tape"], commands)

    def test_driver_block_does_not_block_app_polls(self):
        with tempfile.TemporaryDirectory() as directory:
            test = SimpleNamespace(install="/owned/install", url="http://example", remote_arguments=lambda argv, timeout: argv)
            def request(argv, timeout):
                if "sample" in argv:
                    time.sleep(0.2)
                return {"exit_code": 0, "stdout": "{}"}
            path = Path(directory) / "samples.jsonl"
            with metrics.PerformanceSampler(test, {"id": 1}, "/fixture", "/tape", path, request=request,
                                            app_period=0.03, driver_period=0.03):
                time.sleep(0.16)
            rows = [json.loads(line) for line in path.read_text().splitlines()]
            self.assertGreater(sum(row["channel"] == "app" for row in rows), 2)
            self.assertEqual(sum(row["channel"] == "driver" for row in rows), 1)
            self.assertTrue(all(row["period_seconds"] == 0.03 for row in rows))

    def test_optional_periods_preserve_defaults_and_reject_zero(self):
        test = SimpleNamespace()
        sampler = metrics.PerformanceSampler(test, {"id": 1}, "/fixture", "/tape", "/output")
        self.assertEqual((sampler.app_period, sampler.driver_period), (2, 5))
        sampler = metrics.PerformanceSampler(test, {"id": 1}, "/fixture", "/tape", "/output",
                                              app_period=1, driver_period=1)
        self.assertEqual((sampler.app_period, sampler.driver_period), (1, 1))
        with self.assertRaises(ValueError):
            metrics.PerformanceSampler(test, {"id": 1}, "/fixture", "/tape", "/output", app_period=0)

    def test_resource_parser_scopes_processes_and_rejects_ambiguous_ownership(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            proc = root / "proc"
            proc.mkdir()
            (proc / "stat").write_text("cpu 10 2 3 40 5 6 7 8 0 0\n")
            (proc / "diskstats").write_text("8 0 sda 1 2 3 4\n")
            (proc / "meminfo").write_text("MemAvailable: 100 kB\nDirty: 2 kB\n")
            def add(pid, argv):
                process = proc / str(pid)
                process.mkdir()
                (process / "cmdline").write_bytes(b"\0".join(value.encode() for value in argv))
                fields = ["0"] * 20
                fields[0], fields[7], fields[9], fields[11], fields[12], fields[19] = "S", "72", "3", "123", "45", "600"
                (process / "stat").write_text(str(pid) + " (worker with ) parenthesis) " + " ".join(fields))
                (process / "status").write_text("VmRSS: 1000 kB\nVmHWM: 2000 kB\n")
                (process / "io").write_text("read_bytes: 1234\nwrite_bytes: 5678\n")
                (process / "fd").mkdir()
            add(20, ["/owned/yatm-httpd"])
            add(21, ["/opt/openltfs/bin/ltfs", "-o", "work_directory=/owned/tape", "/mount"])
            add(22, ["/opt/openltfs/bin/ltfs", "-o", "work_directory=/other/tape", "/other"])
            def mapped(value):
                return root / str(value).lstrip("/") if str(value).startswith("/proc") else Path(value)
            with mock.patch.object(metrics, "Path", side_effect=mapped), mock.patch.object(metrics.os, "sysconf", return_value=100):
                result = metrics.resource_sample("/owned", "/owned/tape")
                self.assertEqual(set(result["processes"]), {"yatm", "ltfs"})
                self.assertEqual(result["processes"]["yatm"]["cpu_ticks"], 168)
                self.assertEqual(result["processes"]["yatm"]["minflt"], 72)
                self.assertEqual(result["processes"]["yatm"]["majflt"], 3)
                self.assertEqual(result["processes"]["ltfs"]["start_ticks"], 600)
                self.assertEqual(result["host"]["cpu_total_ticks"], 81)
                add(23, ["/owned/yatm-httpd"])
                add(24, ["/owned/yatm-httpd"])
                result = metrics.resource_sample("/owned", "/owned/tape")
                self.assertNotIn("yatm", result["processes"])
                self.assertTrue(any("ambiguous" in error for error in result["errors"]))

    def test_writer_failure_is_reported(self):
        test = SimpleNamespace(install="/owned", url="http://example", remote_arguments=lambda argv, timeout: argv)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "exists.jsonl"
            path.write_text("preserve")
            with self.assertRaises(RuntimeError):
                with metrics.PerformanceSampler(test, {"id": 1}, "/fixture", "/tape", path, driver=False,
                    request=lambda argv, timeout: {"exit_code": 0, "stdout": "{}"}):
                    time.sleep(0.02)
            self.assertEqual(path.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
