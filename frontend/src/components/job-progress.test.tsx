import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import { EstimateState, Job, JobKind, JobPhase, JobStatus, Progress, StageProgress, ProgressUnit } from "@/entity";
import { JobProgressFrame, counterRows, isJobRunning, jobStatusLabel, stageView } from "@/components/job-progress";

const job = (value: Parameters<typeof Job.create>[0]) => Job.create(value);

const stage = (value: Parameters<typeof StageProgress.create>[0]) => StageProgress.create(value);

const progress = (value: Parameters<typeof Progress.create>[0]) => Progress.create(value);

describe("Job card phase rules", () => {
  // One row per phase × kind the card contract covers: it names the phase, whether the bar may be
  // determinate, the counter wording and the ETA text. A phase without a denominator never has a
  // percentage, whatever business total the Job knows.
  const cases: Array<{
    name: string;
    job: ReturnType<typeof job>;
    progress: Progress | null;
    percentage: number | undefined;
    counter: string;
    phase: string;
    eta: string;
    files: string | number;
    bytes?: string | number;
    totalFileCount: string | number;
  }> = [
    {
      name: "Scan discovery reports only what it found",
      job: job({ kind: JobKind.SCAN, status: JobStatus.PREPARING, phase: JobPhase.INDEXING }),
      progress: progress({ copiedFileCount: 25n, copiedBytes: 1024n }),
      percentage: undefined,
      counter: "Found",
      phase: "indexing",
      eta: "--",
      files: 25,
      bytes: "1 KB",
      totalFileCount: "--",
    },
    {
      name: "Scan queued for Media keeps its frozen found count without a percentage",
      job: job({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.QUEUED }),
      progress: progress({
        copiedFileCount: 12n,
        copiedBytes: 2048n,
        totalFileCount: 12n,
        totalBytes: 2048n,
        totalKnown: true,
        stage: stage({ phase: JobPhase.QUEUED, unit: ProgressUnit.ITEMS, completed: 12n }),
      }),
      percentage: undefined,
      counter: "Processed",
      phase: "Queued — waiting for Media",
      eta: "--",
      files: 12,
      bytes: "2 KB",
      totalFileCount: 12,
    },
    {
      name: "Archive content measures scope bytes with an estimate",
      job: job({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.PROCESSING_CONTENT }),
      progress: progress({
        totalKnown: true,
        copiedFileCount: 4n,
        copiedBytes: 4096n,
        totalFileCount: 10n,
        totalBytes: 8192n,
        speedBytesPerSecond: 512n,
        averageSpeedBytesPerSecond: 256n,
        stage: stage({
          windowId: "attempt",
          phase: JobPhase.PROCESSING_CONTENT,
          unit: ProgressUnit.BYTES,
          completed: 4096n,
          total: 8192n,
          estimateState: EstimateState.ESTIMATED,
          remainingSeconds: 42n,
          ratePerSecond: 128,
        }),
      }),
      percentage: 50,
      counter: "Processed",
      phase: "processing content",
      eta: "About 0:42",
      files: 4,
      bytes: "4 KB",
      totalFileCount: 10,
    },
    {
      name: "Comparison uses item counts and never an estimate",
      job: job({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.COMPARING_CONTENT }),
      progress: progress({
        totalKnown: true,
        copiedFileCount: 1n,
        copiedBytes: 10n,
        totalFileCount: 4n,
        totalBytes: 40n,
        stage: stage({ phase: JobPhase.COMPARING_CONTENT, unit: ProgressUnit.ITEMS, completed: 1n, total: 4n }),
      }),
      percentage: 25,
      counter: "Compared",
      phase: "comparing content",
      eta: "--",
      files: 1,
      bytes: "10 B",
      totalFileCount: 4,
    },
    {
      name: "Preview measures decoder items and reports estimating",
      job: job({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.GENERATING_PREVIEWS }),
      progress: progress({
        totalKnown: true,
        copiedFileCount: 2n,
        copiedBytes: 20n,
        totalFileCount: 8n,
        totalBytes: 80n,
        stage: stage({
          phase: JobPhase.GENERATING_PREVIEWS,
          unit: ProgressUnit.ITEMS,
          completed: 2n,
          total: 8n,
          estimateState: EstimateState.ESTIMATING,
          ratePerSecond: 1.5,
        }),
      }),
      percentage: 25,
      counter: "Generated",
      phase: "generating previews",
      eta: "Estimating…",
      files: 2,
      totalFileCount: 8,
    },
    {
      name: "Publication names its phase without inventing a denominator",
      job: job({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.PUBLISHING_SOURCE }),
      progress: progress({ copiedFileCount: 9n, copiedBytes: 90n }),
      percentage: undefined,
      counter: "Processed",
      phase: "publishing source",
      eta: "--",
      files: 9,
      bytes: "90 B",
      totalFileCount: "--",
    },
    {
      name: "Completed Scan reports processed manifest items",
      job: job({ kind: JobKind.SCAN, status: JobStatus.COMPLETED, phase: JobPhase.COMPLETED }),
      progress: progress({
        totalKnown: true,
        copiedFileCount: 6n,
        copiedBytes: 60n,
        totalFileCount: 6n,
        totalBytes: 60n,
        stage: stage({ phase: JobPhase.COMPLETED, unit: ProgressUnit.ITEMS, completed: 6n, total: 6n }),
      }),
      percentage: 100,
      counter: "Processed",
      phase: "completed",
      eta: "--",
      files: 6,
      bytes: "60 B",
      totalFileCount: 6,
    },
    {
      name: "Archive waiting for storage reports unknown counters before the first snapshot",
      job: job({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED }),
      progress: null,
      percentage: undefined,
      counter: "Copied",
      phase: "--",
      eta: "--",
      files: "--",
      bytes: "--",
      totalFileCount: "--",
    },
    {
      name: "A loading card reports unknown counters instead of measured zeros",
      job: job({ kind: JobKind.RESTORE, status: JobStatus.PREPARING, phase: JobPhase.INDEXING }),
      progress: null,
      percentage: undefined,
      counter: "Found",
      phase: "indexing",
      eta: "--",
      files: "--",
      bytes: "--",
      totalFileCount: "--",
    },
    {
      name: "Restore copy reports copied counters and its own label",
      job: job({ kind: JobKind.RESTORE, status: JobStatus.READY, phase: JobPhase.COPYING_FROM_MEDIA }),
      progress: progress({
        totalKnown: true,
        copiedFileCount: 3n,
        copiedBytes: 30n,
        totalFileCount: 5n,
        totalBytes: 50n,
        stage: stage({ phase: JobPhase.COPYING_FROM_MEDIA, unit: ProgressUnit.BYTES, completed: 30n, total: 50n }),
      }),
      percentage: 60,
      counter: "Copied",
      phase: "copying from media",
      eta: "--",
      files: 3,
      bytes: "30 B",
      totalFileCount: 5,
    },
  ];

  it.each(cases)("$name", ({ job: value, progress: snapshot, percentage, counter, phase, eta, files, bytes, totalFileCount }) => {
    const view = stageView(value, snapshot);
    expect(view.percentage).toBe(percentage);
    expect(view.counterLabel).toBe(counter);
    const rows = counterRows(view, snapshot);
    const row = (name: string) => rows.find((value) => value.name === name)?.value;
    expect(row("Current Phase")).toBe(phase);
    expect(row("Stage Remaining")).toBe(eta);
    expect(row(`${counter} Files`)).toBe(files);
    // A phase measured in items alone reports no byte counter at all.
    if (bytes === undefined) {
      expect(row(`${counter} Bytes`)).toBeUndefined();
    } else {
      expect(row(`${counter} Bytes`)).toBe(bytes);
    }
    expect(row("Total Files")).toBe(totalFileCount);
    // The bar animates only while the Job is working on a stage of unknown length; a waiting or
    // settled Job shows a still bar at the percentage it has.
    const page = render(
      <MemoryRouter>
        <JobProgressFrame view={view} rows={rows} />
      </MemoryRouter>,
    );
    const bar = screen.getByRole("progressbar");
    if (view.activity === "active" && percentage === undefined) {
      expect(bar.className).toContain("indeterminate");
      expect(bar).not.toHaveAttribute("aria-valuenow");
    } else {
      expect(bar.className).not.toContain("indeterminate");
      expect(Number(bar.getAttribute("aria-valuenow"))).toBeCloseTo(percentage ?? 0, 5);
    }
    page.unmount();
  });

  it.each([
    [JobKind.SCAN, "Processed"],
    [JobKind.ARCHIVE, "Copied"],
    [JobKind.RESTORE, "Copied"],
  ] as const)("labels the retained counters of an idle completed %s Job", (kind, label) => {
    const snapshot = progress({ copiedFileCount: 3n, copiedBytes: 512n, totalFileCount: 3n, totalBytes: 512n, totalKnown: true });
    const view = stageView(job({ kind, status: JobStatus.COMPLETED, phase: JobPhase.UNSPECIFIED }), snapshot);
    const rows = counterRows(view, snapshot);
    expect(rows).toContainEqual({ name: `${label} Files`, value: 3 });
    expect(rows).toContainEqual({ name: `${label} Bytes`, value: "512 B" });
    expect(view.activity).toBe("terminal");
  });

  it("classifies an idle ready Job separately from a running Job", () => {
    // A ready Job whose runner is between attempts is not active and waits for its operator.
    const ready = job({ kind: JobKind.RESTORE, status: JobStatus.READY, phase: JobPhase.UNSPECIFIED });
    expect(stageView(ready, null).activity).toBe("terminal");
    expect(jobStatusLabel(ready)).toBe("Choose archive storage to read");

    const running = job({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.COPYING_TO_MEDIA });
    expect(isJobRunning(running)).toBe(true);
    expect(jobStatusLabel(running)).toBe("copying to media");

    const queued = job({ kind: JobKind.RESTORE, status: JobStatus.READY, phase: JobPhase.QUEUED });
    expect(stageView(queued, null).activity).toBe("waiting");
    expect(isJobRunning(queued)).toBe(true);
    expect(isJobRunning(ready)).toBe(false);
    expect(jobStatusLabel(queued)).toBe("Queued — waiting for Media");

    // A failed Job is terminal, names its reason and offers no Cancel.
    const failed = job({ kind: JobKind.SCAN, status: JobStatus.FAILED, phase: JobPhase.UNSPECIFIED, error: "injected failure" });
    expect(stageView(failed, null).activity).toBe("terminal");
    expect(jobStatusLabel(failed)).toBe("Failed");
  });

  it.each([JobStatus.COMPLETED, JobStatus.FAILED])("keeps live cleanup active after durable status %s until the runner becomes idle", (status) => {
    const cleanup = job({ kind: JobKind.ARCHIVE, status, phase: JobPhase.FINALIZING_MEDIA });
    expect(stageView(cleanup, progress({ stage: stage({ phase: JobPhase.FINALIZING_MEDIA }) })).activity).toBe("active");
    expect(isJobRunning(cleanup)).toBe(true);
    expect(jobStatusLabel(cleanup)).toBe("finalizing media");

    const idle = job({ ...cleanup, phase: JobPhase.UNSPECIFIED });
    expect(stageView(idle, progress({ stage: stage({ phase: JobPhase.UNSPECIFIED }) })).activity).toBe("terminal");
    expect(isJobRunning(idle)).toBe(false);
    expect(jobStatusLabel(idle)).toBe(status === JobStatus.COMPLETED ? "Completed" : "Failed");
  });

  it("shows a zero-byte workload as items instead of a false byte percentage", () => {
    const value = job({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.COPYING_TO_MEDIA });
    const snapshot = progress({
      totalKnown: true,
      copiedFileCount: 2n,
      totalFileCount: 4n,
      stage: stage({
        phase: JobPhase.COPYING_TO_MEDIA,
        unit: ProgressUnit.ITEMS,
        completed: 2n,
        total: 4n,
        estimateState: EstimateState.ESTIMATING,
        ratePerSecond: 0.5,
      }),
    });
    const view = stageView(value, snapshot);
    expect(view.unit).toBe(ProgressUnit.ITEMS);
    expect(view.percentage).toBe(50);
    const rows = counterRows(view, snapshot);
    expect(rows.find((row) => row.name === "Current Speed")?.value).toBe("0.5 items/s");
    expect(rows.find((row) => row.name === "Copied Bytes")?.value).toBe("0 B");
  });

  it("reports the server estimate without the client recording a sample", () => {
    // The estimate is whatever the work path already recorded; a rendered frame only reads it.
    const value = job({ kind: JobKind.ARCHIVE, status: JobStatus.READY, phase: JobPhase.COPYING_TO_MEDIA });
    const snapshot = progress({
      totalKnown: true,
      copiedBytes: 500n,
      totalBytes: 1000n,
      stage: stage({
        windowId: "attempt/window",
        phase: JobPhase.COPYING_TO_MEDIA,
        unit: ProgressUnit.BYTES,
        completed: 500n,
        total: 1000n,
        estimateState: EstimateState.ESTIMATED,
        remainingSeconds: 30n,
        ratePerSecond: 16.6,
      }),
    });
    const first = stageView(value, snapshot);
    expect(first.percentage).toBe(50);
    expect(first.eta).toBe("About 0:30");
    // Reading the same snapshot again cannot change the estimate.
    expect(stageView(value, snapshot)).toEqual(first);
    expect(snapshot.stage!.remainingSeconds).toBe(30n);
  });

  it("counts generated previews without reporting the bytes of their source", () => {
    // A live Preview stage reports decoder items. The byte counter beside a copy stage's items
    // describes the content it moved; here it would describe the source of the previews, so the
    // card reports the stage's own unit and keeps the Job's totals apart from it.
    const value = job({ kind: JobKind.SCAN, status: JobStatus.PREPARING, phase: JobPhase.GENERATING_PREVIEWS });
    const snapshot = progress({
      totalKnown: true,
      copiedFileCount: 7n,
      copiedBytes: 700n,
      totalFileCount: 20n,
      totalBytes: 2048n,
      stage: stage({
        phase: JobPhase.GENERATING_PREVIEWS,
        unit: ProgressUnit.ITEMS,
        completed: 7n,
        total: 20n,
        estimateState: EstimateState.ESTIMATING,
        ratePerSecond: 2.5,
      }),
    });
    const view = stageView(value, snapshot);
    expect(view.activity).toBe("active");
    expect(view.counterLabel).toBe("Generated");
    const rows = counterRows(view, snapshot);
    const row = (name: string) => rows.find((value) => value.name === name)?.value;
    expect(row("Generated Files")).toBe(7);
    expect(row("Generated Bytes")).toBeUndefined();
    expect(row("Current Speed")).toBe("2.5 items/s");
    expect(row("Total Files")).toBe(20);
    expect(row("Total Bytes")).toBe("2 KB");
  });

  it("never renders a percentage for a stage that reports no total", () => {
    const value = job({ kind: JobKind.SCAN, status: JobStatus.READY, phase: JobPhase.VERIFYING_MEDIA });
    const snapshot = progress({
      copiedFileCount: 12n,
      copiedBytes: 4096n,
      totalFileCount: 12n,
      totalBytes: 4096n,
      totalKnown: true,
      stage: stage({ phase: JobPhase.VERIFYING_MEDIA, unit: ProgressUnit.BYTES, completed: 4096n }),
    });
    const view = stageView(value, snapshot);
    expect(view.percentage).toBeUndefined();
    const rows = counterRows(view, snapshot);
    expect(rows.find((row) => row.name === "Processed Files")?.value).toBe(12);
    expect(rows.find((row) => row.name === "Total Files")?.value).toBe(12);
  });
});
