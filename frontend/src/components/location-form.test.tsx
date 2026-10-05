import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { BrowsePathsResponse, Location } from "@/entity";
import { LocationForm } from "./location-form";

const { browsePaths, create, update } = vi.hoisted(() => ({ browsePaths: vi.fn(), create: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { browsePaths, create, update } }));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const source = Location.create({ id: 4n, name: "Photos", rootPath: "/photos" });
const show = (location?: Location) => {
  const router = createMemoryRouter([{ path: "/", element: <LocationForm source={location} onSaved={vi.fn()} onCancel={vi.fn()} /> }]);
  return render(<RouterProvider router={router} />);
};
beforeEach(() => {
  vi.resetAllMocks();
  create.mockReturnValue(call({ location: source }));
  update.mockReturnValue(call({ location: source }));
});

it.each([
  ["new", undefined],
  ["existing", source],
] as const)("chooses a server path into the %s draft and writes only on explicit Save", async (_, location) => {
  const exactPath = "/authorized//folder ";
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ path: location?.rootPath ?? "", directories: [{ name: "Folder", path: exactPath }] })));
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ path: exactPath })));
  show(location);
  if (!location) fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "New location" } });
  await userEvent.click(screen.getByRole("button", { name: "Browse directories" }));
  await userEvent.click(await screen.findByRole("button", { name: "Folder" }));
  await screen.findByText("No directories.");
  await userEvent.click(screen.getByRole("button", { name: "Choose directory" }));
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: "Directory" })).toHaveValue(exactPath);
  expect(create).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: location ? "Save changes" : "Add location" }));
  const write = location ? update : create;
  await waitFor(() => expect(write).toHaveBeenCalledWith({ location: expect.objectContaining({ rootPath: exactPath }) }));
  expect(location ? create : update).not.toHaveBeenCalled();
});

it("keeps the edited draft on Cancel and ignores a cancelled read after reopening", async () => {
  let fail!: (error: Error) => void;
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ path: "/draft", directories: [{ name: "Child", path: "/draft/child" }] })));
  browsePaths.mockReturnValueOnce({
    response: new Promise<BrowsePathsResponse>((_, reject) => {
      fail = reject;
    }),
  });
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ path: "/draft", directories: [{ name: "Fresh child", path: "/draft/fresh" }] })));
  show(source);
  fireEvent.change(screen.getByRole("textbox", { name: "Directory" }), { target: { value: "/draft" } });
  await userEvent.click(screen.getByRole("button", { name: "Browse directories" }));
  await userEvent.click(await screen.findByRole("button", { name: "Child" }));
  const signal = browsePaths.mock.calls[1][1].abort as AbortSignal;
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("textbox", { name: "Directory" })).toHaveValue("/draft");
  await userEvent.click(screen.getByRole("button", { name: "Browse directories" }));
  await screen.findByRole("button", { name: "Fresh child" });
  await act(async () => fail(new Error("Cancelled read failed")));
  expect(signal.aborted).toBe(true);
  expect(screen.queryByText("Cancelled read failed")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Fresh child" })).toBeInTheDocument();
  expect(browsePaths).toHaveBeenLastCalledWith({ path: "/draft", cursor: "", limit: 100 }, { abort: expect.any(AbortSignal) });
  expect(create).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
});

it("disables Browse directories while saving", async () => {
  let finish!: (value: { location: Location }) => void;
  update.mockReturnValueOnce({
    response: new Promise<{ location: Location }>((resolve) => {
      finish = resolve;
    }),
  });
  show(source);
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Renamed" } });
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  expect(screen.getByRole("button", { name: "Browse directories" })).toBeDisabled();
  expect(browsePaths).not.toHaveBeenCalled();
  await act(async () => finish({ location: source }));
  expect(screen.getByRole("button", { name: "Browse directories" })).toBeEnabled();
});

it.each(["{Enter}", "{Escape}"])("keeps unsaved edits with %s and proceeds only on Discard", async (key) => {
  const router = createMemoryRouter(
    [
      { path: "/", element: <LocationForm source={source} onSaved={vi.fn()} onCancel={vi.fn()} /> },
      { path: "/other", element: <p>Other page</p> },
    ],
    { initialEntries: ["/other", "/"], initialIndex: 1 },
  );
  render(<RouterProvider router={router} />);
  fireEvent.change(screen.getByRole("textbox", { name: "Name" }), { target: { value: "Edited name" } });
  await act(() => router.navigate(-1));
  const dialog = screen.getByRole("dialog", { name: "Discard unsaved changes?" });
  await waitFor(() => expect(within(dialog).getByRole("button", { name: "Keep editing" })).toHaveFocus());
  await userEvent.keyboard(key);
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(router.state.location.pathname).toBe("/");
  expect(screen.getByRole("textbox", { name: "Name" })).toHaveValue("Edited name");

  await act(() => router.navigate(-1));
  await userEvent.click(screen.getByRole("button", { name: "Discard" }));
  await waitFor(() => expect(router.state.location.pathname).toBe("/other"));
  expect(create).not.toHaveBeenCalled();
  expect(update).not.toHaveBeenCalled();
});

it("preserves Advanced draft values across keyboard expansion and saves them while collapsed", async () => {
  show(Location.create({ ...source, config: { useMmap: false } }));
  const advanced = screen.getByRole("button", { name: "Advanced" });
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  advanced.focus();
  await userEvent.keyboard("{Enter}");
  expect(screen.getByRole("checkbox", { name: "Use mmap for content reads" })).not.toBeChecked();
  await userEvent.click(screen.getByRole("checkbox", { name: "Use mmap for content reads" }));
  advanced.focus();
  await userEvent.keyboard(" ");
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  await userEvent.keyboard("{Enter}");
  expect(screen.getByRole("checkbox", { name: "Use mmap for content reads" })).toBeChecked();
  await userEvent.keyboard(" ");
  expect(advanced).toHaveAttribute("aria-expanded", "false");
  expect(update).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() =>
    expect(update).toHaveBeenCalledWith({
      location: expect.objectContaining({ config: { ignore: { format: "gitignore", text: "" }, useMmap: true } }),
    }),
  );
});
