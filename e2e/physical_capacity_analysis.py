"""Analyze saved capacity samples; never contact a service, mount or device.

Run: python3 e2e/physical_capacity_analysis.py archive-samples.jsonl --output PREFIX
Writes PREFIX-{observations,intervals,pages,events}.csv, PREFIX-summary.json and,
when matplotlib is installed, PREFIX-curves.svg. Stdout is the JSON summary.

HP field definitions: LTO-5 Host Interface Guide (April 2011), pp. 59, 79, 81;
LTO-6 Host Interface Guide (October 2012), pp. 64-65, 87:
https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol3_E5b/LTO5_Vol3_E5b.pdf
https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol3_E1_D20/LTO6_Vol3_E1_D20.pdf
Page 17 is preserved as parameter bytes without capacity interpretation.
"""

import argparse
import csv
from contextlib import ExitStack
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import re

from physical_performance_fixture import decode_log_page


METRICS = {
    "p31_p1_remaining": ("bytes", "31 partition 1 remaining (MiB converted to bytes)", False),
    "ew_minus_eod": ("bytes", "0C:5-4 current-partition EOD to EW (decimal MB converted)", False),
    "filesystem_available": ("bytes", "LTFS statvfs available bytes", False),
    "eod": ("bytes", "0C:4 recorded EOD from partition beginning", False),
    "head_position": ("bytes", "0C:7 current head position from partition beginning", False),
    "app_copied": ("bytes", "Job byte progress (includes rejected work)", True),
    "drive_received": ("bytes", "0C:0 application bytes received by drive", True),
    "drive_written": ("bytes", "0C:1 written-byte proxy excluding ECC/formatting", True),
    "write_0": ("count", "02:0 corrections without substantial delay", True),
    "write_1": ("count", "02:1 corrections using retries", True),
    "write_2": ("count", "02:2 corrected plus uncorrected data sets", True),
    "write_3": ("count", "02:3 data sets rewritten", True),
    "write_4": ("count", "02:4 CCQ sets rewritten", True),
    "write_5": ("count", "02:5 data sets processed", True),
    "write_6": ("count", "02:6 uncorrected data sets", True),
    "repositions": ("repositions/100MB", "34:0 repositions/100 MB (not cumulative)", False),
    "tapealert_active": ("flags", "LTFS retained TapeAlert flags (legacy raw reads noted in evidence)", False),
}
CAPACITY = ("filesystem_available", "p31_p1_remaining", "ew_minus_eod")
PAGES = ("31", "0C", "02", "34", "17", "2E")
OBS_FIELDS = ("line", "channel", "metric", "unit", "value", "status", "owner", "phase",
              "start_ns", "end_ns", "mono_start_ns", "mono_end_ns", "period_seconds",
              "host_start_ns", "host_end_ns", "host_timestamp_ns", "continuity")
INT_FIELDS = ("metric", "line", "start_ns", "end_ns", "elapsed_seconds", "uncertainty_seconds",
              "valid", "reason", "delta", "rate_per_second", "stall")
EVENT_FIELDS = ("kind", "metric", "line", "start_ns", "end_ns", "detail")


def integer(value):
    if isinstance(value, bool) or not isinstance(value, (str, int)):
        return None
    try:
        return int(value)
    except ValueError:
        return None


def parameters(raw, page):
    """Decode only LOG parameter framing; retain control and uninterpreted bytes."""
    data = bytes.fromhex(raw)
    if len(data) < 4 or data[0] & 0x3F != page or data[1] != 0 or int.from_bytes(data[2:4], "big") != len(data) - 4:
        raise ValueError("Wrong or truncated LOG page")
    result, offset = {}, 4
    while offset < len(data):
        if offset + 4 > len(data):
            raise ValueError("Truncated parameter header")
        code, control, width = int.from_bytes(data[offset:offset + 2], "big"), data[offset + 2], data[offset + 3]
        offset += 4
        if code in result or offset + width > len(data):
            raise ValueError("Duplicate or truncated parameter")
        result[code] = {"control": control, "raw": data[offset:offset + width].hex()}
        offset += width
    return result


