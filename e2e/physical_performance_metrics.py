"""Bounded measurement of ACP handoff and drive proxies, never physical commitment."""

import argparse
import csv
from collections import deque
import json
import math
import os
from pathlib import Path
import queue
import threading
import time


COPY_PHASES = {"JOB_PHASE_COPYING_TO_MEDIA", "JOB_PHASE_COPYING_FROM_MEDIA"}
APP_PERIOD = 2
DRIVER_PERIOD = 5


class PerformanceSampler:
    """Independent bounded pollers feeding one JSONL writer; no device ownership changes."""

    def __init__(self, test, job, helper_path, tape_dir, output_path, driver=True, request=None, resource_helper_path=None,
                 app_period=APP_PERIOD, driver_period=DRIVER_PERIOD):
        self.test, self.job = test, job
        self.helper_path, self.tape_dir = helper_path, tape_dir
        self.output_path = Path(output_path)
        self.driver = driver
        self.resource_helper_path = resource_helper_path
        if app_period <= 0 or driver_period <= 0:
            raise ValueError("Sampler periods must be positive")
        self.app_period, self.driver_period = app_period, driver_period
        self.request = request or self._request
        self.stop = threading.Event()
        self.rows = queue.Queue(maxsize=32)
        self.error = None
        self.threads = []

    @staticmethod
    def _request(argv, timeout):
        # Reuse existing process-group timeout/cleanup without an import cycle at load time.
        from package_physical_performance import sample_process
        return sample_process(argv, timeout=timeout)

    def _put(self, row):
        while self.error is None:
            try:
                self.rows.put(row, timeout=0.1)
                return
            except queue.Full:
                continue
        raise RuntimeError("Performance evidence writer failed") from self.error

    def _write(self):
        try:
            with self.output_path.open("x") as output:
                while True:
                    row = self.rows.get()
                    if row is None:
                        return
                    output.write(json.dumps(row, separators=(",", ":")) + "\n")
                    output.flush()
        except Exception as error:
            self.error = error
            self.stop.set()

    def _poll(self, channel, period, argv, timeout):
        deadline = time.monotonic()
        while not self.stop.is_set():
            row = {"channel": channel, "period_seconds": period, "job_id": str(self.job["id"]),
                   "request_start_wall_ns": time.time_ns(), "request_start_mono_ns": time.monotonic_ns()}
            try:
                result = self.request(argv, timeout)
                row.update(exit_code=result.get("exit_code"), timed_out=result.get("timed_out", False))
                if row["exit_code"] == 0 and not row["timed_out"]:
                    row["response"] = json.loads(result["stdout"])
                else:
                    row["error"] = result.get("stderr", "Request failed")
            except Exception as error:
                row["error"] = str(error)
            row.update(request_end_wall_ns=time.time_ns(), request_end_mono_ns=time.monotonic_ns())
            try:
                self._put(row)
            except RuntimeError:
                return
            # Skip missed deadlines; never queue another request behind a blocked request.
            deadline = max(deadline + period, time.monotonic())
            if self.stop.wait(max(0, deadline - time.monotonic())):
                return

    def __enter__(self):
        self.output_path.parent.mkdir(parents=True, exist_ok=True)
        self.writer = threading.Thread(target=self._write, name="performance-evidence", daemon=True)
        self.writer.start()
        commands = [("app", self.app_period, self.test.remote_arguments(
            [self.test.install + "/yatm-cli", "--server", self.test.url, "--timeout", "3s",
             "job", "progress", str(self.job["id"])], 4), 5)]
        if self.driver:
            commands.append(("driver", self.driver_period, self.test.remote_arguments(
                ["python3", self.helper_path, "sample", self.tape_dir], 15), 20))
        if self.resource_helper_path:
            commands.append(("resources", DRIVER_PERIOD, self.test.remote_arguments(
                ["python3", self.resource_helper_path, "resources", self.test.install, self.tape_dir], 4), 5))
        for args in commands:
            thread = threading.Thread(target=self._poll, args=args, name="performance-" + args[0], daemon=True)
            self.threads.append(thread)
            thread.start()
        return self

    def mark(self, name, detail=None):
        """Record controller-observed admission/completion without inventing backend edges."""
        mono, wall = time.monotonic_ns(), time.time_ns()
        self._put({"channel": "milestone", "name": name, "detail": detail,
                   "request_start_mono_ns": mono, "request_end_mono_ns": mono,
                   "request_start_wall_ns": wall, "request_end_wall_ns": wall})

    def __exit__(self, exc_type, exc, traceback):
        self.stop.set()
        for thread in self.threads:
            thread.join(timeout=25)
        alive = [thread.name for thread in self.threads if thread.is_alive()]
        self._put(None)
        self.writer.join(timeout=5)
        if self.error:
            raise RuntimeError("Performance evidence writer failed") from self.error
        if alive or self.writer.is_alive():
            raise RuntimeError("Performance sampling did not stop: " + ", ".join(alive))


