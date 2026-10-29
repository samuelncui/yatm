"""Semantic curve/window and mocked sampler checks; no services or Tape access."""

import csv
import importlib.util
import json
import math
import os
import sys
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
        import matplotlib.pyplot as plt

        with tempfile.TemporaryDirectory() as directory:
            # A complete day retains preparation/finalization waits around the copying observations.
            paths = {name: Path(directory, "synthetic-24h-" + name + suffix) for name, suffix in
                     (("intervals", ".csv"), ("bins", ".csv"), ("files", ".csv"), ("driver", ".csv"),
                      ("resources", ".csv"), ("observations", ".csv"), ("milestones", ".csv"), ("curve", ".svg"))}
            duration, copy_start, copy_end, origin, gap = 86400, 120, 86280, 122, 43200

            def write(name, fields, rows):
                with paths[name].open("w", newline="") as output:
                    writer = csv.writer(output)
                    writer.writerow(fields)
                    writer.writerows(rows)

            write("intervals", ["series", "start", "end", "rate"],
                  ((series, start, start + period, "" if start == gap else 1000000)
                   for series, period, begin, end in [("acp", 2, copy_start, copy_end)] +
                   [("0C:" + str(code), 5, 0, duration) for code in range(4)]
                   for start in range(begin, end, period)))
            write("files", ["start", "end", "file_work_entries_per_second"],
                  ((start, start + 2, "" if start == gap else 1) for start in range(copy_start, copy_end, 2)))

            def bins():
                for width in (10, 30):
                    for end in range(width, duration + 1, width):
                        known = copy_start < end <= copy_end and not end - width <= gap < end
                        yield width, end, 1000000 if known else "", int(known)

            write("bins", ["width_seconds", "end_seconds", "bytes_per_second", "coverage"],
                  bins())

            # Saved protocol shapes have ten 0C and five 34 parameters; only four counters become rates.
            write("driver", ["time_seconds", "page", "parameter", "value", "status"],
                  ((seconds, page, code, 0, "observed") for seconds in range(0, duration + 1, 5)
                   for page, codes in (("0C", [*range(9), 256]), ("34", range(5))) for code in codes))
            process = ("cpu_ticks", "user_ticks", "system_ticks", "VmRSS_kib", "VmHWM_kib", "rchar", "wchar",
                       "syscr", "syscw", "read_bytes", "write_bytes", "cancelled_write_bytes", "open_descriptors")
            host = ("cpu_total_ticks", "cpu_idle_ticks", "cpu_iowait_ticks", "clock_ticks_per_second", "MemAvailable_kib",
                    "MemFree_kib", "Cached_kib", "Dirty_kib", "Writeback_kib")
            write("resources", ["time_seconds", "role", "pid", "start_ticks", "metric", "value"],
                  ((seconds, role, 1234, 5678, metric, 100 if metric == "clock_ticks_per_second" else
                    65536 if metric == "VmRSS_kib" else seconds * (100 if metric == "cpu_ticks" else 1000000))
                   for seconds in range(0, duration + 1, 5) for role, names in (("executor", process), ("host", host))
                   for metric in names))
            write("observations", ["time_seconds", "phase", "status"],
                  ((seconds, phase, "observed") for seconds, phase in
                   ((0, "JOB_PHASE_PREPARING_MEDIA"), (copy_start, "JOB_PHASE_COPYING_TO_MEDIA"),
                    (copy_end, "JOB_PHASE_FINALIZING_MEDIA"))))
            write("milestones", ["time_seconds", "name"], ((0, "admitted"), (duration, "settled")))

            # Render the actual figure once, inspecting its axes before the normal Close releases it.
            summary = {"origin_seconds": origin, "observation_start_seconds": 0, "observation_end_seconds": duration,
                       "first_stable_window": {"start_seconds": 122, "end_seconds": 302}}
            close = plt.close
            figures = []

            def inspect(figure):
                if hasattr(figure, "axes"):
                    figures.append(figure)
                    self.assertEqual(len(figure.axes), 7)
                    for axis in figure.axes:
                        self.assertEqual(axis.get_xlim(), (-origin, duration - origin))
                    line = next(line for line in figure.axes[0].lines if line.get_label() == "Raw ACP handoff intervals")
                    index = (gap - copy_start) // 2 * 3
                    self.assertTrue(math.isnan(line.get_ydata()[index]))
                    self.assertTrue(math.isnan(line.get_ydata()[index + 1]))
                    self.assertEqual(line.get_ydata()[index - 2], 1)
                    self.assertEqual(line.get_ydata()[index + 3], 1)
                    self.assertTrue(math.isnan(figure.axes[6].lines[0].get_ydata()[index]))
                    finite = [value for value in line.get_xdata() if math.isfinite(value)]
                    self.assertGreater(min(finite), -origin)
                    self.assertLess(max(finite), duration - origin)
                close(figure)

            with mock.patch.object(plt, "close", side_effect=inspect):
                metrics.plot_curves(paths, summary)
            self.assertEqual(len(figures), 1)
            for name, count in (("intervals", 112200), ("files", 43080), ("driver", 259215),
                                ("bins", 11520), ("resources", 380182)):
                with paths[name].open() as source:
                    self.assertEqual(sum(1 for _ in source) - 1, count)
            svg = paths["curve"].read_text()
            self.assertIn("ACP supply", svg)
            self.assertIn("First qualifying", svg)
            self.assertIn("settled", svg)
            self.assertIn("Page34/0", svg)


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


class ExplicitResourceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.proc = Path(self.directory.name) / "proc"
        self.proc.mkdir()
        (self.proc / "stat").write_text("cpu 10 2 3 40 5 6 7 8 0 0\n")
        (self.proc / "diskstats").write_text("8 0 sda 1 2 3 4\n")
        (self.proc / "meminfo").write_text("MemAvailable: 100 kB\nDirty: 2 kB\n")
        self.process = self.proc / "20"
        self.process.mkdir()
        self.executable = "/owned/executor.test"
        (self.process / "exe").symlink_to(self.executable)
        fields = ["0"] * 20
        fields[0], fields[7], fields[9], fields[11], fields[12], fields[19] = "S", "72", "3", "123", "45", "600"
        self.stat = "20 (executor with ) parenthesis) " + " ".join(fields)
        (self.process / "stat").write_text(self.stat)
        (self.process / "status").write_text("VmRSS: 1000 kB\nVmHWM: 2000 kB\n")
        (self.process / "io").write_text("read_bytes: 1234\nwrite_bytes: 5678\n")
        (self.process / "fd").mkdir()
        (self.process / "fd" / "0").touch()
        # An unrelated process must never be inspected in explicit mode.
        (self.proc / "21").mkdir()
        def mapped(value):
            value = str(value)
            return self.proc / value.removeprefix("/proc/") if value.startswith("/proc/") else self.proc if value == "/proc" else Path(value)
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(metrics, "Path", side_effect=mapped).start()
        mock.patch.object(metrics.os, "sysconf", return_value=100).start()
        original_stat = metrics.os.stat
        def stat(path, *args, **kwargs):
            if str(path) == "/owned":
                return SimpleNamespace(st_dev=metrics.os.makedev(8, 3))
            return original_stat(path, *args, **kwargs)
        mock.patch.object(metrics.os, "stat", side_effect=stat).start()
        mock.patch.object(metrics.os, "statvfs", return_value=SimpleNamespace(
            f_blocks=100, f_bfree=40, f_bavail=30, f_frsize=4096)).start()

    def sample(self, **kwargs):
        return metrics.resource_sample(pid=20, executable=self.executable, start_ticks=600, **kwargs)

    def test_valid_explicit_identity_uses_shared_counters_only_for_executor(self):
        with mock.patch.object(Path, "glob", side_effect=AssertionError("Explicit sampling must not scan processes")):
            result = self.sample()
        self.assertEqual(result["errors"], [])
        self.assertEqual(set(result["processes"]), {"executor"})
        self.assertEqual(result["processes"]["executor"], {
            "pid": 20, "cpu_ticks": 168, "start_ticks": 600, "minflt": 72, "majflt": 3,
            "VmRSS_kib": 1000, "VmHWM_kib": 2000, "read_bytes": 1234,
            "write_bytes": 5678, "open_descriptors": 1})
        self.assertEqual(result["host"]["cpu_total_ticks"], 81)
        self.assertEqual(result["diskstats"], "8 0 sda 1 2 3 4\n")
        self.assertEqual(result["filesystem"], {"path": "/owned", "device_major": 8,
            "device_minor": 3, "total_bytes": 409600, "free_bytes": 163840, "available_bytes": 122880})

    def test_foreign_executable_reused_pid_and_exited_process_are_absent(self):
        for change in ("executable", "start_ticks", "exited"):
            with self.subTest(change=change):
                (self.process / "stat").write_text(self.stat)
                (self.process / "exe").unlink(missing_ok=True)
                if change != "exited":
                    (self.process / "exe").symlink_to("/foreign/executor.test" if change == "executable" else self.executable)
                if change == "start_ticks":
                    (self.process / "stat").write_text(self.stat.removesuffix("600") + "601")
                result = self.sample()
                self.assertEqual(result["processes"], {})
                self.assertTrue(result["errors"])

    def test_identity_change_or_exit_during_collection_discards_sample(self):
        original = metrics._proc_text
        for change in ("executable", "start_ticks", "exited"):
            with self.subTest(change=change):
                (self.process / "stat").write_text(self.stat)
                (self.process / "exe").unlink(missing_ok=True)
                (self.process / "exe").symlink_to(self.executable)
                def read(path, limit=1024 * 1024):
                    value = original(path, limit)
                    if path.name == "io":
                        if change == "start_ticks":
                            (self.process / "stat").write_text(self.stat.removesuffix("600") + "601")
                        else:
                            (self.process / "exe").unlink()
                            if change == "executable":
                                (self.process / "exe").symlink_to("/foreign/executor.test")
                    return value
                with mock.patch.object(metrics, "_proc_text", side_effect=read):
                    result = self.sample()
                self.assertEqual(result["processes"], {})
                self.assertTrue(result["errors"])

    def test_unknown_filesystem_facts_remain_absent(self):
        for target in ("stat", "statvfs"):
            with self.subTest(target=target), mock.patch.object(metrics.os, target, side_effect=OSError("unavailable")):
                result = self.sample()
            self.assertNotIn("filesystem", result)
            self.assertIn("executor", result["processes"])
            self.assertTrue(any("Executor filesystem: unavailable" in error for error in result["errors"]))

    def test_full_identity_and_exclusive_modes_are_required(self):
        for kwargs in ({"pid": 20}, {"executable": self.executable}, {"start_ticks": 600},
                       {"pid": 20, "executable": self.executable},
                       {"pid": 0, "executable": self.executable, "start_ticks": 600},
                       {"pid": 20, "executable": "relative", "start_ticks": 600},
                       {"pid": 20, "executable": self.executable, "start_ticks": -1},
                       {"pid": 20, "executable": self.executable, "start_ticks": 600, "install": "/owned", "tape_dir": "/tape"},
                       {}, {"install": "/owned"}):
            with self.subTest(kwargs=kwargs), self.assertRaises(ValueError):
                metrics.resource_sample(**kwargs)

    def test_resource_read_and_descriptor_limits_fail_without_partial_process(self):
        (self.process / "io").write_text("x" * (1024 * 1024 + 1))
        result = self.sample()
        self.assertEqual(result["processes"], {})
        self.assertTrue(any("read limit" in error for error in result["errors"]))
        (self.process / "io").write_text("read_bytes: 1234\n")
        with mock.patch.object(Path, "iterdir", return_value=iter(range(65537))):
            result = self.sample()
        self.assertEqual(result["processes"], {})
        self.assertTrue(any("descriptor count" in error for error in result["errors"]))

    def test_cli_preserves_positional_mode_and_accepts_explicit_identity(self):
        for argv, expected in ((["resources", "/owned", "/tape"],
                                (("/owned", "/tape"), {"pid": None, "executable": None, "start_ticks": None})),
                               (["resources", "--pid", "20", "--executable", self.executable, "--start-ticks", "600"],
                                ((None, None), {"pid": 20, "executable": self.executable, "start_ticks": 600}))):
            with self.subTest(argv=argv), mock.patch.object(metrics, "resource_sample", return_value={}) as sample, mock.patch("builtins.print") as output:
                metrics.main(argv)
                self.assertEqual(sample.call_args, mock.call(*expected[0], **expected[1]))
                output.assert_called_once_with("{}")

    def test_cli_rejects_missing_mixed_and_invalid_identity(self):
        for argv in (["resources"], ["resources", "--pid", "20"],
                     ["resources", "/owned", "/tape", "--pid", "20", "--executable", self.executable, "--start-ticks", "600"],
                     ["resources", "--pid", "not-an-int"],
                     ["resources", "--pid", "20", "--executable", self.executable, "--start-ticks", "-1"]):
            with self.subTest(argv=argv), mock.patch("sys.stderr"), self.assertRaises(SystemExit) as error:
                metrics.main(argv)
            self.assertEqual(error.exception.code, 2)


