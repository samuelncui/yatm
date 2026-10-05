import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { BrowsePathsResponse } from "@/entity";
import { LocationPathPicker } from "./location-path-picker";

const { browsePaths } = vi.hoisted(() => ({ browsePaths: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { browsePaths } }));
const call = (value: BrowsePathsResponse) => ({ response: Promise.resolve(value) });
const page = (path: string, names: string[], nextCursor = "") =>
  BrowsePathsResponse.create({ path, directories: names.map((name) => ({ name, path: `${path}/${name}` })), nextCursor });
beforeEach(() => vi.resetAllMocks());

it("retries a failed initial path and permits choosing an empty directory returned by the server", async () => {
  browsePaths.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Directory unavailable")) }));
  browsePaths.mockReturnValueOnce(call(page("/authorized/empty ", [])));
  const onChoose = vi.fn();
  render(<LocationPathPicker initialPath="/requested" onChoose={onChoose} onClose={vi.fn()} />);
  const error = await screen.findByRole("alert");
  expect(error).toHaveTextContent("Directory unavailable");
  expect(screen.getByRole("button", { name: "Choose directory" })).toBeDisabled();
  await userEvent.click(within(error).getByRole("button", { name: "Retry" }));
  await screen.findByText("No directories.");
  await userEvent.click(screen.getByRole("button", { name: "Choose directory" }));
  expect(onChoose).toHaveBeenCalledWith("/authorized/empty ");
  expect(browsePaths).toHaveBeenLastCalledWith({ path: "/requested", cursor: "", limit: 100 }, { abort: expect.any(AbortSignal) });
});

it("discards a late directory response after navigating back to allowed roots", async () => {
  const exactPath = "/allowed//photos ";
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ directories: [{ name: "Photos", path: exactPath }] })));
  let finish!: (value: BrowsePathsResponse) => void;
  browsePaths.mockReturnValueOnce({
    response: new Promise<BrowsePathsResponse>((resolve) => {
      finish = resolve;
    }),
  });
  browsePaths.mockReturnValueOnce(call(BrowsePathsResponse.create({ directories: [{ name: "Other root", path: "/other" }] })));
  render(<LocationPathPicker initialPath="" onChoose={vi.fn()} onClose={vi.fn()} />);
  await userEvent.click(await screen.findByRole("button", { name: "Photos" }));
  expect(browsePaths).toHaveBeenLastCalledWith({ path: exactPath, cursor: "", limit: 100 }, { abort: expect.any(AbortSignal) });
  const signal = browsePaths.mock.calls[1][1].abort as AbortSignal;
  expect(screen.queryByRole("button", { name: "Photos" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Allowed directories" }));
  await screen.findByRole("button", { name: "Other root" });
  await act(async () => finish(page(exactPath, ["Stale child"])));
  expect(signal.aborted).toBe(true);
  expect(screen.queryByRole("button", { name: "Stale child" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Other root" })).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose directory" })).toBeDisabled();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
});

it("serializes cursor pages, retains their order and retries only the failed continuation", async () => {
  browsePaths.mockReturnValueOnce(call(page("/data", ["First"], "cursor-one")));
  let finish!: (value: BrowsePathsResponse) => void;
  browsePaths.mockReturnValueOnce({
    response: new Promise<BrowsePathsResponse>((resolve) => {
      finish = resolve;
    }),
  });
  browsePaths.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Connection lost")) }));
  browsePaths.mockReturnValueOnce(call(page("/data", ["Third"])));
  render(<LocationPathPicker initialPath="/requested-data" onChoose={vi.fn()} onClose={vi.fn()} />);
  const more = await screen.findByRole("button", { name: "Load more directories" });
  fireEvent.click(more);
  fireEvent.click(more);
  expect(more).toBeDisabled();
  expect(browsePaths).toHaveBeenCalledTimes(2);
  await act(async () => finish(page("/data", ["Second"], "cursor-two")));
  await userEvent.click(screen.getByRole("button", { name: "Load more directories" }));
  const error = await screen.findByRole("alert");
  expect(error).toHaveTextContent("Connection lost");
  expect(
    within(screen.getByRole("list", { name: "Directories" }))
      .getAllByRole("button")
      .map((row) => row.textContent),
  ).toEqual(["First", "Second"]);
  await userEvent.click(within(error).getByRole("button", { name: "Retry" }));
  await screen.findByRole("button", { name: "Third" });
  expect(
    within(screen.getByRole("list", { name: "Directories" }))
      .getAllByRole("button")
      .map((row) => row.textContent),
  ).toEqual(["First", "Second", "Third"]);
  expect(browsePaths.mock.calls.map(([input]) => input)).toEqual([
    { path: "/requested-data", cursor: "", limit: 100 },
    { path: "/data", cursor: "cursor-one", limit: 100 },
    { path: "/data", cursor: "cursor-two", limit: 100 },
    { path: "/data", cursor: "cursor-two", limit: 100 },
  ]);
});