def observation(row):
    start, end = row["request_start_mono_ns"] / 1e9, row["request_end_mono_ns"] / 1e9
    if end < start:
        raise ValueError("Request monotonic time decreased")
    return (start + end) / 2, (end - start) / 2


def app_point(row):
    """The phase and cumulative handoff count come from one locked progress response."""
    progress = row.get("response", {}).get("progress")
    if not progress or "error" in row:
        return None
    phase = progress.get("stage", {}).get("phase")
    if phase not in COPY_PHASES:
        return None
    timestamp, uncertainty = observation(row)
    return {"time": timestamp, "bytes": int(progress.get("copied_bytes", 0)),
            "files": int(progress.get("copied_file_count", 0)), "phase": phase,
            "uncertainty": uncertainty}


def interval(previous, current, period):
    """Invalid gaps carry no fabricated bytes or zero speeds."""
    start, end = previous["time"], current["time"]
    duration = end - start
    reason = ""
    if duration <= 0:
        reason = "non-increasing-time"
    elif current.get("phase") != previous.get("phase"):
        reason = "phase-change"
    elif current["bytes"] < previous["bytes"]:
        reason = "counter-reset"
    elif duration > 2 * period + 1e-6:
        reason = "sample-gap"
    elif max(previous.get("uncertainty", 0), current.get("uncertainty", 0)) > period:
        reason = "request-uncertainty"
    delta = current["bytes"] - previous["bytes"] if not reason else None
    return {"start": start, "end": end, "valid": not reason, "reason": reason,
            "bytes": delta, "rate": delta / duration if not reason else None,
            "no_progress_observed": not reason and delta == 0,
            "uncertainty_seconds": max(previous.get("uncertainty", 0), current.get("uncertainty", 0))}


def bin_estimate(intervals, start, width):
    """Allocate only valid adjacent observations by overlap; never extrapolate a gap."""
    end = start + width
    covered, value, longest_gap, cursor = 0.0, 0.0, 0.0, start
    uncertainty = 0.0
    for entry in intervals:
        if not entry["valid"]:
            continue
        left, right = max(start, entry["start"]), min(end, entry["end"])
        if right <= left:
            continue
        longest_gap = max(longest_gap, left - cursor)
        cursor = max(cursor, right)
        duration = right - left
        covered += duration
        value += entry["rate"] * duration
        uncertainty = max(uncertainty, entry["uncertainty_seconds"])
    longest_gap = max(longest_gap, end - cursor)
    return {"start_seconds": start, "end_seconds": end, "coverage": min(1.0, covered / width),
            "max_gap_seconds": longest_gap, "estimated_bytes": value,
            "bytes_per_second": value / covered if covered else None,
            "max_request_uncertainty_seconds": uncertainty}


def steady_window(intervals, start, period=APP_PERIOD):
    bins = [bin_estimate(intervals, start + offset, 30) for offset in range(0, 180, 30)]
    boundary = any(not entry["valid"] and entry["reason"] in ("counter-reset", "phase-change", "phase-boundary")
                   and entry["start"] < start + 180 and entry["end"] > start for entry in intervals)
    quality = not boundary and all(row["coverage"] >= 0.9 - 1e-9 and row["max_gap_seconds"] <= 2 * period + 1e-6
                  for row in bins)
    rates = [row["bytes_per_second"] for row in bins]
    mean = sum(rates) / 6 if all(rate is not None for rate in rates) else None
    cv = math.sqrt(sum((rate - mean)**2 for rate in rates) / 6) / mean if mean and mean > 0 else None
    drift = abs(sum(rates[:2]) / 2 - sum(rates[-2:]) / 2) / mean if mean and mean > 0 else None
    return {"start_seconds": start, "end_seconds": start + 180, "quality": quality,
            "stable": quality and cv is not None and cv <= 0.15 + 1e-9 and drift <= 0.10 + 1e-9,
            "mean_bytes_per_second": mean, "cv": cv, "relative_drift": drift,
            "minimum_coverage": min(row["coverage"] for row in bins),
            "max_gap_seconds": max(row["max_gap_seconds"] for row in bins),
            "max_request_uncertainty_seconds": max(row["max_request_uncertainty_seconds"] for row in bins)}