class NativeResourceTests(unittest.TestCase):
    @unittest.skipUnless(sys.platform == "linux", "Native resource sampling requires Linux /proc")
    def test_own_process_identity_counters_and_filesystem(self):
        pid = os.getpid()
        executable = os.readlink("/proc/self/exe")
        fields = Path("/proc", str(pid), "stat").read_text().rsplit(")", 1)[1].split()
        start_ticks = int(fields[19])
        result = metrics.resource_sample(pid=pid, executable=executable, start_ticks=start_ticks)
        self.assertEqual(result["errors"], [])
        self.assertEqual(set(result["processes"]), {"executor"})
        process = result["processes"]["executor"]
        self.assertEqual((process["pid"], process["start_ticks"]), (pid, start_ticks))
        for name in ("cpu_ticks", "minflt", "majflt", "read_bytes", "write_bytes"):
            self.assertGreaterEqual(process[name], 0)
        self.assertGreater(process["VmRSS_kib"], 0)
        self.assertGreater(process["open_descriptors"], 0)
        directory = os.path.dirname(executable)
        device = os.stat(directory).st_dev
        filesystem = result["filesystem"]
        self.assertEqual(filesystem["path"], directory)
        self.assertEqual((filesystem["device_major"], filesystem["device_minor"]),
                         (os.major(device), os.minor(device)))
        capacity = os.statvfs(directory)
        self.assertEqual(filesystem["total_bytes"], capacity.f_blocks * capacity.f_frsize)
        for name in ("free_bytes", "available_bytes"):
            self.assertGreaterEqual(filesystem[name], 0)
            self.assertLessEqual(filesystem[name], filesystem["total_bytes"])
        self.assertEqual(result["host"]["clock_ticks_per_second"], os.sysconf("SC_CLK_TCK"))
        self.assertTrue(result["diskstats"].strip())


if __name__ == "__main__":
    unittest.main()
