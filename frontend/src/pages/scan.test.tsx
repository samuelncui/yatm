import { GeneralSettings, PreviewSettings, SettingsGroup, SettingsValue } from "@/entity";
import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { MemoryRouter, Route, Routes, useNavigate, useParams } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  FileScope,
  FileSelection,
  Location,
  LocationSelection,
  Media,
  MediaAccess,
  MediaKind,
  PreviewPolicy,
  ScanJobSpec,
  ScanResultPolicy as Result,
  ScanSignaturePolicy as Signature,
} from "@/entity";

const { get, mediaList, scan, latest, fileGet, capabilities, previewSettings, librarySettings, settingsUpdate, importPositions } = vi.hoisted(() => ({
  get: vi.fn(),
  mediaList: vi.fn(),
  scan: vi.fn(),
  latest: vi.fn(),
  fileGet: vi.fn(),
  capabilities: vi.fn(),
  previewSettings: vi.fn(),
  librarySettings: vi.fn(),
  settingsUpdate: vi.fn(),
  importPositions: vi.fn(),
}));
vi.mock("@/api", () => ({
  locationCli: { get },
  mediaCli: { list: mediaList },
  filesCli: { get: fileGet, importPositions },
  scanJobCli: { create: scan, getCreation: latest },
  previewCli: { getCapabilities: capabilities },
  // General settings hold the skipped confirmations; every other read is the Preview group the cases vary.
  settingsCli: {
    get: (request: { group: SettingsGroup }) => (request.group === SettingsGroup.GENERAL ? librarySettings(request) : previewSettings(request)),
    update: settingsUpdate,
  },
}));
vi.mock("@/components/scan-selection-dialog", () => ({
  ScanSelectionDialog: ({ entries, onChoose, onClose }: { entries: unknown[]; onChoose: (entries: unknown[]) => void; onClose: () => void }) => (
    <div role="dialog">
      <p>{entries.length} draft roots</p>
      <button onClick={onClose}>Cancel selection</button>
      <button onClick={() => onChoose(entries)}>Choose selection</button>
      <button onClick={() => onChoose([...entries, { selection: selection("Added"), name: "Added", path: "/photos/Added", isDir: true }])}>
        Add selection
      </button>
    </div>
  ),
}));

import { ScanBrowser } from "./scan";

const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 1n, name: "Photos", rootPath: "/photos" });
const disk = Media.create({ id: 3n, name: "Archive disk", kind: MediaKind.VOLUME, mounted: true, capabilities: { read: MediaAccess.RANDOM } });
const selection = (path: string) =>
  FileSelection.create({
    target: {
      oneofKind: "location",
      location: LocationSelection.create({
        locationId: 1n,
        path,
      }),
    },
    scope: FileScope.ALL,
  });
// The most recently created Scan, as the latest creation read returns it.
const lastScan = (spec: Partial<ScanJobSpec>) =>
  call({
    request: {
      priority: 1n,
      spec: ScanJobSpec.create({
        signaturePolicy: Signature.KNOWN_ONLY,
        resultPolicy: Result.REPORT_ONLY,
        previewPolicy: PreviewPolicy.NONE,
        writeSignatureCache: false,
        ...spec,
      }),
    },
  });
const filesState = (paths = ["Projects"]) => ({
  scan: {
    target: { kind: "files" as const, entries: paths.map((path) => ({ selection: selection(path), name: path, path: `/photos/${path}`, isDir: true })) },
  },
});
const Destination = () => <div>Job {useParams().id}</div>;
const StateRedirect = ({ state, search = "" }: { state: unknown; search?: string }) => {
  const navigate = useNavigate();
  useEffect(() => {
    void navigate({ pathname: "/scan", search }, { replace: true, state });
  }, [navigate, search, state]);
  return null;
};
const show = (path: string | { pathname: string; search?: string; state?: unknown } = "/scan") =>
  render(
    <MemoryRouter initialEntries={[typeof path === "string" ? path : "/redirect"]}>
      <Routes>
        <Route
          path="/redirect"
          element={<StateRedirect state={typeof path === "string" ? undefined : path.state} search={typeof path === "string" ? undefined : path.search} />}
        />
        <Route path="/scan" element={<ScanBrowser />} />
        <Route path="/jobs/:id" element={<Destination />} />
      </Routes>
    </MemoryRouter>,
  );