def copy_phase_edges(before, first, last, after):
    """Bracket phase changes by request ranges, not midpoint precision or payload claims."""
    start_lower = before["time"] - before["uncertainty"] if before else None
    start_upper = first["time"] + first["uncertainty"]
    end_lower = last["time"] - last["uncertainty"]
    end_upper = after["time"] + after["uncertainty"] if after else None
    return {"phase": first["phase"], "start_lower_seconds": start_lower,
            "start_upper_seconds": start_upper, "end_lower_seconds": end_lower,
            "end_upper_seconds": end_upper,
            "duration_lower_seconds": max(0.0, end_lower - start_upper),
            "duration_upper_seconds": end_upper - start_lower if end_upper is not None and start_lower is not None else None,
            "first_observed_bytes": first["bytes"], "last_observed_bytes": last["bytes"],
            "first_observed_file_work_entries": first["files"], "last_observed_file_work_entries": last["files"]}


INTERVAL_FIELDS = ["series", "start", "end", "valid", "reason", "bytes", "rate",
                   "no_progress_observed", "uncertainty_seconds"]
BIN_FIELDS = ["width_seconds", "start_seconds", "end_seconds", "coverage", "max_gap_seconds",
              "estimated_bytes", "bytes_per_second", "max_request_uncertainty_seconds"]
WINDOW_FIELDS = ["start_seconds", "end_seconds", "quality", "stable", "mean_bytes_per_second", "cv",
                 "relative_drift", "minimum_coverage", "max_gap_seconds", "max_request_uncertainty_seconds"]