def raw_page(reading, page):
    if reading.get("error") or reading.get("timed_out"):
        return {}, str(reading.get("error", "timeout"))
    if not reading.get("raw"):
        return {}, "missing-raw"
    try:
        if page in ("17", "2E"):
            return parameters(reading["raw"], int(page, 16)), ""
        return decode_log_page(bytes.fromhex(reading["raw"]), int(page, 16)), ""
    except (ValueError, TypeError) as error:
        return {}, str(error)


def observations(row, line):
    """Use controller wall bounds for alignment; keep host times separate and exact."""
    channel = row.get("channel")
    response = row.get("response") or {}
    start, end = integer(row.get("request_start_wall_ns")), integer(row.get("request_end_wall_ns"))
    mono_start, mono_end = integer(row.get("request_start_mono_ns")), integer(row.get("request_end_mono_ns"))
    period = row.get("period_seconds")
    if not isinstance(period, (int, float)) or isinstance(period, bool) or not math.isfinite(period) or period <= 0:
        period = None
    base = dict(line=line, channel=channel, start_ns=start, end_ns=end, mono_start_ns=mono_start,
                mono_end_ns=mono_end, period_seconds=period, phase="", owner="", continuity="")
    error = str(row.get("error") or ("timeout" if row.get("timed_out") else ""))
    if row.get("exit_code", 0) != 0:
        error = error or "request-failed"
    if start is None or end is None or end < start:
        error = error or "invalid-time"

    def point(metric, value, reading=None, status=""):
        reading = reading or {}
        reason = error or status
        if value is None:
            reason = reason or "missing"
        return {**base, "metric": metric, "unit": METRICS[metric][0],
                "value": value if not reason else None, "status": reason or "observed",
                "host_start_ns": integer(reading.get("request_start_ns")),
                "host_end_ns": integer(reading.get("request_end_ns")),
                "host_timestamp_ns": integer(reading.get("timestamp_ns"))}

    if channel == "app":
        progress = response.get("progress") or {}
        stage = progress.get("stage") or {}
        base.update(owner=str(row.get("job_id", "")), phase=stage.get("phase", ""))
        value = integer(progress.get("copied_bytes"))
        reason = "negative-counter" if value is not None and value < 0 else ""
        return [point("app_copied", value, status=reason)], []
    if channel != "driver":
        return [], []
    if response.get("status") != "mounted":
        error = error or "not-mounted"
    if response.get("pid") is not None and response.get("mount") and row.get("job_id"):
        base["owner"] = json.dumps([str(row["job_id"]), response["pid"], response["mount"], response.get("argv")], separators=(",", ":"))
    pages, page_errors, evidence = {}, {}, []
    for page in PAGES:
        reading = response.get("pages", {}).get(page, {})
        pages[page], page_errors[page] = raw_page(reading, page)
        evidence.append({"line": line, "page": page, "start_ns": start, "end_ns": end,
                         "status": error or page_errors[page] or "observed",
                         "reading": json.dumps(reading, separators=(",", ":")),
                         "parameters": json.dumps(pages[page], separators=(",", ":"))})

    def field(page, code, scale=1, bits=32):
        value = pages[page].get(str(code))
        # Do not interpret all-ones capacity/counter values as usable magnitudes.
        # Their vendor-specific unknown/saturation meaning remains in raw evidence.
        if value == (1 << bits) - 1:
            return None, "all-ones-uninterpreted"
        return (value * scale if value is not None else None), page_errors[page]

    points = []
    for metric, page, code, scale, bits in (
            ("p31_p1_remaining", "31", 2, 1024**2, 32),
            ("eod", "0C", 4, 10**6, 32), ("head_position", "0C", 7, 10**6, 32),
            ("drive_received", "0C", 0, 1, 64), ("drive_written", "0C", 1, 1, 64),
            ("repositions", "34", 0, 1, 16),
            *(("write_" + str(code), "02", code, 1, 32) for code in range(7))):
        value, reason = field(page, code, scale, bits)
        points.append(point(metric, value, response.get("pages", {}).get(page), reason))
    ew, ew_error = field("0C", 5, 10**6)
    eod, eod_error = field("0C", 4, 10**6)
    # Preserve negative differences; never clamp them to a plausible free capacity.
    points.append(point("ew_minus_eod", ew - eod if ew is not None and eod is not None else None,
                        response.get("pages", {}).get("0C"), ew_error or eod_error))
    space = response.get("statvfs") or {}
    value = integer(space.get("available_bytes"))
    reason = str(space.get("error") or "")
    if not reason and page_errors["31"] and page_errors["0C"]:
        # An LTFS process appears before FUSE mounts. The same path can still
        # describe the host filesystem; retain it raw without labeling it Tape.
        reason = "mount-not-established-by-capacity-reads"
    points.append(point("filesystem_available", value, space, reason))
    alert = response.get("storage_alert")
    if alert is not None:
        try:
            bits = int(alert["value"], 16)
            if not 0 <= bits < 1 << 64:
                raise ValueError("TapeAlert mask exceeds 64 bits")
            count, reason = bin(bits).count("1"), alert.get("error", "")
        except (KeyError, ValueError, TypeError) as alert_error:
            count, reason = None, str(alert.get("error") or alert_error)
        points.append(point("tapealert_active", count, alert, reason))
        evidence.append({"line": line, "page": "storageAlert", "start_ns": start, "end_ns": end,
                         "status": error or reason or "observed", "reading": json.dumps(alert),
                         "parameters": "LTFS retained bitmask"})
    else:
        flags = pages["2E"]
        complete = set(flags) == set(range(1, 65)) and all(
            value["control"] == 0x40 and value["raw"] in ("00", "01") for value in flags.values())
        points.append(point("tapealert_active", sum(value["raw"] == "01" for value in flags.values()) if complete else None,
                            response.get("pages", {}).get("2E"), page_errors["2E"] or ("" if complete else "unknown-flag-layout")))
    # A changed EW coordinate can indicate a partition change: do not join these samples.
    for item in points:
        item["partition_ew"] = ew
    return points, evidence


