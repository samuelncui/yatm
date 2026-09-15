import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { DirectoryPicker } from "./directory-picker";

const { browsePaths } = vi.hoisted(() => ({ browsePaths: vi.fn() }));
vi.mock("@/api", () => ({ settingsCli: { browsePaths } }));
beforeEach(() => browsePaths.mockReset());

it.each(["", "photos"])("does not start a loading state when clicking the current breadcrumb at %j", async (initialPath) => {
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [{ name: "child", path: "child" }], nextCursor: "" }) });
  render(<DirectoryPicker locationID={1n} initialPath={initialPath} rootName="Restored files" onChoose={vi.fn()} onClose={vi.fn()} />);
  await screen.findByRole("button", { name: "child" });
  await userEvent.click(screen.getByRole("button", { name: initialPath || "Restored files" }));
  expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "child" })).toBeInTheDocument();
  expect(browsePaths).toHaveBeenCalledOnce();
});

it("reloads from the first page after a paged directory read fails", async () => {
  browsePaths.mockReturnValueOnce({ response: Promise.resolve({ directories: [{ name: "first", path: "first" }], nextCursor: "next" }) });
  browsePaths.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory changed")) }));
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [{ name: "first", path: "first" }], nextCursor: "next" }) });
  render(<DirectoryPicker locationID={1n} onChoose={vi.fn()} onClose={vi.fn()} />);
  await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
  await userEvent.click(await screen.findByRole("button", { name: "Retry" }));
  await screen.findByRole("button", { name: "first" });
  expect(browsePaths).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "" }));
});

it("does not expose the previous directory's rows under a failed child navigation", async () => {
  browsePaths.mockReturnValueOnce({ response: Promise.resolve({ directories: [{ name: "photos", path: "photos" }], nextCursor: "" }) });
  browsePaths.mockImplementation(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
  render(<DirectoryPicker locationID={1n} onChoose={vi.fn()} onClose={vi.fn()} />);
  await userEvent.dblClick(await screen.findByRole("button", { name: "photos" }));
  await screen.findByText("Directory unavailable");
  expect(within(screen.getByRole("list", { name: "Directories" })).queryByRole("button", { name: "photos" })).not.toBeInTheDocument();
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [], nextCursor: "" }) });
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.queryByText("Directory unavailable")).not.toBeInTheDocument());
  expect(browsePaths).toHaveBeenLastCalledWith(expect.objectContaining({ locationId: 1n, path: "photos", cursor: "" }));
});

it("ignores an old destination response after switching locations", async () => {
  let resolveOld!: (value: { directories: { path: string; name: string }[]; nextCursor: string }) => void;
  browsePaths.mockReturnValueOnce({
    response: new Promise((resolve) => {
      resolveOld = resolve;
    }),
  });
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [{ path: "current", name: "current" }], nextCursor: "" }) });
  const props = { onChoose: vi.fn(), onClose: vi.fn() };
  const { rerender } = render(<DirectoryPicker locationID={1n} {...props} />);
  rerender(<DirectoryPicker locationID={2n} {...props} />);
  await screen.findByRole("button", { name: "current" });
  await act(async () => {
    resolveOld({ directories: [{ path: "stale", name: "stale" }], nextCursor: "old-page" });
  });
  expect(screen.queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
});

it("resets the physical path when switching Restore destination while the picker is open", async () => {
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [], nextCursor: "" }) });
  const onChoose = vi.fn();
  const onClose = vi.fn();
  const { rerender } = render(<DirectoryPicker locationID={1n} initialPath="photos" onChoose={onChoose} onClose={onClose} />);
  await waitFor(() => expect(browsePaths).toHaveBeenCalledWith(expect.objectContaining({ locationId: 1n, path: "photos" })));
  rerender(<DirectoryPicker locationID={2n} initialPath="" onChoose={onChoose} onClose={onClose} />);
  await waitFor(() => expect(browsePaths).toHaveBeenLastCalledWith(expect.objectContaining({ locationId: 2n, path: "" })));
});

it("chooses the current directory and delegates explicit folder creation", async () => {
  browsePaths.mockReturnValue({ response: Promise.resolve({ directories: [], nextCursor: "" }) });
  const onChoose = vi.fn();
  const onNewDirectory = vi.fn();
  render(<DirectoryPicker locationID={1n} initialPath="photos" onNewDirectory={onNewDirectory} onChoose={onChoose} onClose={vi.fn()} />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Choose" })).toBeEnabled());
  await userEvent.click(screen.getByRole("button", { name: "New folder" }));
  expect(onNewDirectory).toHaveBeenCalledWith("photos");
  await userEvent.click(screen.getByRole("button", { name: "Choose" }));
  expect(onChoose).toHaveBeenCalledWith("photos");
  expect(browsePaths).toHaveBeenLastCalledWith(expect.objectContaining({ path: "photos" }));
});
