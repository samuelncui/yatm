import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LibrarySettings, Location, LocationReply, OnlineBinding } from "@/entity";

const { create, update, get, list, confirm, remove, listEntries, getJob, getAccess } = vi.hoisted(() => ({
  create: vi.fn(),
  update: vi.fn(),
  get: vi.fn(),
  list: vi.fn(),
  confirm: vi.fn(),
  remove: vi.fn(),
  listEntries: vi.fn(),
  getJob: vi.fn(),
  getAccess: vi.fn(),
}));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  locationCli: { create, update, get, list, confirm, delete: remove, listEntries },
  settingsCli: { getAccess, getLibrary: () => ({ response: Promise.resolve(LibrarySettings.create({ autoCollectFiles: true })) }) },
  jobCli: { get: getJob },
}));
import { LocationsBrowser } from "./online";
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 4n, name: "Photos", rootPath: "/photos", revision: 9n, binding: OnlineBinding.CONFIRMED });
const show = (path = "/settings/locations/4") => {
  const router = createMemoryRouter([{ path: "/settings/locations/*", element: <LocationsBrowser /> }], { initialEntries: [path] });
  return { ...render(<RouterProvider router={router} />), router };
};
beforeEach(() => {
  vi.clearAllMocks();
  list.mockReturnValue(call({ locations: [source], hasMore: false }));
  get.mockReturnValue(call(LocationReply.create({ location: source, accessibility: { accessible: true, checkedAtMs: 100n } })));
  create.mockReturnValue(call({ location: source }));
  update.mockReturnValue(call({ location: Location.create({ ...source, revision: 10n }) }));
  listEntries.mockReturnValue(call({ entries: [], hasMore: false, revision: 9n }));
  getAccess.mockReturnValue(call({ migrationMessages: [] }));
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
  it("reports configuration lookup failures instead of showing an empty migration summary as success", async () => {
    getAccess.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Configuration unavailable")) }));
    show("/settings/locations");
    expect(await screen.findByText(/Configuration details unavailable: Configuration unavailable/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText(/Configuration details unavailable/)).not.toBeInTheDocument());
  });
  it("uses a compact list and directly navigable inline configuration", async () => {
    const { router } = show("/settings/locations");
    await userEvent.click(await screen.findByRole("link", { name: "Photos" }));
    expect(router.state.location.pathname).toBe("/settings/locations/4");
    expect(await screen.findByRole("textbox", { name: "Name" })).toHaveValue("Photos");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Scan" })).toHaveAttribute("href", "/scan?location=4");
  });
  it("requires confirmation only for imported paths", async () => {
    get.mockReturnValue(call(LocationReply.create({ location: Location.create({ ...source, binding: OnlineBinding.CONFIRMED }) })));
    const rendered = show();
    expect(await screen.findByRole("link", { name: "Scan" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Confirm this path" })).not.toBeInTheDocument();
    rendered.unmount();
    get.mockReturnValue(call(LocationReply.create({ location: Location.create({ ...source, binding: OnlineBinding.UNCONFIRMED }) })));
    confirm.mockReturnValue(call({}));
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Confirm this path" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledWith({ id: 4n, revision: 9n }));
  });
  it("preserves Ignore text and marks a preferred restore destination", async () => {
    show("/settings/locations/new");
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Originals" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/source" } });
    fireEvent.change(screen.getByLabelText("Ignore"), { target: { value: "# comment\ndownloads/\n!keep*\n" } });
    await userEvent.click(screen.getByRole("checkbox", { name: "Preferred restore destination" }));
    await userEvent.click(screen.getByRole("button", { name: "Add location" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        location: expect.objectContaining({
          name: "Originals",
          rootPath: "/source",
          restoreTarget: true,
          ignore: { format: "gitignore", text: "# comment\ndownloads/\n!keep*\n" },
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
  it("shows an error and retry instead of browsing an unavailable Location's old index", async () => {
    get.mockReturnValue(
      call(LocationReply.create({ location: Location.create({ ...source, lastSyncAtMs: 100n }), accessibility: { accessible: false, checkedAtMs: 200n } })),
    );
    listEntries.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
    show("/settings/locations/4?tab=files");
    expect(await screen.findByText("Unavailable")).toBeInTheDocument();
    expect(await screen.findByText("Directory unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Directory unavailable")).not.toBeInTheDocument());
    expect(listEntries).toHaveBeenCalledWith(expect.objectContaining({ locationId: 4n, parentPath: "", cursor: "" }));
  });
});
