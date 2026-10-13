"""Synthetic capacity observations only; no service, NAS or device access."""

import csv
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import physical_capacity_analysis as analysis


EPOCH = 1800000000000000123


def page(number, values, width=4, control=0x60):
    body = b"".join(code.to_bytes(2, "big") + bytes((control, width)) + value.to_bytes(width, "big")
                    for code, value in values.items())
    return (bytes((number, 0)) + len(body).to_bytes(2, "big") + body).hex()


def sample(channel, seconds, response, period=5, duration=0.2):
    start = EPOCH + round(seconds * 10**9)
    return {"channel": channel, "job_id": "9", "period_seconds": period, "exit_code": 0,
            "request_start_wall_ns": start, "request_end_wall_ns": start + round(duration * 10**9),
            "request_start_mono_ns": start - EPOCH, "request_end_mono_ns": start - EPOCH + round(duration * 10**9),
            "response": response}


def driver(seconds, *, available=4000000, remaining=10, eod=10, ew=20, head=3,
           received=100, written=50, retries=0, alerts=(), pid=42, period=5):
    sequential = bytes.fromhex(page(12, {0: received, 1: written, 2: 0, 3: 0}, width=8))[4:]
    sequential += bytes.fromhex(page(12, {4: eod, 5: ew, 6: 1, 7: head, 8: 128}))[4:]
    raw = (b"\x0c\0" + len(sequential).to_bytes(2, "big") + sequential).hex()
    readings = {
        "31": page(49, {1: 2, 2: remaining, 3: 4, 4: 100}), "0C": raw,
        "02": page(2, {0: 0, 1: retries, 2: retries, 3: retries, 4: retries * 2, 5: 20, 6: 0}, control=0x40),
        "34": page(52, {0: 7}, width=2),
        "17": page(23, {0x0204: 0xFFFFFFFF}),
        "2E": page(46, {i: int(i in alerts) for i in range(1, 65)}, width=1, control=0x40),
    }
    return sample("driver", seconds, {
        "status": "mounted", "pid": pid, "mount": "/test/mount", "argv": ["ltfs", "work_directory=/test/job"],
        "pages": {key: {"raw": value, "request_start_ns": EPOCH + round(seconds * 10**9) + 11,
                        "request_end_ns": EPOCH + round(seconds * 10**9) + 91} for key, value in readings.items()},
        "statvfs": {"available_bytes": available, "fragment_bytes": 4096, "timestamp_ns": EPOCH + round(seconds * 10**9) + 101},
    }, period)


def app(seconds, copied, phase="JOB_PHASE_COPYING_TO_MEDIA", period=5):
    return sample("app", seconds, {"progress": {"copied_bytes": str(copied), "stage": {"phase": phase}}}, period)


class CapacityAnalysisTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.raw = self.root / "archive-samples.jsonl"
        self.prefix = self.root / "capacity"

    def analyze(self, rows, plot=False):
        self.raw.write_text("".join(json.dumps(row) + "\n" for row in rows))
        return analysis.analyze_samples(self.raw, self.prefix, plot=plot)

    def csv(self, name):
        with Path(str(self.prefix) + "-" + name + ".csv").open() as source:
            return list(csv.DictReader(source))

    def test_units_head_eod_negative_collapse_and_exact_timestamps(self):
        result = self.analyze([driver(0), app(0, 0),
                               driver(5, available=0, remaining=3, eod=21, head=8, received=600, written=250), app(5, 500)])
        metrics = result["metrics"]
        self.assertEqual(metrics["p31_p1_remaining"]["first"]["value"], 10 * 1024**2)
        self.assertEqual(metrics["ew_minus_eod"]["first"]["value"], 10 * 10**6)
        self.assertEqual(metrics["ew_minus_eod"]["min"]["value"], -10**6)
        self.assertEqual(metrics["head_position"]["first"]["value"], 3 * 10**6)
        self.assertEqual(metrics["eod"]["first"]["value"], 10 * 10**6)
        collapse = result["first_collapse"]["filesystem_available"]
        self.assertEqual(collapse["start_ns"], EPOCH)
        self.assertEqual(collapse["end_ns"], EPOCH + 5200000000)
        observed = next(row for row in self.csv("observations") if row["metric"] == "p31_p1_remaining")
        self.assertEqual(observed["start_ns"], str(EPOCH))
        self.assertEqual(observed["host_start_ns"], str(EPOCH + 11))
        received = [row for row in self.csv("intervals") if row["metric"] == "drive_received"][-1]
        self.assertEqual(float(received["delta"]), 500)
        self.assertEqual(float(received["rate_per_second"]), 100)
        self.assertEqual(float(received["uncertainty_seconds"]), 0.2)

    def test_variable_period_and_gaps_use_actual_time(self):
        result = self.analyze([app(0, 0, period=2), app(3, 300, period=4),
                               app(10, 1000, period=4), app(30, 3000, period=4), app(34, 3000, period=4)])
        spans = self.csv("intervals")
        self.assertEqual([row["reason"] for row in spans], ["first-observation", "", "", "sample-gap", ""])
        self.assertEqual(float(spans[1]["rate_per_second"]), 100)
        self.assertEqual(float(spans[2]["elapsed_seconds"]), 7)
        self.assertEqual(spans[3]["delta"], "")
        self.assertEqual(result["stalls"]["count"], 1)
        self.assertEqual(result["stalls"]["observed_seconds"], 4)

    def test_owner_retained_alerts_take_precedence_and_errors_stay_unknown(self):
        row = driver(0, alerts=())
        row["response"]["storage_alert"] = {"value": "0x8000000000000001"}
        result = self.analyze([row])
        self.assertEqual(result["metrics"]["tapealert_active"]["first"]["value"], 2)
        row["response"]["storage_alert"] = {"error": "read failed"}
        result = self.analyze([row])
        self.assertEqual(result["metrics"]["tapealert_active"]["unknown"], 1)
        self.assertIsNone(result["metrics"]["tapealert_active"]["first"])

    def test_starting_process_does_not_turn_host_disk_space_into_tape_capacity(self):
        row = driver(0, available=30 * 1024**3)
        row["response"]["pages"]["31"] = {"error": "xattr unavailable before mount"}
        row["response"]["pages"]["0C"] = {"error": "xattr unavailable before mount"}
        result = self.analyze([row])
        self.assertIsNone(result["metrics"]["filesystem_available"]["first"])
        observed = next(item for item in self.csv("observations") if item["metric"] == "filesystem_available")
        self.assertEqual(observed["status"], "mount-not-established-by-capacity-reads")

    def test_raw_required_missing_page_breaks_deltas_and_retains_evidence(self):
        missing = driver(5, received=200)
        missing["response"]["pages"]["0C"] = {"parameters": {"0": 200, "1": 100, "4": 10, "5": 20}}
        missing["response"]["pages"]["31"] = {"error": "unsupported", "errno": 95}
        self.analyze([driver(0), missing, driver(10, received=300)])
        spans = [row for row in self.csv("intervals") if row["metric"] == "drive_received"]
        self.assertEqual([row["delta"] for row in spans], ["", "", ""])
        records = self.csv("pages")
        self.assertTrue(any(row["status"] == "missing-raw" for row in records))
        self.assertTrue(any(json.loads(row["reading"]).get("errno") == 95 for row in records))
        raw17 = next(row for row in records if row["page"] == "17")
        self.assertEqual(json.loads(raw17["parameters"])["516"]["raw"], "ffffffff")
        self.assertFalse(any(row["metric"].startswith("p17") for row in self.csv("observations")))

    def test_reload_reset_partition_and_phase_boundaries(self):
        self.analyze([driver(0), driver(5, pid=43, received=200),
                      driver(10, pid=43, received=10, written=10), driver(15, pid=43, received=20, ew=30),
                      app(0, 100), app(5, 50), app(10, 1000, phase="JOB_PHASE_FINALIZING_MEDIA")])
        spans = self.csv("intervals")
        received = [row["reason"] for row in spans if row["metric"] == "drive_received"]
        self.assertEqual(received, ["first-observation", "owner-change", "counter-reset", "partition-coordinate-change"])
        copied = [row["reason"] for row in spans if row["metric"] == "app_copied"]
        self.assertEqual(copied, ["first-observation", "counter-reset", "phase-boundary"])

    def test_unknown_flags_all_ones_and_malformed_raw_are_not_zero(self):
        row = driver(0, remaining=0xFFFFFFFF, eod=0xFFFFFFFF)
        row["response"]["pages"]["2E"]["raw"] = page(46, {1: 2}, width=1, control=0x40)
        row["response"]["pages"]["02"]["raw"] = "020000080000400401"
        result = self.analyze([row])
        for metric in ("p31_p1_remaining", "ew_minus_eod", "eod", "tapealert_active", "write_1"):
            self.assertIsNone(result["metrics"][metric]["min"])
        self.assertIn("unknown-flag-layout", [row["status"] for row in self.csv("observations")])
        with self.assertRaises(ValueError):
            analysis.parameters("2e00000a00014001000001400101", 46)

    def test_error_temporal_overlap_and_tapealert_not_cumulative(self):
        failure = sample("milestone", 5, {})
        failure.update(name="job-settled", detail={"error": "target full"})
        result = self.analyze([driver(0), app(0, 100),
                              driver(5, available=0, retries=2, alerts=(2, 6)), app(5, 100),
                              driver(10, retries=2, alerts=(), received=200), failure])
        overlap = result["temporal_error_overlap"]["filesystem_available"]
        self.assertEqual(overlap["error_increments"], 4)
        self.assertEqual(overlap["alert_reads"], 1)
        self.assertEqual(overlap["operation_errors"], 1)
        self.assertEqual(result["temporal_error_overlap"]["first_app_stall"]["alert_reads"], 1)
        self.assertEqual(result["metrics"]["tapealert_active"]["last"]["value"], 0)
        self.assertEqual(result["metrics"]["repositions"]["last"]["value"], 7)
        self.assertFalse(any(row["kind"] == "error-increment" and row["metric"] == "repositions" for row in self.csv("events")))

    def test_time_uncertainty_clock_jump_and_missing_period(self):
        rows = [app(0, 0), app(5, 100), app(10, 200), app(15, 300)]
        rows[1]["request_start_wall_ns"] = EPOCH + 100000000
        rows[1]["request_start_mono_ns"] = 100000000
        rows[2]["request_start_wall_ns"] += 10**9
        rows[2]["request_end_wall_ns"] += 10**9
        rows[3].pop("period_seconds")
        self.analyze(rows)
        reasons = [row["reason"] for row in self.csv("intervals")]
        self.assertEqual(reasons[1], "overlapping-or-reversed-time")
        self.assertEqual(reasons[2:], ["clock-discontinuity", "clock-discontinuity"])
        result = self.analyze([app(0, 0), {**app(5, 100), "period_seconds": None}])
        self.assertIn("unknown-period", result["metrics"]["app_copied"]["breaks"])

    def test_cli_outputs_plain_json_and_csv(self):
        self.raw.write_text(json.dumps(driver(0)) + "\n")
        result = subprocess.run([sys.executable, "-B", analysis.__file__, str(self.raw), "--output", str(self.prefix), "--no-plot"],
                                capture_output=True, text=True, check=True)
        summary = json.loads(result.stdout)
        self.assertEqual(summary["plot"], "disabled")
        self.assertEqual(summary, json.loads(Path(str(self.prefix) + "-summary.json").read_text()))
        for artifact in summary["artifacts"].values():
            self.assertTrue(Path(artifact).is_file())

    def test_job_error_uses_its_timezone_and_keeps_second_precision(self):
        log = self.root / "job.log"
        log.write_text('time="2026-10-09T17:44:58+08:00" level=info msg="copying"\n'
                       'time="2026-10-09T17:44:59+08:00" level=error msg="target full"\n')
        self.raw.write_text(json.dumps(driver(0)) + "\n")
        result = analysis.analyze_samples(self.raw, self.prefix, plot=False, job_log=log)
        self.assertEqual(result["first_job_error"], {
            "line": 2, "start_ns": 1791539099000000000, "end_ns": 1791539100000000000})
        event = next(row for row in self.csv("events") if row["kind"] == "job-error")
        self.assertIn('msg="target full"', event["detail"])
        self.assertEqual(event["line"], "2")

    def test_job_log_is_never_overwritten_by_analysis(self):
        self.raw.write_text(json.dumps(driver(0)) + "\n")
        log = self.root / "capacity-events.csv"
        log.write_text("keep original log")
        with self.assertRaisesRegex(ValueError, "overwrite"):
            analysis.analyze_samples(self.raw, self.prefix, plot=False, job_log=log)
        self.assertEqual(log.read_text(), "keep original log")

    def test_png_symlinks_cannot_overwrite_samples_or_job_log(self):
        self.raw.write_text(json.dumps(driver(0)) + "\n")
        log = self.root / "job.log"
        log.write_text('time="2026-10-09T17:44:59+08:00" level=error msg="target full"\n')
        for suffix in ("-curves.png", "-boundary.png"):
            for source in (self.raw, log):
                with self.subTest(suffix=suffix, source=source):
                    destination = Path(str(self.prefix) + suffix)
                    destination.symlink_to(source)
                    before = source.read_bytes()
                    try:
                        with self.assertRaisesRegex(ValueError, "overwrite"):
                            analysis.analyze_samples(self.raw, self.prefix, job_log=log)
                        self.assertEqual(source.read_bytes(), before)
                        self.assertFalse(Path(str(self.prefix) + "-observations.csv").exists())
                    finally:
                        destination.unlink()

    def test_gap_at_zero_is_not_a_measured_collapse_and_unmounted_breaks_counter(self):
        result = self.analyze([driver(0), driver(20, available=0),
                              sample("driver", 25, {"status": "not-mounted"}), driver(30, received=1000)])
        self.assertNotIn("filesystem_available", result["first_collapse"])
        self.assertEqual(result["first_nonpositive"]["filesystem_available"]["value"], 0)
        spans = [row for row in self.csv("intervals") if row["metric"] == "drive_received"]
        self.assertTrue(all(row["delta"] == "" for row in spans))

    def test_rejects_input_overwrite_and_invalid_json(self):
        self.raw.write_text("not JSON\n")
        with self.assertRaisesRegex(ValueError, "line 1"):
            analysis.analyze_samples(self.raw, self.prefix, plot=False)
        colliding = self.root / "capacity-summary.json"
        colliding.write_text("keep")
        with self.assertRaisesRegex(ValueError, "overwrite"):
            analysis.analyze_samples(colliding, self.prefix, plot=False)
        self.assertEqual(colliding.read_text(), "keep")

    @unittest.skipUnless(importlib.util.find_spec("matplotlib"), "SVG requires optional matplotlib")
    def test_svg_has_capacity_and_distinct_position_labels(self):
        result = self.analyze([driver(0), app(0, 0), driver(5, available=0), app(5, 100)], plot=True)
        self.assertEqual(result["plot"], "written")
        svg = Path(result["artifacts"]["curves"]).read_text()
        for label in ("Filesystem vs raw P1", "current head position", "recorded EOD", "first observed &lt;=0"):
            self.assertIn(label, svg)

    @unittest.skipUnless(importlib.util.find_spec("matplotlib"), "SVG requires optional matplotlib")
    def test_job_error_generates_a_boundary_detail_without_changing_raw_data(self):
        log = self.root / "job.log"
        timestamp = datetime.fromtimestamp(EPOCH // 10**9 + 5, timezone.utc).strftime("%Y-%m-%dT%H:%M:%S%z")
        log.write_text(f'time="{timestamp}" level=error msg="target full"\n')
        self.raw.write_text("".join(json.dumps(driver(at)) + "\n" for at in (0, 5, 10)))
        before = self.raw.read_bytes()
        result = analysis.analyze_samples(self.raw, self.prefix, plot=True, job_log=log)
        self.assertIn("Capacity observations around the first Job error",
                      Path(result["artifacts"]["boundary"]).read_text())
        self.assertTrue(Path(result["artifacts"]["boundary_png"]).is_file())
        self.assertEqual(self.raw.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