def interval(previous, current, forced="", gap_factor=2):
    result = dict(metric=current["metric"], line=current["line"], start_ns=None, end_ns=current["end_ns"],
                  elapsed_seconds=None, uncertainty_seconds=None, valid=False, reason=forced,
                  delta=None, rate_per_second=None, stall=False)
    if previous is None:
        result["reason"] = forced or "first-observation"
        return result
    result["start_ns"] = previous["start_ns"]
    reason = forced
    if previous["status"] != "observed" or current["status"] != "observed":
        reason = reason or "missing-observation"
    if not previous["owner"] or not current["owner"]:
        reason = reason or "unknown-owner"
    if previous["owner"] != current["owner"]:
        reason = reason or "owner-change"
    if current["channel"] == "app" and (previous["phase"] != current["phase"] or current["phase"] != "JOB_PHASE_COPYING_TO_MEDIA"):
        reason = reason or "phase-boundary"
    if any(p[key] is None for p in (previous, current) for key in ("start_ns", "end_ns")):
        result["reason"] = reason or "invalid-time"
        return result
    wall_delta = (current["start_ns"] - previous["start_ns"] + current["end_ns"] - previous["end_ns"]) / 2e9
    duration = wall_delta
    if all(p[key] is not None for p in (previous, current) for key in ("mono_start_ns", "mono_end_ns")):
        duration = (current["mono_start_ns"] - previous["mono_start_ns"] + current["mono_end_ns"] - previous["mono_end_ns"]) / 2e9
        if abs(duration - wall_delta) > 0.001:
            reason = reason or "clock-discontinuity"
    uncertainty = sum(p["end_ns"] - p["start_ns"] for p in (previous, current)) / 2e9
    result.update(elapsed_seconds=duration, uncertainty_seconds=uncertainty)
    periods = [p["period_seconds"] for p in (previous, current)]
    if duration <= 0 or current["start_ns"] < previous["end_ns"]:
        reason = reason or "overlapping-or-reversed-time"
    elif None in periods:
        reason = reason or "unknown-period"
    elif duration > gap_factor * max(periods):
        reason = reason or "sample-gap"
    elif uncertainty >= duration:
        reason = reason or "request-uncertainty"
    if not reason:
        delta = current["value"] - previous["value"]
        if METRICS[current["metric"]][2] and delta < 0:
            reason = "counter-reset"
        else:
            result.update(valid=True, delta=delta, rate_per_second=delta / duration,
                          stall=current["metric"] == "app_copied" and delta == 0)
    result["reason"] = reason
    return result


