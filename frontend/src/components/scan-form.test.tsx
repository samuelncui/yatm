import { act, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { Location, PreviewPolicy, PreviewSettings, ScanResultPolicy, ScanSignaturePolicy } from "@/entity";
import type { InitialScan } from "@/pages/scan-input";
import { scanPreferencesKey } from "@/pages/scan-preferences";
import { ScanForm } from "./scan-form";

const { get, fileGet, create, capabilities, settings } = vi.hoisted(() => ({
  get: vi.fn(),
  fileGet: vi.fn(),
  create: vi.fn(),
  capabilities: vi.fn(),
  settings: vi.fn(),
}));
vi.mock("@/api", () => ({
  locationCli: { get },
  filesCli: { get: fileGet },
  scanJobCli: { create },
  previewCli: { getCapabilities: capabilities },
  settingsCli: { get: settings },
}));
vi.mock("./catalog-search-select", () => ({
  LocationSearchSelect: ({ onChange }: { onChange: (location: Location) => void }) => (
    <button onClick={() => onChange(Location.create({ id: 2n, name: "New source", rootPath: "/new" }))}>Select new Location</button>
  ),
  MediaSearchSelect: () => null,
}));
vi.mock("./scan-selection-dialog", () => ({ ScanSelectionDialog: () => null }));
const source = Location.create({ id: 1n, name: "Source", rootPath: "/source" });
const initial = (): InitialScan => ({
  target: { kind: "location", id: "1" },
  explicit: true,
  error: "",
  saved: undefined,
  options: { signaturePolicy: ScanSignaturePolicy.KNOWN_ONLY, resultPolicy: ScanResultPolicy.REPORT_ONLY, compare: false, previewPolicy: PreviewPolicy.NONE },
});
const Job = () => <p>Created Job {useParams().id}</p>;
const show = (value = initial()) =>
  render(
    <MemoryRouter initialEntries={["/scan"]}>
      <Routes>
        <Route path="/scan" element={<ScanForm initial={value} />} />
        <Route path="/jobs/:id" element={<Job />} />
      </Routes>
    </MemoryRouter>,
  );
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  get.mockReturnValue({ response: Promise.resolve({ location: source }) });
  create.mockReturnValue({ response: Promise.resolve({ job: { id: 43n } }) });
  capabilities.mockReturnValue({ response: Promise.resolve({ available: true }) });
  settings.mockReturnValue({ response: Promise.resolve({ value: { value: { oneofKind: "preview", preview: PreviewSettings.create({ enabled: true }) } } }) });
});
afterEach(() => vi.restoreAllMocks());

it("navigates to a successfully created Scan even when saving its preferences fails", async () => {
  let complete!: (reply: unknown) => void;
  create.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  show();
  const start = screen.getByRole("button", { name: "Start scan" });
  await waitFor(() => expect(start).toBeEnabled());
  const write = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("Storage denied");
  });
  await userEvent.click(start);
  expect(write).not.toHaveBeenCalled();
  await act(async () => complete({ job: { id: 43n } }));
  await screen.findByText("Created Job 43");
  expect(write).toHaveBeenCalledExactlyOnceWith(
    scanPreferencesKey,
    JSON.stringify({
      kind: "location",
      location: { id: "1", rootPath: "/source" },
      ...initial().options,
    }),
  );
  expect(create).toHaveBeenCalledOnce();
});

it("keeps the form's draft and does not write preferences after failed creation", async () => {
  create.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Creation offline")) }));
  show();
  const start = screen.getByRole("button", { name: "Start scan" });
  await waitFor(() => expect(start).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  await userEvent.click(screen.getByRole("checkbox", { name: "Count matching archived copies" }));
  const write = vi.spyOn(Storage.prototype, "setItem");
  await userEvent.click(start);
  await screen.findByText("Creation offline");
  expect(write).not.toHaveBeenCalled();
  expect(screen.getByRole("checkbox", { name: "Count matching archived copies" })).toBeChecked();
  expect(start).toBeEnabled();
});

it("does not resolve old shortcut paths or overwrite a manually chosen Location after cancellation", async () => {
  let complete!: (reply: unknown) => void;
  get.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  show({ ...initial(), target: { kind: "location", id: "1", paths: ["old-range"] } });
  const signal = get.mock.calls[0][1].abort as AbortSignal;
  await userEvent.click(screen.getByRole("button", { name: "Select new Location" }));
  expect(signal.aborted).toBe(true);
  await act(async () => complete({ location: source }));
  expect(fileGet).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  await screen.findByText("Created Job 43");
  expect(create.mock.calls[0][0].spec.selections[0].target).toEqual({ oneofKind: "location", location: { locationId: 2n, path: "" } });
});

it("keeps signature and comparison tuning folded with an effective summary", async () => {
  show();
  await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
  expect(screen.getByRole("combobox", { name: "Results" })).toBeVisible();
  expect(screen.getByRole("combobox", { name: "Previews" })).toBeVisible();
  expect(screen.queryByRole("combobox", { name: "Signatures" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: /Advanced/ })).toHaveTextContent("Reuse known signatures only · No archived copy comparison");
  expect(screen.getByText(/Selected reads and Preview generation still run/)).toBeVisible();
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  expect(create).toHaveBeenCalledWith(
    expect.objectContaining({ spec: expect.objectContaining({ signaturePolicy: ScanSignaturePolicy.KNOWN_ONLY, compareLibrary: false }) }),
  );
});
it("explains known-only Preview skips and changes policy only on request", async () => {
  const input = initial();
  input.options.previewPolicy = PreviewPolicy.MISSING_ONLY;
  show(input);
  await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
  expect(screen.getByText(/Preview generation skips files whose signatures are unknown/)).toBeVisible();
  expect(screen.getByRole("button", { name: /Advanced/ })).toHaveTextContent("Reuse known signatures only");
  await userEvent.click(screen.getByRole("button", { name: "Read new and changed files" }));
  expect(screen.queryByText(/Preview generation skips files whose signatures are unknown/)).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  expect(create).toHaveBeenCalledWith(
    expect.objectContaining({
      spec: expect.objectContaining({ signaturePolicy: ScanSignaturePolicy.FILL_MISSING, previewPolicy: PreviewPolicy.MISSING_ONLY }),
    }),
  );
});
it("retries a failed Preview lookup in place without changing the Scan selection", async () => {
  const input = initial();
  input.options.previewPolicy = PreviewPolicy.REGENERATE_ALL;
  capabilities.mockReturnValueOnce({ response: Promise.reject(new Error("Temporary Preview failure")) });
  show(input);
  await screen.findByText(/Temporary Preview failure/);
  expect(screen.queryByRole("link", { name: "Settings" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
  expect(screen.getByRole("combobox", { name: "Previews" })).toHaveTextContent("Regenerate all");
  await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
  expect(create).toHaveBeenCalledWith(
    expect.objectContaining({
      spec: expect.objectContaining({
        selections: [expect.objectContaining({ target: { oneofKind: "location", location: { locationId: 1n, path: "" } } })],
        previewPolicy: PreviewPolicy.REGENERATE_ALL,
      }),
    }),
  );
});
