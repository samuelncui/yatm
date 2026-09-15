import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useParams } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Location, Media, MediaKind, MediaAccess, OnlineBinding, PreviewPolicy, ScanResultPolicy as Result, ScanSignaturePolicy as Signature } from "@/entity";
const { list, get, mediaList, scan } = vi.hoisted(() => ({ list: vi.fn(), get: vi.fn(), mediaList: vi.fn(), scan: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list, get }, cli: { mediaList }, scanJobCli: { create: scan } }));
import { ScanBrowser } from "./scan";
import { scanPreferencesKey, type ScanPreferences } from "./scan-preferences";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 1n, name: "Photos", rootPath: "/photos", binding: OnlineBinding.CONFIRMED, bindingToken: "photos-binding" });
const Destination = () => <div>Job {useParams().id}</div>;
const storedOptions: ScanPreferences = {
  kind: "location",
  location: { id: "1", rootPath: "/photos", bindingToken: "photos-binding" },
  signaturePolicy: Signature.KNOWN_ONLY,
  resultPolicy: Result.REPORT_ONLY,
  compare: false,
  previewPolicy: PreviewPolicy.PREVIEW_MISSING_ONLY,
};
const show = (path = "/scan") =>
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/scan" element={<ScanBrowser />} />
        <Route path="/jobs/:id" element={<Destination />} />
      </Routes>
    </MemoryRouter>,
  );
beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  get.mockReturnValue(call({ location: source }));
  list.mockReturnValue(call({ locations: [source, Location.create({ id: 2n, name: "Imported", binding: OnlineBinding.UNCONFIRMED })], hasMore: false }));
  mediaList.mockReturnValue(
    call({
      media: [
        Media.create({ id: 3n, name: "Archive disk", kind: MediaKind.VOLUME, mounted: true, capabilities: { read: MediaAccess.RANDOM } }),
        Media.create({ id: 4n, name: "Archive tape", kind: MediaKind.TAPE, capabilities: { read: MediaAccess.SEQUENTIAL } }),
      ],
      hasMore: false,
    }),
  );
  scan.mockReturnValue(call({ job: { id: 43n } }));
});
describe("One Scan configuration", () => {
  it("uses the existing navigation without a duplicate creation heading or Locations shortcut", () => {
    show();
    expect(screen.queryByRole("heading", { name: /New scan/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Locations" })).not.toBeInTheDocument();
    expect(scan).not.toHaveBeenCalled();
  });
  it("resolves a Location shortcut without fetching catalog pages", async () => {
    get.mockReturnValue(call({ location: Location.create({ id: 80n, name: "Other location", binding: OnlineBinding.CONFIRMED }) }));
    show("/scan?location=80");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    expect(list).not.toHaveBeenCalled();
    expect(get).toHaveBeenCalledWith({ id: 80n, revision: 0n }, { abort: expect.any(AbortSignal) });
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ locationId: 80n, resultPolicy: Result.PUBLISH_ORIGINALS }) }));
  });
  it("submits Location and preview stages to the same Scan job with cache-only policy", async () => {
    show("/scan?location=1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("combobox", { name: "Signatures" }));
    await userEvent.click(screen.getByRole("option", { name: "Reuse known signatures only" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Generate missing" }));
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(await screen.findByText("Job 43")).toBeInTheDocument();
    expect(scan).toHaveBeenCalledWith({
      priority: 1n,
      spec: expect.objectContaining({
        locationId: 1n,
        signaturePolicy: Signature.KNOWN_ONLY,
        resultPolicy: Result.PUBLISH_ORIGINALS,
        previewPolicy: PreviewPolicy.PREVIEW_MISSING_ONLY,
      }),
    });
  });
  it("uses one mutually exclusive result policy and forces real reads for recorded-copy checks", async () => {
    show("/scan?media=3&result=verify");
    await screen.findByDisplayValue(/Archive disk/);
    expect(mediaList).toHaveBeenCalledWith({ param: { oneofKind: "mget", mget: { ids: [3n] } } }, { abort: expect.any(AbortSignal) });
    expect(screen.getByRole("combobox", { name: "Signatures" })).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith({
      priority: 1n,
      spec: expect.objectContaining({ mediaId: 3n, signaturePolicy: Signature.FORCE_READ, resultPolicy: Result.VERIFY_COPIES }),
    });
  });
  it("never offers Preview for sequential Tape and does not expose an Apply phase", async () => {
    show("/scan?media=4");
    await screen.findByDisplayValue(/Archive tape/);
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Not supported on Tape")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Apply/ })).not.toBeInTheDocument();
  });
  it("keeps imported paths unselectable and shows creation errors without navigating", async () => {
    scan.mockImplementation(() => ({ response: Promise.reject(new Error("Location is busy")) }));
    show("/scan?location=1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    expect(await screen.findByRole("option", { name: /Imported/ })).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("option", { name: /Photos/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(await screen.findByText("Location is busy")).toBeInTheDocument();
  });
  it("creates the explicit regenerate-all policy rather than an outdated-only option", async () => {
    show("/scan?location=1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Regenerate all" }));
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ previewPolicy: PreviewPolicy.PREVIEW_REGENERATE_ALL }) }));
  });
  it("requires a Media option and invalidates the old selection when its label is edited", async () => {
    show("/scan?media=3");
    const input = await screen.findByDisplayValue(/Archive disk/);
    await userEvent.clear(input);
    await userEvent.type(input, "Archive");
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
    await userEvent.click(await screen.findByRole("option", { name: /Archive tape/ }));
    expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(scan).toHaveBeenCalledWith(expect.objectContaining({ spec: expect.objectContaining({ mediaId: 4n, previewPolicy: PreviewPolicy.PREVIEW_NONE }) }));
  });
  it("does not turn a missing requested Media into a default selection", async () => {
    mediaList.mockReturnValue(call({ media: [], hasMore: false }));
    show("/scan?media=999");
    expect(await screen.findByText(/selected Media is no longer available/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
  });
  it("shows target validation progress without allowing stale submission", async () => {
    let resolve!: (reply: unknown) => void;
    get.mockReturnValue({
      response: new Promise((done) => {
        resolve = done;
      }),
    });
    show("/scan?location=1");
    expect(screen.getByRole("status")).toHaveTextContent("Loading source…");
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
    await act(async () => resolve({ location: source }));
    expect(screen.queryByText("Loading source…")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled();
  });
  it("places Signatures before Results and remembers only a successfully submitted configuration", async () => {
    const view = show("/scan?location=1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    expect(
      screen.getByRole("combobox", { name: "Signatures" }).compareDocumentPosition(screen.getByRole("combobox", { name: "Results" })) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    await userEvent.click(screen.getByRole("combobox", { name: "Signatures" }));
    await userEvent.click(screen.getByRole("option", { name: "Reuse known signatures only" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Results" }));
    await userEvent.click(screen.getByRole("option", { name: "View results only" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Find matching Library content" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Generate missing" }));
    expect(localStorage.getItem(scanPreferencesKey)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(await screen.findByText("Job 43")).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(scanPreferencesKey)!)).toEqual(storedOptions);
    view.unmount();
    show();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    expect(screen.getByRole("combobox", { name: "Location" })).toHaveValue("Photos · /photos");
    expect(screen.getByRole("combobox", { name: "Signatures" })).toHaveTextContent("Reuse known signatures only");
    expect(screen.getByRole("combobox", { name: "Results" })).toHaveTextContent("View results only");
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveTextContent("Generate missing");
    expect(screen.getByRole("checkbox", { name: "Find matching Library content" })).not.toBeChecked();
    expect(scan).toHaveBeenCalledTimes(1);
  });
  it("keeps the last successful options after a failed submission and locks controls while submitting", async () => {
    localStorage.setItem(scanPreferencesKey, JSON.stringify(storedOptions));
    let reject!: (error: Error) => void;
    scan.mockReturnValue({
      response: new Promise((_, fail) => {
        reject = fail;
      }),
    });
    show();
    await waitFor(() => expect(screen.getByRole("button", { name: "Start scan" })).toBeEnabled());
    await userEvent.click(screen.getByRole("combobox", { name: "Previews" }));
    await userEvent.click(screen.getByRole("option", { name: "Regenerate all" }));
    await userEvent.click(screen.getByRole("button", { name: "Start scan" }));
    expect(screen.getByRole("combobox", { name: "Location" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Source" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("combobox", { name: "Results" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveAttribute("aria-disabled", "true");
    await act(async () => reject(new Error("Busy")));
    expect(await screen.findByText("Busy")).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(scanPreferencesKey)!)).toEqual(storedOptions);
  });
  it.each([
    ["rebound", Location.create({ ...source, bindingToken: "replacement-binding" }), "last Location has changed"],
    ["imported", Location.create({ ...source, binding: OnlineBinding.UNCONFIRMED }), "needs confirmation"],
    ["removed", undefined, "unavailable"],
  ])("requires reselecting a %s remembered Location", async (_, location, message) => {
    localStorage.setItem(scanPreferencesKey, JSON.stringify(storedOptions));
    get.mockReturnValue(call({ location }));
    show();
    expect(await screen.findByText(new RegExp(message))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Location" })).toHaveValue("");
  });
  it("lets explicit source shortcuts override a remembered source without dropping content options", async () => {
    localStorage.setItem(scanPreferencesKey, JSON.stringify(storedOptions));
    show("/scan?media=4&result=verify");
    await screen.findByDisplayValue(/Archive tape/);
    expect(get).not.toHaveBeenCalled();
    expect(screen.getByRole("combobox", { name: "Results" })).toHaveTextContent("Check recorded copies");
    expect(screen.getByRole("combobox", { name: "Previews" })).toHaveTextContent("Don’t generate");
    expect(screen.getByRole("checkbox", { name: "Find matching Library content" })).not.toBeChecked();
  });
  it("ignores malformed preferences and never submits a typed target", async () => {
    localStorage.setItem(scanPreferencesKey, "{broken");
    show();
    await userEvent.type(screen.getByRole("combobox", { name: "Location" }), "Photos");
    expect(await screen.findByRole("option", { name: /Photos/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start scan" })).toBeDisabled();
    expect(scan).not.toHaveBeenCalled();
  });
});