def endpoint(point):
    return {key: point[key] for key in ("value", "line", "start_ns", "end_ns")}


def job_errors(path):
    """Keep second-resolution log times as intervals, not exact transfer edges."""
    if path is None:
        return
    epoch = datetime(1970, 1, 1, tzinfo=timezone.utc)
    with Path(path).open() as handle:
        for number, text in enumerate(handle, 1):
            match = re.match(r'^time="([^"]+)" level=error\b', text)
            if not match:
                continue
            elapsed = datetime.strptime(match[1], "%Y-%m-%dT%H:%M:%S%z") - epoch
            start = (elapsed.days * 86400 + elapsed.seconds) * 10**9
            yield dict(kind="job-error", metric="job-log", line=number, start_ns=start,
                       end_ns=start + 10**9, detail=text.rstrip())


def analyze_samples(source, prefix, plot=True, gap_factor=2, job_log=None):
    if not math.isfinite(gap_factor) or gap_factor < 1:
        raise ValueError("gap-factor must be finite and at least one")
    source, prefix = Path(source), Path(prefix)
    paths = {name: Path(str(prefix) + "-" + name + suffix) for name, suffix in
             (("observations", ".csv"), ("intervals", ".csv"), ("pages", ".csv"), ("events", ".csv"),
              ("summary", ".json"), ("curves", ".svg"), ("boundary", ".svg"))}
    inputs = [source] + ([Path(job_log)] if job_log is not None else [])
    outputs = [*paths.values(), paths["curves"].with_suffix(".png"), paths["boundary"].with_suffix(".png")]
    if any(item.resolve() in [path.resolve() for path in outputs] for item in inputs):
        raise ValueError("Output must not overwrite input evidence")
    prefix.parent.mkdir(parents=True, exist_ok=True)
    summary = {"source": str(source), "scope": "HP observations; no absolute accuracy, writable-capacity or causation claim",
               "timing": "Controller wall request bounds align channels; host timestamps retained separately; no interpolation",
               "correlation": "Overlapping observation brackets only, not causation; counts are counter intervals, not unique errors",
               "gap_factor": gap_factor, "page17": "Raw parameter bytes only; derived capacity deferred",
               "limits": ["31 partition 1 and 0C current partition are not assumed identical",
                          "Job progress includes rejected work; use published items and Restore for completed bytes",
                          "Unobserved reload/reset cannot be detected; owner changes and counter decreases break deltas",
                          "2E flags may clear on read; zero does not prove no earlier alert",
                          "All-ones values are retained raw and excluded from derived quantities"],
               "metrics": {}, "stalls": {"count": 0, "observed_seconds": 0, "first": None},
               "first_collapse": {}, "first_nonpositive": {}, "temporal_error_overlap": {}}
    previous, origin, count = {}, None, 0
    with ExitStack() as stack:
        writers = {}
        for name, fields in (("observations", OBS_FIELDS), ("intervals", INT_FIELDS), ("events", EVENT_FIELDS),
                             ("pages", ("line", "page", "start_ns", "end_ns", "status", "reading", "parameters"))):
            handle = stack.enter_context(paths[name].open("w", newline=""))
            writers[name] = csv.DictWriter(handle, fieldnames=fields, extrasaction="ignore")
            writers[name].writeheader()
        input_file = stack.enter_context(source.open())
        for line, text in enumerate(input_file, 1):
            if not text.strip():
                continue
            count += 1
            try:
                row = json.loads(text)
            except ValueError as error:
                raise ValueError(f"Invalid JSON on line {line}") from error
            if row.get("channel") == "milestone":
                detail = row.get("detail") or {}
                failed = isinstance(detail, dict) and (detail.get("error") or detail.get("exit_code", 0) != 0)
                writers["events"].writerow(dict(kind="operation-error" if failed else "milestone",
                    metric=row.get("name", ""), line=line, start_ns=integer(row.get("request_start_wall_ns")),
                    end_ns=integer(row.get("request_end_wall_ns")), detail=json.dumps(detail)))
            points, evidence = observations(row, line)
            writers["pages"].writerows(evidence)
            forced = ""
            if row.get("channel") == "driver":
                for point in points:
                    old = previous.get(point["metric"])
                    if not old or old["status"] != "observed" or point["status"] != "observed":
                        continue
                    if METRICS[point["metric"]][2] and point["value"] < old["value"]:
                        forced = "counter-reset"
                    if point.get("partition_ew") != old.get("partition_ew"):
                        forced = forced or "partition-coordinate-change"
            for point in points:
                metric = point["metric"]
                old = previous.get(metric)
                span = interval(old, point, forced, gap_factor)
                point["continuity"] = span["reason"]
                writers["observations"].writerow(point)
                writers["intervals"].writerow(span)
                previous[metric] = point
                if point["start_ns"] is not None:
                    origin = point["start_ns"] if origin is None else min(origin, point["start_ns"])
                stats = summary["metrics"].setdefault(metric, {"label": METRICS[metric][1], "unit": METRICS[metric][0],
                    "first": None, "last": None, "min": None, "observed": 0, "unknown": 0, "breaks": {}, "largest_drop": None})
                if span["reason"]:
                    stats["breaks"][span["reason"]] = stats["breaks"].get(span["reason"], 0) + 1
                if point["status"] == "observed":
                    value = endpoint(point)
                    stats["observed"] += 1
                    stats["first"] = stats["first"] or value
                    stats["last"] = value
                    if stats["min"] is None or point["value"] < stats["min"]["value"]:
                        stats["min"] = value
                    if metric in CAPACITY and point["value"] <= 0:
                        summary["first_nonpositive"].setdefault(metric, value)
                        if span["valid"] and old["value"] > 0:
                            summary["first_collapse"].setdefault(metric, {**value, "start_ns": span["start_ns"], "previous_value": old["value"]})
                    if span["valid"] and span["delta"] < 0:
                        drop = {"delta": span["delta"], "start_ns": span["start_ns"], "end_ns": span["end_ns"]}
                        if stats["largest_drop"] is None or drop["delta"] < stats["largest_drop"]["delta"]:
                            stats["largest_drop"] = drop
                else:
                    stats["unknown"] += 1
                event = None
                if span["stall"]:
                    summary["stalls"]["count"] += 1
                    summary["stalls"]["observed_seconds"] += span["elapsed_seconds"]
                    summary["stalls"]["first"] = summary["stalls"]["first"] or span
                    event = ("stall", span["start_ns"], "No observed ACP increment; not proof of a physical stop")
                elif metric.startswith("write_") and metric != "write_5" and span["valid"] and span["delta"] > 0:
                    event = ("error-increment", span["start_ns"], str(span["delta"]))
                elif metric == "tapealert_active" and point["value"] is not None and point["value"] > 0:
                    event = ("tapealert", point["start_ns"], str(point["value"]))
                elif point["status"] != "observed":
                    event = ("observation-error", point["start_ns"], point["status"])
                if event:
                    writers["events"].writerow(dict(kind=event[0], metric=metric, line=line, start_ns=event[1], end_ns=point["end_ns"], detail=event[2]))
        for event in job_errors(job_log):
            writers["events"].writerow(event)
            if "first_job_error" not in summary or event["start_ns"] < summary["first_job_error"]["start_ns"]:
                summary["first_job_error"] = {key: event[key] for key in ("line", "start_ns", "end_ns")}
    if not summary["metrics"]:
        raise ValueError("No app or driver samples")
    # Correlate only observed request brackets, never nearest-neighbor values or missing data.
    targets = dict(summary["first_collapse"])
    if summary["stalls"]["first"]:
        targets["first_app_stall"] = summary["stalls"]["first"]
    summary["temporal_error_overlap"] = {name: {"error_increments": 0, "alert_reads": 0, "observation_errors": 0, "operation_errors": 0}
                                         for name in targets}
    with paths["events"].open() as handle:
        for event in csv.DictReader(handle):
            category = {"error-increment": "error_increments", "tapealert": "alert_reads",
                        "observation-error": "observation_errors", "operation-error": "operation_errors",
                        "job-error": "operation_errors"}.get(event["kind"])
            if not category or not event["start_ns"] or not event["end_ns"]:
                continue
            for name, target in targets.items():
                if int(event["start_ns"]) <= target["end_ns"] and int(event["end_ns"]) >= target["start_ns"]:
                    summary["temporal_error_overlap"][name][category] += 1
    summary.update(input_rows=count, origin_ns=origin, artifacts={key: str(path) for key, path in paths.items() if key not in ("curves", "boundary")})
    summary["plot"] = render(paths, summary) if plot else "disabled"
    if summary["plot"] == "written":
        summary["artifacts"]["curves"] = str(paths["curves"])
        summary["artifacts"]["curves_png"] = str(paths["curves"].with_suffix(".png"))
        if summary.get("first_job_error"):
            summary["artifacts"]["boundary"] = str(paths["boundary"])
            summary["artifacts"]["boundary_png"] = str(paths["boundary"].with_suffix(".png"))
    paths["summary"].write_text(json.dumps(summary, indent=2, allow_nan=False) + "\n")
    return summary