def analyze_samples(raw_path, output_prefix, plot=True):
    """Stream evidence with a 180-second rolling horizon; all windows remain in CSV."""
    prefix = Path(output_prefix)
    paths = {name: Path(str(prefix) + suffix) for name, suffix in
             (("copy_phases", "-copy-phases.csv"), ("files", "-files.csv"), ("milestones", "-milestones.csv"), ("observations", "-observations.csv"), ("resources", "-resources.csv"), ("intervals", "-intervals.csv"), ("bins", "-bins.csv"), ("windows", "-windows.csv"),
              ("driver", "-driver.csv"), ("summary", "-summary.json"), ("curve", "-curve.svg"))}
    previous_app, previous_driver = None, {}
    driver_owner = None
    last_phase_observation, phase_before, phase_first, phase_last = None, None, None, None
    horizon = deque()
    origin, next_window = None, None
    next_bins = {10: None, 30: None}
    summary = {"scope": "ACP read/handoff; page 0C/34 are auxiliary drive proxies, not physical commitment",
               "window_estimator": "valid adjacent counter increments allocated by time overlap; rates use covered time",
               "first_stable_window": None, "window_count": 0, "stable_window_count": 0,
               "app_samples": 0, "driver_samples": 0, "invalid_intervals": 0,
               "no_progress_intervals": 0, "file_work_entry_counter_resets": 0, "byte_counter_resets": 0,
               "copy_phase_count": 0, "first_copy_phase_edges": None,
               "observed_copy_subset_bytes_per_second": None,
               "file_work_entry_rate_scope": "ACP file-work entry reports increment before transfer completion; not completed or published files", "driver_errors": 0, "driver_counter_resets": 0,
               "observation_start_seconds": None, "observation_end_seconds": None,
               "copy_observed_start_seconds": None, "copy_observed_end_seconds": None,
               "copy_observed_first_bytes": None, "copy_observed_last_bytes": None,
               "max_request_uncertainty_seconds": 0, "driver_proxy_refresh": "equal 34 values do not establish freshness",
               "thresholds": {"window_seconds": 180, "bin_seconds": 30, "step_seconds": 10,
                              "coverage": 0.9, "cv": 0.15, "drift": 0.10, "app_period_seconds": APP_PERIOD}}
    handles = {name: paths[name].open("w", newline="") for name in ("copy_phases", "files", "milestones", "observations", "resources", "intervals", "bins", "windows", "driver")}
    writers = {name: csv.DictWriter(handles[name], fieldnames=fields) for name, fields in
               (("copy_phases", ["phase", "start_lower_seconds", "start_upper_seconds", "end_lower_seconds", "end_upper_seconds",
                                   "duration_lower_seconds", "duration_upper_seconds", "first_observed_bytes", "last_observed_bytes",
                                   "first_observed_file_work_entries", "last_observed_file_work_entries"]),
                ("files", ["start", "end", "valid", "reason", "file_work_entries", "file_work_entries_per_second", "no_file_entry_observed", "uncertainty_seconds"]),
                ("milestones", ["time_seconds", "name", "detail"]),
                ("observations", ["time_seconds", "uncertainty_seconds", "phase", "bytes", "file_work_entries", "status"]),
                ("resources", ["time_seconds", "role", "pid", "start_ticks", "metric", "value"]),
                ("intervals", INTERVAL_FIELDS), ("bins", BIN_FIELDS), ("windows", WINDOW_FIELDS),
                ("driver", ["time_seconds", "request_uncertainty_seconds", "page", "parameter", "value", "status"]))}
    for writer in writers.values():
        writer.writeheader()
    try:
        with Path(raw_path).open() as source:
            for line in source:
                row = json.loads(line)
                timestamp, uncertainty = observation(row)
                if summary["observation_start_seconds"] is None:
                    summary["observation_start_seconds"] = timestamp
                summary["observation_start_seconds"] = min(summary["observation_start_seconds"], timestamp)
                summary["observation_end_seconds"] = max(summary["observation_end_seconds"] or timestamp, timestamp)
                summary["max_request_uncertainty_seconds"] = max(summary["max_request_uncertainty_seconds"], uncertainty)
                if row["channel"] == "milestone":
                    writers["milestones"].writerow({"time_seconds": timestamp, "name": row["name"],
                                                   "detail": json.dumps(row.get("detail"))})
                    continue
                if row["channel"] == "resources":
                    resources = row.get("response", {})
                    for role, values in resources.get("processes", {}).items():
                        for metric, value in values.items():
                            if metric not in ("pid", "start_ticks") and isinstance(value, (int, float)):
                                writers["resources"].writerow({"time_seconds": timestamp, "role": role,
                                    "pid": values["pid"], "start_ticks": values["start_ticks"], "metric": metric, "value": value})
                    for metric, value in resources.get("host", {}).items():
                        if isinstance(value, (int, float)):
                            writers["resources"].writerow({"time_seconds": timestamp, "role": "host", "metric": metric, "value": value})
                    continue
                if row["channel"] == "driver":
                    summary["driver_samples"] += 1
                    response = row.get("response", {})
                    owner = (response.get("pid"), response.get("mount"))
                    if owner != driver_owner:
                        previous_driver.clear()
                    driver_owner = owner
                    pages = response.get("pages", {})
                    if not pages:
                        summary["driver_errors"] += 1
                    for page, reading in pages.items():
                        values = reading.get("parameters", {})
                        if not values:
                            writers["driver"].writerow({"time_seconds": timestamp, "page": page,
                                                        "status": reading.get("error", "unknown")})
                            summary["driver_errors"] += 1
                            for key in list(previous_driver):
                                if key.startswith(page.upper() + ":"):
                                    del previous_driver[key]
                        for code, value in values.items():
                            writers["driver"].writerow({"time_seconds": timestamp, "request_uncertainty_seconds": uncertainty,
                                                        "page": page, "parameter": code, "value": value, "status": "observed"})
                            if page.upper() == "0C" and str(code) in ("0", "1", "2", "3"):
                                key = "0C:" + str(code)
                                current = {"time": timestamp, "bytes": int(value), "uncertainty": uncertainty}
                                if key in previous_driver:
                                    entry = interval(previous_driver[key], current, DRIVER_PERIOD)
                                    writers["intervals"].writerow({"series": key, **entry})
                                    summary["driver_counter_resets"] += entry["reason"] == "counter-reset"
                                previous_driver[key] = current
                    if "error" in row or not pages:
                        previous_driver.clear()
                    continue
                if row["channel"] != "app":
                    continue
                summary["app_samples"] += 1
                current = app_point(row)
                progress = row.get("response", {}).get("progress", {})
                writers["observations"].writerow({"time_seconds": timestamp, "uncertainty_seconds": uncertainty,
                    "phase": progress.get("stage", {}).get("phase", "JOB_PHASE_UNSPECIFIED"),
                    "bytes": progress.get("copied_bytes", 0), "file_work_entries": progress.get("copied_file_count", 0),
                    "status": row.get("error", "observed") if progress else row.get("error", "unknown")})
                phase_reading = None
                if progress and "error" not in row:
                    phase_reading = {"time": timestamp, "uncertainty": uncertainty,
                                     "phase": progress.get("stage", {}).get("phase", "JOB_PHASE_UNSPECIFIED")}
                if phase_first and phase_reading and phase_reading["phase"] != phase_first["phase"]:
                    edges = copy_phase_edges(phase_before, phase_first, phase_last, phase_reading)
                    writers["copy_phases"].writerow(edges)
                    summary["copy_phase_count"] += 1
                    if summary["first_copy_phase_edges"] is None:
                        summary["first_copy_phase_edges"] = edges
                    phase_first = None
                if current is not None:
                    if phase_first is None:
                        phase_before, phase_first = last_phase_observation, current
                    phase_last = current
                if phase_reading is not None:
                    last_phase_observation = phase_reading
                if current is None:
                    if previous_app is not None:
                        boundary = {"start": previous_app["time"], "end": timestamp, "valid": False,
                                    "reason": "phase-boundary" if progress else "request-error",
                                    "bytes": None, "rate": None, "no_progress_observed": False,
                                    "uncertainty_seconds": uncertainty}
                        horizon.append(boundary)
                        writers["intervals"].writerow({"series": "acp", **boundary})
                        summary["invalid_intervals"] += 1
                    previous_app = None
                    continue
                if summary["copy_observed_start_seconds"] is None:
                    summary["copy_observed_start_seconds"] = current["time"]
                    summary["copy_observed_first_bytes"] = current["bytes"]
                summary["copy_observed_end_seconds"] = current["time"]
                summary["copy_observed_last_bytes"] = current["bytes"]
                if previous_app is not None:
                    entry = interval(previous_app, current, APP_PERIOD)
                    writers["intervals"].writerow({"series": "acp", **entry})
                    file_entry = interval({**previous_app, "bytes": previous_app["files"]},
                                          {**current, "bytes": current["files"]}, APP_PERIOD)
                    writers["files"].writerow({"start": file_entry["start"], "end": file_entry["end"],
                        "valid": file_entry["valid"], "reason": file_entry["reason"], "file_work_entries": file_entry["bytes"],
                        "file_work_entries_per_second": file_entry["rate"], "no_file_entry_observed": file_entry["no_progress_observed"],
                        "uncertainty_seconds": file_entry["uncertainty_seconds"]})
                    summary["file_work_entry_counter_resets"] += file_entry["reason"] == "counter-reset"
                    summary["byte_counter_resets"] += entry["reason"] == "counter-reset"
                    summary["invalid_intervals"] += not entry["valid"]
                    summary["no_progress_intervals"] += entry["no_progress_observed"]
                    horizon.append(entry)
                    if origin is None:
                        while horizon and horizon[0]["end"] < current["time"] - 180:
                            horizon.popleft()
                    if origin is None and entry["valid"] and entry["bytes"] > 0:
                        # Plan anchors at the observed positive progress, not the prior endpoint.
                        origin = current["time"]
                        next_window = origin
                        next_bins = {10: origin, 30: origin}
                previous_app = current
                if origin is None:
                    continue
                for width in (10, 30):
                    while next_bins[width] + width <= current["time"] + 1e-9:
                        writers["bins"].writerow({"width_seconds": width, **bin_estimate(horizon, next_bins[width], width)})
                        next_bins[width] += width
                while next_window + 180 <= current["time"] + 1e-9:
                    window = steady_window(horizon, next_window)
                    writers["windows"].writerow(window)
                    summary["window_count"] += 1
                    summary["stable_window_count"] += window["stable"]
                    if window["stable"] and summary["first_stable_window"] is None:
                        summary["first_stable_window"] = window
                    next_window += 10
                cutoff = min(next_window, *next_bins.values())
                while horizon and horizon[0]["end"] <= cutoff:
                    horizon.popleft()
    finally:
        for handle in handles.values():
            handle.close()
    if phase_first:
        edges = copy_phase_edges(phase_before, phase_first, phase_last, None)
        # The stream ended without observing a post-COPY phase: its upper edge is unknown.
        with paths["copy_phases"].open("a", newline="") as output:
            csv.DictWriter(output, fieldnames=writers["copy_phases"].fieldnames).writerow(edges)
        summary["copy_phase_count"] += 1
        if summary["first_copy_phase_edges"] is None:
            summary["first_copy_phase_edges"] = edges
    first_time, last_time = summary["copy_observed_start_seconds"], summary["copy_observed_end_seconds"]
    if summary["copy_phase_count"] == 1 and not summary["byte_counter_resets"] and first_time is not None and last_time > first_time and summary["copy_observed_last_bytes"] >= summary["copy_observed_first_bytes"]:
        summary["observed_copy_subset_bytes_per_second"] = (summary["copy_observed_last_bytes"] - summary["copy_observed_first_bytes"]) / (last_time - first_time)
    summary["origin_seconds"] = origin
    summary["artifacts"] = {name: str(path) for name, path in paths.items() if name != "curve" or plot}
    if plot:
        plot_curves(paths, summary)
    paths["summary"].write_text(json.dumps(summary, indent=2) + "\n")
    return summary


