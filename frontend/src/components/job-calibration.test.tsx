import type { PropsWithChildren } from "react";
import { screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@/state/test-render";
import {
  CalibrationMove,
  DeviceSettings,
  GetCalibrationJobProgressResponse,
  Job,
  JobKind,
  JobPhase,
  JobStatus,
  SettingsGroup,
  SettingsValue,
  TapeReadParameters,
} from "@/entity";

const { progress, settingsGet } = vi.hoisted(() => ({ progress: vi.fn(), settingsGet: vi.fn() }));
vi.mock("@/api", () => ({ calibrationJobCli: { getProgress: progress }, settingsCli: { get: settingsGet }, jobCli: {} }));
vi.mock("@/tools", async (original) => ({
  ...(await original<typeof import("@/tools")>()),
  useSharedIntersectionObserver: () => true,
}));
import { CalibrationCard } from "./job-calibration";

const call = (value: unknown) => ({ response: Promise.resolve(value) });
const wrapper = ({ children }: PropsWithChildren) => <MemoryRouter>{children}</MemoryRouter>;
const moves = [
  CalibrationMove.create({ fromWrap: 0, fromSpan: 0.1, toWrap: 0, toSpan: 0.6, seconds: 12.345 }),
  CalibrationMove.create({ fromWrap: 0, fromSpan: 0.6, toWrap: 1, toSpan: 0.25, seconds: 20.5 }),
];
const parameters = TapeReadParameters.create({
  appliesTo: { densityCode: 0x58, driveSerial: "HU1234" },
  positioning: { baseSeconds: 4.25, seekSecondsPerWrap: 52.5, approachReversalSeconds: 2.75, bufferBytes: 1_500_000_000n },
  reading: { bytesPerSecond: 140_000_000 },
});
const job = (value: Parameters<typeof Job.create>[0]) => Job.create({ id: 12n, kind: JobKind.CALIBRATION, targetName: "drive", ...value });

beforeEach(() => {
  vi.resetAllMocks();
  settingsGet.mockReturnValue(call({ value: SettingsValue.create({ value: { oneofKind: "device", device: DeviceSettings.create() } }) }));
});

describe("Calibration Job", () => {
  it("shows measured moves out of the sequence, elapsed time and Cancel while calibrating", async () => {
    progress.mockReturnValue(call(GetCalibrationJobProgressResponse.create({ moves, totalMoves: 22, elapsedMs: 65_000n, spec: { device: "drive" } })));
    render(<CalibrationCard job={job({ status: JobStatus.PREPARING, phase: JobPhase.CALIBRATING })} />, { wrapper });
    expect(screen.getByRole("link", { name: "Calibration · drive · Job 12" })).toBeInTheDocument();
    expect(await screen.findByTitle("2 / 22")).toBeInTheDocument();
    expect(screen.getByTitle("1:05")).toBeInTheDocument();
    expect(screen.getAllByText("calibrating").length).toBeGreaterThan(0);
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", String((2 / 22) * 100));
    const rows = within(screen.getByRole("table", { name: "Measured moves" })).getAllByRole("row");
    expect(rows.map((row) => row.textContent)).toEqual(["From wrapFrom spanTo wrapTo spanSeconds", "00.10000.60012.35", "00.60010.25020.50"]);
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete Job" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Recreate" })).not.toBeInTheDocument();
    expect(progress).toHaveBeenCalledWith({ id: 12n }, { abort: expect.any(AbortSignal) });
  });

  it("names the fitted parameters with units and links to the device's Read parameters", async () => {
    progress.mockReturnValue(call(GetCalibrationJobProgressResponse.create({ moves, totalMoves: 22, parameters, spec: { device: "drive a" } })));
    render(<CalibrationCard job={job({ status: JobStatus.COMPLETED })} />, { wrapper });
    expect(await screen.findByText(/Fitted read parameters · Density code 88 · Serial number HU1234/)).toBeInTheDocument();
    for (const text of [
      "Fixed positioning time (base_seconds): 4.25 s",
      "Seek time for one span (seek_seconds_per_wrap): 52.50 s",
      "Added time when arriving against the wrap direction (approach_reversal_seconds): 2.75 s",
      "Read rate (bytes_per_second): 140.0 MB/s",
      "Read-ahead buffer (buffer_bytes): 1.50 GB",
    ]) {
      expect(screen.getByText((_, element) => element?.tagName === "SPAN" && element.textContent === text)).toBeInTheDocument();
    }
    expect(screen.getByRole("link", { name: "Read parameters in Device settings" })).toHaveAttribute("href", "/settings/devices?device=drive%20a");
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "100");
    expect(screen.getByRole("button", { name: "Delete Job" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    // A Job that was already settled when shown stored nothing new in this view.
    expect(settingsGet).not.toHaveBeenCalled();
  });

  it("shows a failure reason like other kinds, without fitted parameters", async () => {
    progress.mockReturnValue(call(GetCalibrationJobProgressResponse.create({ moves: moves.slice(0, 1), totalMoves: 22 })));
    render(<CalibrationCard job={job({ status: JobStatus.FAILED, error: "moves do not fit the positioning model" })} />, { wrapper });
    expect(screen.getByRole("alert")).toHaveTextContent("moves do not fit the positioning model");
    expect(await screen.findByTitle("1 / 22")).toBeInTheDocument();
    expect(screen.queryByText(/Fitted read parameters/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete Job" })).toBeInTheDocument();
  });

  it("reads Device settings again after observing the Job complete", async () => {
    progress.mockReturnValue(call(GetCalibrationJobProgressResponse.create({ moves, totalMoves: 22 })));
    const view = render(<CalibrationCard job={job({ status: JobStatus.PREPARING, phase: JobPhase.CALIBRATING })} />, { wrapper });
    await screen.findByTitle("2 / 22");
    expect(settingsGet).not.toHaveBeenCalled();
    view.rerender(<CalibrationCard job={job({ status: JobStatus.COMPLETED })} />);
    await waitFor(() => expect(settingsGet).toHaveBeenCalledExactlyOnceWith({ group: SettingsGroup.DEVICE }));
  });
});