def render(paths, summary):
    try:
        import matplotlib
    except ImportError:
        return "unavailable: matplotlib is not installed"
    matplotlib.use("Agg")
    from matplotlib import pyplot as plt
    series = {metric: ([], [], []) for metric in METRICS}
    origin = summary["origin_ns"] or 0
    with paths["observations"].open() as handle:
        for row in csv.DictReader(handle):
            if not row["start_ns"] or not row["end_ns"]:
                continue
            x, y, uncertainty = series[row["metric"]]
            start, end = int(row["start_ns"]), int(row["end_ns"])
            middle = ((start - origin) + (end - origin)) / 2e9
            if row["continuity"]:
                x.append(middle)
                y.append(math.nan)
                uncertainty.append(0)
            x.append(middle)
            y.append(float(row["value"]) if row["value"] else math.nan)
            uncertainty.append((end - start) / 2e9)
    figure, axes = plt.subplots(7, 1, figsize=(13, 20), sharex=True)
    groups = (CAPACITY, ("eod", "head_position"), ("app_copied", "drive_received", "drive_written"),
              (), ("write_1", "write_3", "write_4", "write_6"), ("repositions",), ("tapealert_active",))
    for axis, group in zip(axes, groups):
        for metric in group:
            x, y, uncertainty = series[metric]
            scale = 1e9 if METRICS[metric][0] == "bytes" else 1
            axis.errorbar(x, [value / scale for value in y], xerr=uncertainty, marker=".", markersize=3,
                          linewidth=0.8, label=METRICS[metric][1])
    with paths["intervals"].open() as handle:
        rates = {metric: ([], []) for metric in ("app_copied", "drive_received", "drive_written")}
        for row in csv.DictReader(handle):
            if row["metric"] not in rates or not row["end_ns"]:
                continue
            x, y = rates[row["metric"]]
            x.append((int(row["end_ns"]) - origin) / 1e9)
            y.append(float(row["rate_per_second"]) / 1e6 if row["valid"] == "True" else math.nan)
        for metric, (x, y) in rates.items():
            axes[3].plot(x, y, ".-", linewidth=0.8, label=METRICS[metric][1] + " delta / elapsed time")
    for metric, event in summary["first_nonpositive"].items():
        left, right = (event["start_ns"] - origin) / 1e9, (event["end_ns"] - origin) / 1e9
        collapse = summary["first_collapse"].get(metric)
        if collapse:
            left = (collapse["start_ns"] - origin) / 1e9
        axes[0].axvspan(left, right, alpha=0.15)
        axes[0].axvline(right, linestyle=":", label=metric + " first observed <=0")
    if summary.get("first_job_error"):
        event = summary["first_job_error"]
        left, right = (event["start_ns"] - origin) / 1e9, (event["end_ns"] - origin) / 1e9
        for axis in axes:
            axis.axvspan(left, right, color="red", alpha=0.15)
        axes[0].axvline(left, color="red", linestyle=":", label="First Job error (1-second log interval)")
    for axis, label in zip(axes, ("Available estimate (decimal GB)", "Partition distance (decimal GB)",
                                "Cumulative bytes (decimal GB)", "Observed delta rate (decimal MB/s)",
                                "Error/rewrite cumulative counts", "Repositions / 100 MB", "Active alert flags in read")):
        axis.set_ylabel(label)
        axis.grid(alpha=0.2)
        axis.legend(fontsize=7, loc="best")
    axes[0].set_title("Filesystem vs raw P1 vs current-partition EW estimates; no absolute accuracy claim")
    axes[-1].set_xlabel("Seconds from first controller observation; horizontal bars show request uncertainty")
    figure.tight_layout()
    figure.savefig(paths["curves"], format="svg")
    figure.savefig(paths["curves"].with_suffix(".png"), dpi=130)
    plt.close(figure)
    if summary.get("first_job_error"):
        error_time = (summary["first_job_error"]["start_ns"] - origin) / 1e9
        detail, detail_axes = plt.subplots(3, 1, figsize=(12, 9), sharex=True)
        for axis, group, label in zip(detail_axes,
                (CAPACITY, ("drive_received", "drive_written"), ("write_4",)),
                ("Remaining estimate (decimal GB)", "Drive byte counters (decimal GB)", "CCQ rewritten sets (cumulative)")):
            for metric in group:
                x, y, uncertainty = series[metric]
                points = [(at - error_time, value, span) for at, value, span in zip(x, y, uncertainty)
                          if error_time - 15 <= at <= error_time + 15]
                scale = 1e9 if METRICS[metric][0] == "bytes" else 1
                axis.errorbar([p[0] for p in points], [p[1] / scale for p in points],
                              xerr=[p[2] for p in points], marker=".", linewidth=0.8, label=METRICS[metric][1])
            axis.axvspan(0, 1, color="red", alpha=0.15, label="First Job error (log precision)")
            axis.set_ylabel(label)
            axis.grid(alpha=0.2)
            axis.legend(fontsize=7, loc="best")
        detail_axes[0].set_title("Capacity observations around the first Job error")
        detail_axes[-1].set_xlim(-15, 15)
        detail_axes[-1].set_xlabel("Seconds relative to the recorded Job error; horizontal bars show request uncertainty")
        detail.tight_layout()
        detail.savefig(paths["boundary"], format="svg")
        detail.savefig(paths["boundary"].with_suffix(".png"), dpi=150)
        plt.close(detail)
    return "written"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("samples", type=Path)
    parser.add_argument("--output", required=True, type=Path, help="Output filename prefix")
    parser.add_argument("--no-plot", action="store_true")
    parser.add_argument("--job-log", type=Path, help="Optional same-host Job log; annotate its first error with the recorded time precision")
    parser.add_argument("--gap-factor", type=float, default=2, help="Break intervals exceeding this times the larger adjacent sampling period")
    args = parser.parse_args(argv)
    print(json.dumps(analyze_samples(args.samples, args.output, not args.no_plot, args.gap_factor, args.job_log), allow_nan=False))


if __name__ == "__main__":
    main()
