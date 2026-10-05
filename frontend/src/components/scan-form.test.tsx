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
  settings.mockReturnValue({ response: Promise.resolve({ value: { oneofKind: "preview", preview: PreviewSettings.create({ enabled: true }) } }) });
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
  await userEvent.click(screen.getByRole("checkbox", { name: "Find matching Library content" }));
  const write = vi.spyOn(Storage.prototype, "setItem");
  await userEvent.click(start);
  await screen.findByText("Creation offline");
  expect(write).not.toHaveBeenCalled();
  expect(screen.getByRole("checkbox", { name: "Find matching Library content" })).toBeChecked();
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
