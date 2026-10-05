import { MemoryRouter } from "react-router";
import type { ReactNode } from "react";
import { render as testingRender, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LibraryFilters, buildLibraryQuery } from "./library-filters";
import { Location } from "@/entity";
const render = (ui: ReactNode) => testingRender(ui, { wrapper: MemoryRouter });
const { list } = vi.hoisted(() => ({ list: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list } }));
beforeEach(() => {
  list.mockReset();
  list.mockReturnValue({ response: Promise.resolve({ locations: [Location.create({ id: 9n, name: "Photos" })], hasMore: false }) });
});
describe("Files query bar", () => {
  it("keeps one query and stable controls for both sources", async () => {
    const onSearch = vi.fn(),
      onClear = vi.fn();
    const view = render(<LibraryFilters onSearch={onSearch} onClear={onClear} />);
    expect(screen.getAllByRole("button").map((button) => button.textContent || button.getAttribute("aria-label"))).toEqual([
      "Clear search",
      "More filters",
      "Search",
      "Find identical files",
    ]);
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(list).not.toHaveBeenCalled();
    await userEvent.type(screen.getByRole("textbox", { name: "Search query" }), "tag:travel");
    view.rerender(<LibraryFilters onSearch={onSearch} onClear={onClear} scopeLabel="Photos" locationScope={{ id: "7", name: "Photos" }} />);
    expect(screen.getByRole("textbox", { name: "Search query" })).toHaveValue("tag:travel");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(onSearch).toHaveBeenCalledWith("tag:travel", false);
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(onClear).toHaveBeenCalledOnce();
  });
  it("keeps Clear available when the draft is empty but results are active", async () => {
    const onSearch = vi.fn();
    const onClear = vi.fn();
    const view = render(<LibraryFilters onSearch={onSearch} onClear={onClear} />);
    const input = screen.getByRole("textbox", { name: "Search query" });
    const clear = screen.getByRole("button", { name: "Clear search" });
    expect(clear).toBeDisabled();

    await userEvent.type(input, "tag:travel{Enter}");
    expect(onSearch).toHaveBeenCalledWith("tag:travel", false);
    view.rerender(<LibraryFilters onSearch={onSearch} onClear={onClear} searchActive />);
    await userEvent.clear(input);
    expect(clear).toBeEnabled();
    await userEvent.click(clear);
    expect(onClear).toHaveBeenCalledOnce();
  });
  it("composes filters into the visible query", async () => {
    const onSearch = vi.fn();
    render(<LibraryFilters initialQuery="name:*.jpg OR tag:travel" onSearch={onSearch} onClear={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Note" }), "review");
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    await userEvent.click(await screen.findByRole("option", { name: "Photos" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Duplicates in Locations" }));
    await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(onSearch).toHaveBeenCalledWith('(name:*.jpg OR tag:travel) AND location:9 AND has:duplicates AND note:"review"', false);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Search query" })).toHaveValue(onSearch.mock.lastCall?.[0]);
  });
  it("keeps source scope explicit for ordinary and duplicate filters", async () => {
    render(<LibraryFilters onSearch={vi.fn()} onClear={vi.fn()} locationScope={{ id: "7", name: "Photos" }} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    expect(screen.getByRole("combobox", { name: "Location" })).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("checkbox", { name: "Duplicates in Locations" }));
    expect(screen.getByRole("combobox", { name: "Location" })).not.toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    expect(await screen.findByRole("option", { name: "Photos" })).toBeInTheDocument();
  });
  it("does not retain a different Location filter after leaving duplicate mode", async () => {
    const onSearch = vi.fn();
    render(<LibraryFilters onSearch={onSearch} onClear={vi.fn()} locationScope={{ id: "7", name: "Documents" }} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Duplicates in Locations" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    await userEvent.click(await screen.findByRole("option", { name: "Photos" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Duplicates in Locations" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Tag" }), "travel");
    await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(onSearch).toHaveBeenCalledWith('tag:"travel"', false);
  });
  it("cancels a closed dialog's read and ignores its response after reopening", async () => {
    let resolve!: (value: { locations: Location[]; hasMore: boolean }) => void;
    list.mockReturnValueOnce({ response: new Promise((done) => (resolve = done)) });
    render(<LibraryFilters onSearch={vi.fn()} onClear={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    const signal = list.mock.calls[0][1]?.abort as AbortSignal | undefined;
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(signal?.aborted).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    resolve({ locations: [Location.create({ id: 1n, name: "Stale directory" })], hasMore: false });
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    expect(await screen.findByRole("option", { name: "Photos" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Stale directory" })).not.toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(2);
  });
  it("retries the next Location page and retains its selected draft after reopening", async () => {
    const onSearch = vi.fn();
    list
      .mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 9n, name: "Photos" })], hasMore: true }) })
      .mockImplementationOnce(() => ({ response: Promise.reject(new Error("Next page unavailable")) }))
      .mockReturnValueOnce({ response: Promise.resolve({ locations: [Location.create({ id: 12n, name: "Documents" })], hasMore: false }) });
    render(<LibraryFilters onSearch={onSearch} onClear={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await userEvent.click(await screen.findByRole("button", { name: "More locations" }));
    expect(await screen.findByText("Next page unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    expect(screen.getByRole("option", { name: "Photos" })).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("option", { name: "Documents" }));
    expect(list.mock.calls.map(([request]) => request)).toEqual([
      { afterId: 0n, limit: 50, query: "" },
      { afterId: 9n, limit: 50, query: "" },
      { afterId: 9n, limit: 50, query: "" },
    ]);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(4));
    expect(screen.getByRole("combobox", { name: "Location" })).toHaveTextContent("Documents");
    await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(onSearch).toHaveBeenCalledWith("location:12", false);
  });
  it("retains incoming expressions and boolean grouping", () => {
    render(<LibraryFilters initialQuery="location:99" onSearch={vi.fn()} onClear={vi.fn()} />);
    expect(screen.getByRole("textbox", { name: "Search query" })).toHaveValue("location:99");
    expect(buildLibraryQuery("tag:a OR tag:b", true, "9", "needed")).toBe(
      "(tag:a OR tag:b) AND location:9 AND (has:original AND NOT has:unknown AND NOT has:archive)",
    );
  });
});