def _proc_text(path, limit=1024 * 1024):
    with path.open("rb") as source:
        value = source.read(limit + 1)
    if len(value) > limit:
        raise ValueError("Resource file exceeds read limit: " + str(path))
    return os.fsdecode(value)


def _process_stat(process):
    # proc stat comm may include spaces/parentheses; split after its final ')'.
    fields = _proc_text(process / "stat").rsplit(")", 1)[1].split()
    return {"pid": int(process.name), "cpu_ticks": int(fields[11]) + int(fields[12]),
            "start_ticks": int(fields[19]), "minflt": int(fields[7]), "majflt": int(fields[9])}


def resource_sample(install=None, tape_dir=None, *, pid=None, executable=None, start_ticks=None):
    """Read execution-host counters for owned services or one explicitly identified Executor."""
    explicit = any(value is not None for value in (pid, executable, start_ticks))
    if explicit:
        if install is not None or tape_dir is not None:
            raise ValueError("Explicit process identity cannot be combined with install/Tape directory")
        if (type(pid) is not int or pid <= 0 or type(start_ticks) is not int or start_ticks < 0
                or not executable or not os.path.isabs(executable)):
            raise ValueError("Explicit process identity requires positive pid, absolute executable and nonnegative start_ticks")
    elif install is None or tape_dir is None:
        raise ValueError("Resource sampling requires install and Tape directory")
    result = {"host": {}, "processes": {}, "errors": []}
    try:
        cpu = list(map(int, _proc_text(Path("/proc/stat")).splitlines()[0].split()[1:9]))
        result["host"].update(cpu_total_ticks=sum(cpu), cpu_idle_ticks=cpu[3], cpu_iowait_ticks=cpu[4])
        result["host"]["clock_ticks_per_second"] = os.sysconf("SC_CLK_TCK")
        result["diskstats"] = _proc_text(Path("/proc/diskstats"))
        for line in _proc_text(Path("/proc/meminfo")).splitlines():
            key, value = line.split(":", 1)
            if key in ("MemAvailable", "MemFree", "Cached", "Dirty", "Writeback"):
                result["host"][key + "_kib"] = int(value.split()[0])
    except (OSError, ValueError, IndexError) as error:
        result["errors"].append(str(error))
    if explicit:
        try:
            directory = os.path.dirname(executable)
            device = os.stat(directory).st_dev
            filesystem = os.statvfs(directory)
            result["filesystem"] = {"path": directory, "device_major": os.major(device),
                                    "device_minor": os.minor(device),
                                    "total_bytes": filesystem.f_blocks * filesystem.f_frsize,
                                    "free_bytes": filesystem.f_bfree * filesystem.f_frsize,
                                    "available_bytes": filesystem.f_bavail * filesystem.f_frsize}
        except (OSError, ValueError, AttributeError) as error:
            result["errors"].append("Executor filesystem: " + str(error))
    ambiguous = set()
    processes = [Path("/proc") / str(pid)] if explicit else Path("/proc").glob("[0-9]*")
    for index, process in enumerate(processes):
        if index >= 65536:
            result["errors"].append("Resource process count exceeds limit")
            break
        role = "executor" if explicit else None
        try:
            if explicit:
                if os.readlink(process / "exe") != executable:
                    raise ValueError("Executor executable identity mismatch")
            else:
                argv = [arg for arg in _proc_text(process / "cmdline").split("\0") if arg]
                if not argv:
                    continue
                if argv[0] == install + "/yatm-httpd":
                    role = "yatm"
                elif Path(argv[0]).name == "ltfs" and ("work_directory=" + tape_dir) in argv:
                    role = "ltfs"
                if role is None:
                    continue
            values = _process_stat(process)
            if explicit and values["start_ticks"] != start_ticks:
                raise ValueError("Executor start_ticks identity mismatch")
            for line in _proc_text(process / "status").splitlines():
                if line.startswith(("VmRSS:", "VmHWM:")):
                    values[line.split(":", 1)[0] + "_kib"] = int(line.split()[1])
            for line in _proc_text(process / "io").splitlines():
                key, value = line.split(":", 1)
                values[key] = int(value)
            descriptors = 0
            for _ in (process / "fd").iterdir():
                descriptors += 1
                if descriptors > 65536:
                    raise ValueError("Resource descriptor count exceeds limit")
            values["open_descriptors"] = descriptors
            if explicit and (_process_stat(process)["start_ticks"] != start_ticks
                             or os.readlink(process / "exe") != executable):
                raise ValueError("Executor identity changed during resource collection")
            if role in ambiguous or role in result["processes"]:
                result["errors"].append("Multiple matching " + role + " processes; ownership ambiguous")
                result["processes"].pop(role, None)
                ambiguous.add(role)
                continue
            result["processes"][role] = values
        except (OSError, ValueError, IndexError) as error:
            # Processes may exit during collection; unrelated inaccessible processes are normal.
            if role:
                result["errors"].append(str(error))
    return result


