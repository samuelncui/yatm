import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileScope, Location, GetLocationResponse } from "@/entity";
import { contentTime } from "@/components/content-status";

const { create, update, get, list, remove, listEntries, getJob } = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  get: vi.fn(),
  list: vi.fn(),
  remove: vi.fn(),
  listEntries: vi.fn(),
  getJob: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  locationCli: { create, update, get, list, delete: remove, listEntries },
  filesCli: {
    list: ({ directory, cursor }: any) =>
      streamListing(
        listEntries({ locationId: directory.target.location.locationId, parentPath: directory.target.location.path, cursor }).response.then((reply: any) => ({
          entries: [],
          breadcrumbs: [],
          nextCursor: reply.nextCursor ?? "",
          scope: FileScope.ALL,
        })),
      ),
  },
  jobCli: { get: getJob },
}));
import { LocationsBrowser } from "./locations";
import { streamListing } from "@/test/files-fixture";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 4n, name: "Photos", rootPath: "/photos", revision: 9n });
const show = (path = "/settings/locations/4") => {
  const router = createMemoryRouter([{ path: "/settings/locations/*", element: <LocationsBrowser /> }], { initialEntries: [path] });
  return { ...render(<RouterProvider router={router} />), router };
};
beforeEach(() => {
  vi.clearAllMocks();
  list.mockReturnValue(call({ locations: [source], hasMore: false }));
  get.mockReturnValue(call(GetLocationResponse.create({ location: source, accessibility: { accessible: true, checkedAtNs: 1700000000123456789n } })));
  create.mockReturnValue(call({ location: source }));
  update.mockReturnValue(call({ location: Location.create({ ...source, revision: 10n }) }));
  listEntries.mockReturnValue(call({ entries: [], hasMore: false, revision: 9n }));
});
describe("Routed Location settings", () => {
  it("distinguishes failed detail requests from inaccessible directories", async () => {
    list.mockReturnValue(call({ locations: [Location.create({ ...source, lastJobId: 42n })], hasMore: false }));
    get.mockImplementation(() => ({ response: Promise.reject(new Error("Access service unavailable")) }));
    getJob.mockImplementation(() => ({ response: Promise.reject(new Error("Job service unavailable")) }));
    show("/settings/locations");
    expect(await screen.findByText("Check failed")).toBeInTheDocument();
    expect(screen.getByText("Check failed")).toHaveAttribute("title", "Access service unavailable");
    expect(screen.getByText("Details unavailable")).toHaveAttribute("title", "Job service unavailable");
    expect(screen.queryByText("Unavailable", { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText("Not checked")).not.toBeInTheDocument();
  });
  it("retains a confirmed access observation when refresh fails and identifies it as cached", async () => {
    show("/settings/locations");
    await screen.findByText("Available");
    get.mockImplementation(() => ({ response: Promise.reject(new Error("Connection lost")) }));
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText("Last check · Refresh failed")).toBeInTheDocument();
    expect(screen.getByText("Available")).toBeInTheDocument();
    expect(screen.queryByText("Not checked")).not.toBeInTheDocument();
  });
  it("shows current Locations without fetching historical migration messages", async () => {
    show("/settings/locations");
    expect(await screen.findByRole("link", { name: "Photos" })).toBeInTheDocument();
    expect(await screen.findByText("Available")).toHaveAttribute("title", contentTime(1700000000123456789n));
    expect(screen.queryByText("Configuration migration")).not.toBeInTheDocument();
    // The removed setup-label column no longer occupies the table.
    expect(screen.queryByRole("columnheader", { name: "Path" })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Find identical files" })).not.toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Locations" });
    const add = screen.getByRole("link", { name: "Add location" });
    const refresh = screen.getByRole("button", { name: "Refresh" });
    expect(table.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(add.compareDocumentPosition(refresh) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });
  it("uses a compact list and directly navigable inline configuration", async () => {
    const { router } = show("/settings/locations");
    await userEvent.click(await screen.findByRole("link", { name: "Photos" }));
    expect(router.state.location.pathname).toBe("/settings/locations/4");
    expect(await screen.findByRole("textbox", { name: "Name" })).toHaveValue("Photos");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    // Navigation states where the operator is and the Directory field owns the root path.
    expect(screen.queryByRole("link", { name: "Locations" })).not.toBeInTheDocument();
    expect(screen.getAllByDisplayValue("/photos")).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "Find identical files" })).not.toBeInTheDocument();
    const actions = screen.getByRole("button", { name: "Save changes" }).parentElement;
    expect(actions).not.toBeNull();
    expect(Array.from(actions!.children, (action) => action.textContent?.trim())).toEqual(["Save changes", "Scan", "Cancel", "Remove location"]);
    expect(actions).toContainElement(screen.getByRole("link", { name: "Scan" }));
    expect(actions).toContainElement(screen.getByRole("button", { name: "Remove location" }));
  });
  it("treats a registered location as directly usable, with no confirmation flow", async () => {
    const rendered = show();
    expect(await screen.findByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    expect(screen.queryByRole("button", { name: "Confirm this path" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Confirm its path/)).not.toBeInTheDocument();
    rendered.unmount();

    // A registration that was imported behaves identically: no confirmation affordance exists.
    get.mockReturnValue(call(GetLocationResponse.create({ location: Location.create({ ...source, revision: 12n }) })));
    show();
    expect(await screen.findByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
    expect(screen.queryByRole("button", { name: "Confirm this path" })).not.toBeInTheDocument();
  });
  it("preserves Ignore text and marks a preferred restore destination", async () => {
    show("/settings/locations/new");
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Originals" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/source" } });
    fireEvent.change(screen.getByLabelText("Ignore"), { target: { value: "# comment\ndownloads/\n!keep*\n" } });
    await userEvent.click(screen.getByRole("checkbox", { name: "Preferred restore destination" }));
    expect(screen.getByRole("button", { name: "Advanced" })).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Use mmap for content reads" }));
    await userEvent.click(screen.getByRole("button", { name: "Add location" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        location: expect.objectContaining({
          name: "Originals",
          rootPath: "/source",
          restoreTarget: true,
          config: { ignore: { format: "gitignore", text: "# comment\ndownloads/\n!keep*\n" }, useMmap: true },
        }),
      }),
    );
  });
  it("protects unsaved edits when switching tabs and disables removal", async () => {
    const { router } = show();
    fireEvent.change(await screen.findByRole("textbox", { name: "Name" }), { target: { value: "New name" } });
    expect(screen.getByRole("button", { name: "Remove location" })).toBeDisabled();
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    expect(await screen.findByRole("dialog", { name: "Discard unsaved changes?" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(await screen.findByRole("textbox", { name: "Name" })).toHaveValue("New name");
    expect(router.state.location.search).toBe("");
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    await userEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect(router.state.location.search).toBe("?tab=files");
    expect(update).not.toHaveBeenCalled();
  });
  it("keeps a failed removal in the shared confirmation for retry", async () => {
    const { router } = show();
    await userEvent.click(await screen.findByRole("button", { name: "Remove location" }));
    const dialog = screen.getByRole("dialog", { name: "Remove Photos?" });
    expect(dialog).toHaveTextContent("Disk files, Library organization, saved versions and archive copies are kept.");
    remove.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Location could not be removed")) }));
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("Location could not be removed")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/settings/locations/4");
    remove.mockReturnValueOnce(call({}));
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/settings/locations"));
    expect(remove).toHaveBeenCalledTimes(2);
  });
  it("shows an error and retry instead of browsing an unavailable Location's old index", async () => {
    get.mockReturnValue(
      call(
        GetLocationResponse.create({
          location: Location.create({ ...source, lastSyncAtNs: 100000000n }),
          accessibility: { accessible: false, checkedAtNs: 200000000n },
        }),
      ),
    );
    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
    show("/settings/locations/4?tab=files");
    expect(await screen.findByText("Unavailable")).toBeInTheDocument();
    expect(await screen.findByText("Directory unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Directory unavailable")).not.toBeInTheDocument());
    expect(listEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "", cursor: undefined }));
  });
});
