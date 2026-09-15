import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Media, MediaInspectReply, MediaKind } from "@/entity";

const { mediaList, deviceList, mediaInspect } = vi.hoisted(() => ({ mediaList: vi.fn(), deviceList: vi.fn(), mediaInspect: vi.fn() }));
vi.mock("@/api", () => ({ cli: { mediaList, deviceList, mediaInspect } }));
vi.mock("@/pages/jobs", async () => {
  const { createContext } = await import("react");
  return { RefreshContext: createContext(async () => {}) };
});
import { ReadMediaDialog } from "./read-media-dialog";

const tape = Media.create({ id: 4n, name: "Quarterly Tape", identity: "DEMO001L9", kind: MediaKind.TAPE });
const volume = Media.create({ id: 1n, name: "Review HDD", identity: "volume-uuid", kind: MediaKind.VOLUME, mounted: true });
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.resetAllMocks();
  mediaList.mockReturnValue(call({ media: [tape] }));
  deviceList.mockReturnValue(call({ devices: ["demo-drive"] }));
  mediaInspect.mockReturnValue(call(MediaInspectReply.create({ media: tape, identity: tape.identity })));
});

describe("Job-bound Media selection", () => {
  it("opens Tape verification directly on its required Tape and submits its inspected identity", async () => {
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={onRead} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText("Load the required Tape and select its drive.")).toBeInTheDocument();
    expect(screen.queryByText(/Reads every indexed file/)).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Storage type" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Volume" })).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start reading" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Start reading" }));
    expect(onRead).toHaveBeenCalledWith(
      expect.objectContaining({ backend: { oneofKind: "tape", tape: { device: "demo-drive" } }, expectedMediaId: 4n, expectedIdentity: tape.identity }),
    );
  });

  it("opens Volume verification on its fixed Volume without inspecting Tape devices", async () => {
    mediaList.mockReturnValue(call({ media: [volume] }));
    const onRead = vi.fn().mockResolvedValue(undefined);
    render(<ReadMediaDialog mediaIDs={[1n]} operation="scan" onRead={onRead} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Volume: Review HDD")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(deviceList).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Start reading" }));
    expect(onRead).toHaveBeenCalledWith(expect.objectContaining({ backend: { oneofKind: "volume", volume: { uuid: volume.identity } }, expectedMediaId: 1n }));
  });

  it("keeps an unmounted required Volume visible but unavailable", async () => {
    mediaList.mockReturnValue(call({ media: [Media.create({ ...volume, mounted: false })] }));
    render(<ReadMediaDialog mediaIDs={[1n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Mount this Volume before continuing.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("does not offer unrelated storage when required Media is missing", async () => {
    mediaList.mockReturnValue(call({ media: [volume] }));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Required Media is missing from the Library.")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("rejects an inserted Tape whose identity differs even if the catalog ID matches", async () => {
    mediaInspect.mockReturnValue(call(MediaInspectReply.create({ media: tape, identity: "ANOTHERL9" })));
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("The inserted Tape does not match this Job’s required Media.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
  });

  it("retains both storage types for a Restore with candidates on both", async () => {
    mediaList.mockReturnValue(call({ media: [tape, volume] }));
    render(<ReadMediaDialog mediaIDs={[4n, 1n]} operation="restore" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose a backup to read" }));
    expect(await screen.findByText("Volume: Review HDD")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Storage type" }));
    await userEvent.click(screen.getByRole("option", { name: "Tape" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start restore" })).toBeEnabled());
  });

  it("ignores a closed dialog's late Media response when reopened", async () => {
    let resolve!: (value: { media: Media[] }) => void;
    mediaList.mockReturnValueOnce({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    render(<ReadMediaDialog mediaIDs={[4n]} operation="scan" onRead={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(screen.getByRole("status")).toHaveTextContent("Loading required Media");
    expect(screen.getByRole("button", { name: "Start reading" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(screen.getByRole("button", { name: "Choose Media to read" }));
    expect(await screen.findByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    await act(async () => resolve({ media: [volume] }));
    expect(screen.getByText("Tape: Quarterly Tape", { selector: "strong" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