def plot_curves(paths, summary):
    """Bounded 24-hour plot at 2s app/5s driver cadence, with ten 0C and five 34 parameters."""
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    origin = summary.get("origin_seconds") or summary.get("observation_start_seconds") or 0
    figure, axes = plt.subplots(7, 1, figsize=(12, 19), sharex=True)
    lines = {}
    with paths["intervals"].open() as source:
        for index, row in enumerate(csv.DictReader(source)):
            if index >= 120000:
                raise ValueError("Curve exceeds bounded 120000 raw intervals")
            key = row["series"]
            x, y = lines.setdefault(key, ([], []))
            x.extend([float(row["start"]) - origin, float(row["end"]) - origin, math.nan])
            rate = float(row["rate"]) / 1e6 if row["rate"] else math.nan
            y.extend([rate, rate, math.nan])
    for key, (x, y) in lines.items():
        axis = axes[0] if key == "acp" else axes[1]
        names = {"acp": "Raw ACP handoff intervals", "0C:0": "Host write accepted",
                 "0C:1": "Compressed media write bytes (logical updates)",
                 "0C:2": "Compressed media read bytes", "0C:3": "Host read returned"}
        axis.plot(x, y, linewidth=0.7, alpha=0.7, label=names.get(key, key))
    bins = {10: ([], []), 30: ([], [])}
    with paths["bins"].open() as source:
        for index, row in enumerate(csv.DictReader(source)):
            if index >= 20000:
                raise ValueError("Curve exceeds bounded 20000 fixed bins")
            x, y = bins[int(row["width_seconds"])]
            x.append(float(row["end_seconds"]) - origin)
            y.append(float(row["bytes_per_second"]) / 1e6 if row["bytes_per_second"] and float(row["coverage"]) >= 0.9 else math.nan)
    for width, (x, y) in bins.items():
        axes[0].plot(x, y, label=str(width) + "s estimate over valid covered time")
    file_x, file_y = [], []
    with paths["files"].open() as source:
        for index, row in enumerate(csv.DictReader(source)):
            if index >= 45000:
                raise ValueError("Curve exceeds bounded file-work-entry intervals")
            file_x.extend([float(row["start"]) - origin, float(row["end"]) - origin, math.nan])
            rate = float(row["file_work_entries_per_second"]) if row["file_work_entries_per_second"] else math.nan
            file_y.extend([rate, rate, math.nan])
    axes[6].plot(file_x, file_y, linewidth=0.8, label="ACP file-work entries; not completed or published files")
    reposition = ([], [])
    with paths["driver"].open() as source:
        for index, row in enumerate(csv.DictReader(source)):
            if index >= 300000:
                raise ValueError("Curve exceeds bounded driver rows")
            if row["page"].upper() == "34" and row["parameter"] == "0" and row["status"] == "observed":
                reposition[0].append(float(row["time_seconds"]) - origin)
                reposition[1].append(float(row["value"]))
    axes[2].scatter(*reposition, s=8, label="Page34/0; freshness unknown, not cumulative")
    resources, clock_ticks = {}, None
    with paths["resources"].open() as source:
        for index, row in enumerate(csv.DictReader(source)):
            if index >= 500000:
                raise ValueError("Curve exceeds bounded resource rows")
            if row["metric"] == "clock_ticks_per_second":
                clock_ticks = float(row["value"])
            if row["metric"] in ("VmRSS_kib", "cpu_ticks", "read_bytes"):
                key = (row["role"], row["metric"])
                values = resources.setdefault(key, [])
                values.append((float(row["time_seconds"]) - origin, float(row["value"]),
                               (row["pid"], row["start_ticks"])))
    for (role, metric), values in resources.items():
        if metric == "VmRSS_kib":
            axes[3].plot([value[0] for value in values], [value[1] / 1024 for value in values], label=role + " RSS MiB")
            continue
        x, y = [], []
        for previous, current in zip(values, values[1:]):
            elapsed = current[0] - previous[0]
            valid = previous[2] == current[2] and 0 < elapsed <= 2 * DRIVER_PERIOD and current[1] >= previous[1]
            rate = (current[1] - previous[1]) / elapsed if valid else math.nan
            x.append(current[0])
            if metric == "cpu_ticks":
                y.append(rate * 100 / clock_ticks if clock_ticks else math.nan)
            else:
                y.append(rate / 1e6)
        if metric == "cpu_ticks":
            axes[4].plot(x, y, label=role + " CPU, percent of one core")
        else:
            axes[5].plot(x, y, label=role + " process accounted disk reads (not source-only)")
    phase_observations = []
    with paths["observations"].open() as source:
        for row in csv.DictReader(source):
            if row["status"] != "observed":
                continue
            phase = row["phase"]
            if not phase_observations or phase_observations[-1][1] != phase:
                if len(phase_observations) >= 1000:
                    raise ValueError("Curve exceeds bounded phase transitions")
                phase_observations.append((float(row["time_seconds"]) - origin, phase))
    for timestamp, phase in phase_observations:
        axes[0].axvline(timestamp, color="grey", alpha=0.3)
        axes[0].annotate(phase.removeprefix("JOB_PHASE_"), (timestamp, 1), xycoords=("data", "axes fraction"),
                         rotation=90, va="top", fontsize=6)
    with paths["milestones"].open() as source:
        for row in csv.DictReader(source):
            timestamp = float(row["time_seconds"]) - origin
            axes[0].axvline(timestamp, color="black", alpha=0.4)
            axes[0].annotate(row["name"], (timestamp, 0), xycoords=("data", "axes fraction"),
                             rotation=90, va="bottom", fontsize=7)
    first = summary["first_stable_window"]
    if first:
        axes[0].axvspan(first["start_seconds"] - origin, first["end_seconds"] - origin, alpha=0.15, label="First qualifying 180s")
    # Keep preparation/finalization waits visible, even without an ACP byte interval.
    left = (summary["observation_start_seconds"] if summary["observation_start_seconds"] is not None else origin) - origin
    right = (summary["observation_end_seconds"] if summary["observation_end_seconds"] is not None else origin) - origin
    for axis in axes:
        if right > left:
            axis.set_xlim(left, right)
        axis.grid(alpha=0.2)
        if axis.get_legend_handles_labels()[0]:
            axis.legend(loc="best", fontsize=8)
    axes[0].set(title="ACP supply and owned drive proxies; no physical commitment claim", ylabel="Logical MB/s")
    axes[1].set(ylabel="Proxy MB/s")
    axes[2].set(ylabel="Repositions/100MB")
    axes[3].set(ylabel="RSS MiB")
    axes[4].set(ylabel="CPU % of one core")
    axes[5].set(ylabel="Process read MB/s")
    axes[6].set(ylabel="File-work entries/s", xlabel="Seconds from first observed positive ACP increment (waits retained)")
    figure.tight_layout()
    figure.savefig(paths["curve"], format="svg")
    plt.close(figure)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    resources = commands.add_parser("resources")
    resources.add_argument("install", nargs="?")
    resources.add_argument("tape_dir", nargs="?")
    resources.add_argument("--pid", type=int)
    resources.add_argument("--executable")
    resources.add_argument("--start-ticks", type=int)
    args = parser.parse_args(argv)
    try:
        result = resource_sample(args.install, args.tape_dir, pid=args.pid,
                                 executable=args.executable, start_ticks=args.start_ticks)
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(result))


if __name__ == "__main__":
    main()