const Reenter = ({ state }: { state: unknown }) => {
  const navigate = useNavigate();
  return <button onClick={() => navigate("/scan", { state })}>Reenter scan</button>;
};
const showReenter = (state: unknown, next: unknown) =>
  render(
    <MemoryRouter initialEntries={["/redirect"]}>
      <Routes>
        <Route path="/redirect" element={<StateRedirect state={state} />} />
        <Route
          path="/scan"
          element={
            <>
              <ScanBrowser />
              <Reenter state={next} />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );

beforeEach(() => {
  capabilities.mockReturnValue(call({ available: true }));
  previewSettings.mockReturnValue(call({ value: { value: { oneofKind: "preview", preview: PreviewSettings.create({ enabled: true }) } } }));
  vi.clearAllMocks();
  localStorage.clear();
  librarySettings.mockReturnValue(call({ value: SettingsValue.create({ value: { oneofKind: "general", general: GeneralSettings.create() } }) }));
  settingsUpdate.mockImplementation(({ value }: { value: SettingsValue }) => call({ value: SettingsValue.clone(value) }));
  get.mockReturnValue(call({ location: source }));
  mediaList.mockReturnValue(call({ media: [disk], hasMore: false }));
  scan.mockReturnValue(call({ job: { id: 43n } }));
  // No Scan was created before.
  latest.mockReturnValue(call({}));
  fileGet.mockReturnValue(call({}));
  importPositions.mockReturnValue(call({ fileCount: 12n, directoryCount: 3n, skippedFileCount: 2n, existingCount: 1n, skippedUnsignedCount: 0n }));
});

describe("Scan form", () => {
  it.each(["disabled", "missing helper", "failed check"])(
    "blocks a requested Preview when generation is %s without silently changing policy",
    async (state) => {
      if (state === "disabled")
        previewSettings.mockReturnValue(call({ value: { value: { oneofKind: "preview", preview: PreviewSettings.create({ enabled: false }) } } }));
      if (state === "missing helper") capabilities.mockReturnValue(call({ available: false, reason: "Helper not installed" }));
      if (state === "failed check") capabilities.mockImplementation(() => ({ response: Promise.reject(new Error("Capability check failed")) }));
      const input = filesState();
      show({ pathname: "/scan", state: { scan: { ...input.scan, options: { previewPolicy: PreviewPolicy.REGENERATE_ALL } } } });
      if (state === "failed check") await screen.findByRole("button", { name: "Retry" });
      else await screen.findByRole("link", { name: "Settings" });
      await waitFor(() => expect(screen.queryByText("Checking Preview generation…")).not.toBeInTheDocument());
      expect(screen.getByRole("combobox", { name: "Previews" })).toHaveTextContent("Regenerate all");
      expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
      expect(scan).not.toHaveBeenCalled();
      await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
      expect(screen.getByRole("option", { name: "Generate missing" })).toHaveAttribute("aria-disabled", "true");
      await userEvent.click(screen.getByRole("option", { name: "Don’t generate" }));
      await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
      await waitFor(() => expect(scan).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ previewPolicy: PreviewPolicy.NONE }) })));
    },
  );
  it("always offers Location, Files, and Media with the full editable policy form", async () => {
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("combobox", { name: "Source" }));
    expect(screen.getByRole("option", { name: "Location" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Files" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Media" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.getByRole("combobox", { name: "Signatures" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Results" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Previews" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled();
  });

  it("supports an entire Location or an editable selected range", async () => {
    show("/scan?location=1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("combobox", { name: "Scope" }));
    expect(screen.getByRole("option", { name: "Entire Location" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: "Selected files and folders" }));
    expect(screen.getByRole("button", { name: "Edit selection" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  });

  it("submits an entire Location through the same selections contract as a selected range", async () => {
    show("/scan?location=1");
    await userEvent.click(await screen.findByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ mediaId: 0n, selections: [selection("")] }) }));
  });

  it("preserves Files roots across a cancelled edit and allows removal and clearing", async () => {
    show({ pathname: "/scan", state: filesState(["Projects", "Movies"]) });
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("button", { name: "Edit selection" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("2 draft roots");
    await userEvent.click(screen.getByRole("button", { name: "Cancel selection" }));
    expect(screen.getByText("Projects")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove /photos/Projects" }));
    expect(screen.queryByText("Projects")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  });

  it("commits a chosen Files root from the selection dialog", async () => {
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("button", { name: "Edit selection" }));
    await userEvent.click(screen.getByRole("button", { name: "Add selection" }));
    expect(screen.getByText("Added")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({ selections: [selection("Projects"), selection("Added")] }),
      }),
    );
  });

  it("keeps editable policies while changing source", async () => {
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    await userEvent.click(screen.getByRole("combobox", { name: "Signatures" }));
    await userEvent.click(screen.getByRole("option", { name: "Reuse known signatures only" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Source" }));
    await userEvent.click(screen.getByRole("option", { name: "Media" }));
    expect(screen.getByRole("combobox", { name: "Signatures" })).toHaveTextContent("Reuse known signatures only");
    await userEvent.click(screen.getByRole("combobox", { name: "Source" }));
    await userEvent.click(screen.getByRole("option", { name: "Location" }));
    expect(screen.getByRole("combobox", { name: "Signatures" })).toHaveTextContent("Reuse known signatures only");
  });

  it("starts from the latest Scan's whole Location and options without writing browser storage", async () => {
    latest.mockReturnValue(lastScan({ selections: [selection("")] }));
    show();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    expect(latest).toHaveBeenCalledExactlyOnceWith({ id: 0n, latest: true }, expect.objectContaining({ abort: expect.any(AbortSignal) }));
    expect(get).toHaveBeenCalledWith({ id: 1n }, expect.objectContaining({ abort: expect.anything() }));
    expect(screen.queryByText(/confirmation|Confirm imported/)).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Source" })).toHaveTextContent("Location");
    expect(screen.getByRole("combobox", { name: "Results" })).toHaveTextContent("View results only");
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(await screen.findByText("Job 43")).toBeInTheDocument();
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({
          mediaId: 0n,
          selections: [selection("")],
          signaturePolicy: Signature.KNOWN_ONLY,
          resultPolicy: Result.REPORT_ONLY,
          writeSignatureCache: false,
        }),
      }),
    );
    expect(localStorage.getItem("scan:last-options")).toBeNull();
  });

  it("starts from the latest Scan's Media", async () => {
    latest.mockReturnValue(lastScan({ mediaId: 3n }));
    show();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    expect(screen.getByRole("combobox", { name: "Source" })).toHaveTextContent("Media");
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({ spec: expect.objectContaining({ mediaId: 3n, selections: [], resultPolicy: Result.REPORT_ONLY }) }),
    );
  });

  it("keeps a routed Files target and applies the latest Scan's options to it", async () => {
    latest.mockReturnValue(lastScan({ selections: [selection("")] }));
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    expect(get).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({ selections: [selection("Projects")], signaturePolicy: Signature.KNOWN_ONLY }),
      }),
    );
  });

  it("applies the options of a latest Scan of selected files and leaves the Location to choose", async () => {
    latest.mockReturnValue(lastScan({ selections: [selection("Projects")] }));
    show();
    expect(await screen.findByRole("combobox", { name: "Source" })).toHaveTextContent("Location");
    expect(screen.getByRole("combobox", { name: "Results" })).toHaveTextContent("View results only");
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
    expect(get).not.toHaveBeenCalled();
  });

  it.each([
    ["no Scan was created before", () => call({})],
    ["the latest Scan cannot be read", () => ({ response: Promise.reject(new Error("Offline")) })],
  ])("starts from the built-in options when %s", async (_, reply) => {
    latest.mockImplementation(reply);
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    expect(screen.queryByText("Offline")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({
          signaturePolicy: Signature.FILL_MISSING,
          resultPolicy: Result.PUBLISH_ORIGINALS,
          previewPolicy: PreviewPolicy.NONE,
          writeSignatureCache: true,
        }),
      }),
    );
  });

  it("keeps the draft and writes no browser storage after failed creation", async () => {
    scan.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Busy")) }));
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(await screen.findByText("Busy")).toBeInTheDocument();
    expect(screen.getByText("Projects")).toBeInTheDocument();
    expect(localStorage.getItem("scan:last-options")).toBeNull();
  });

  it("reinitializes when the same route receives a new scan state", async () => {
    showReenter(filesState(["Projects"]), filesState(["Movies"]));
    await screen.findByText("Projects");
    await userEvent.click(screen.getByRole("button", { name: "Reenter scan" }));
    expect(await screen.findByText("Movies")).toBeInTheDocument();
    expect(screen.queryByText("Projects")).not.toBeInTheDocument();
  });

  it("does not let an aborted initial lookup overwrite an edited source", async () => {
    let resolve!: (value: { location: Location }) => void;
    get.mockReturnValue({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    show("/scan?location=1");
    expect(await screen.findByRole("status")).toHaveTextContent("Loading source…");
    await userEvent.click(screen.getByRole("combobox", { name: "Source" }));
    await userEvent.click(screen.getByRole("option", { name: "Files" }));
    await act(async () => resolve({ location: source }));
    expect(screen.getByRole("combobox", { name: "Source" })).toHaveTextContent("Files");
    expect(screen.queryByRole("combobox", { name: "Scope" })).not.toBeInTheDocument();
  });

  it("forces real reads and disables previews for sequential Tape verification", async () => {
    const tape = Media.create({ id: 4n, name: "Archive tape", kind: MediaKind.TAPE, capabilities: { read: MediaAccess.SEQUENTIAL } });
    mediaList.mockReturnValue(call({ media: [tape], hasMore: false }));
    show("/scan?media=4&result=verify");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    expect(screen.getByRole("combobox", { name: "Signatures" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Not supported on Tape")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(
      expect.objectContaining({
        spec: expect.objectContaining({
          mediaId: 4n,
          signaturePolicy: Signature.FORCE_READ,
          resultPolicy: Result.VERIFY_COPIES,
          previewPolicy: PreviewPolicy.NONE,
        }),
      }),
    );
  });

  it("does not start from the latest Scan's Location after it is removed", async () => {
    latest.mockReturnValue(lastScan({ selections: [selection("")] }));
    get.mockReturnValue(call({}));
    show();
    expect(await screen.findByText(/unavailable/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  });

  it("submits at most once while creation is pending", async () => {
    let resolve!: (value: { job: { id: bigint } }) => void;
    scan.mockReturnValue({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    show({ pathname: "/scan", state: filesState() });
    await screen.findByText("Projects");
    const start = screen.getByRole("button", { name: "Start scan" });
    await userEvent.click(start);
    expect(scan).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: "Creating…" })).toBeDisabled();
    await act(async () => resolve({ job: { id: 43n } }));
    expect(await screen.findByText("Job 43")).toBeInTheDocument();
  });

  it("counts a Media inventory admission before writing and remembers that the count is not needed", async () => {
    show("/scan?media=3");
    const add = await screen.findByRole("button", { name: "Add existing inventory to Library" });
    await userEvent.click(add);
    expect(await screen.findByRole("dialog")).toHaveTextContent("Add existing inventory to Library");
    expect(await screen.findByText("12 files from Archive disk will be added as independent Library files.")).toBeInTheDocument();
    expect(screen.getByText("3 folder entries · 1 already in Library · 2 without signatures")).toBeInTheDocument();
    expect(importPositions).toHaveBeenCalledWith(expect.objectContaining({ mediaId: 3n, dryrun: true }), expect.anything());

    await userEvent.click(screen.getByRole("checkbox", { name: "Don’t show this again" }));
    await userEvent.click(screen.getByRole("button", { name: "Add to Library" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(importPositions).toHaveBeenLastCalledWith(expect.objectContaining({ mediaId: 3n, dryrun: false }));
    expect(settingsUpdate).toHaveBeenCalledExactlyOnceWith({
      value: { value: { oneofKind: "general", general: GeneralSettings.create({ skipAddMediaConfirmation: true }) } },
    });
    expect(localStorage).toHaveLength(0);

    // The stored choice imports directly instead of asking for the count again.
    await waitFor(() => expect(document.querySelector(".MuiBackdrop-root")).not.toBeInTheDocument());
    importPositions.mockClear();
    await userEvent.click(screen.getByRole("button", { name: "Add existing inventory to Library" }));
    await waitFor(() => expect(importPositions).toHaveBeenCalledOnce());
    expect(importPositions).toHaveBeenCalledWith(expect.objectContaining({ mediaId: 3n, dryrun: false }));
    expect(scan).not.toHaveBeenCalled();
    importPositions.mockClear();
    await userEvent.click(screen.getByRole("button", { name: "Review what will be added" }));
    await screen.findByRole("dialog");
    expect(importPositions).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ mediaId: 3n, dryrun: true }), expect.anything());
    expect(scan).not.toHaveBeenCalled();
  });
});
