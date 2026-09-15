import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { LibraryFilters, buildLibraryQuery } from "./library-filters";
import { Location } from "@/entity";
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
  it("composes filters into the visible query", async () => {
    const onSearch = vi.fn();
    render(<LibraryFilters initialQuery="name:*.jpg OR tag:travel" onSearch={onSearch} onClear={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Note" }), "review");
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    await userEvent.click(await screen.findByRole("option", { name: "Photos" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Duplicates in Locations" }));
    await userEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    expect(onSearch).toHaveBeenCalledWith('(name:*.jpg OR tag:travel) AND location:9 AND has:duplicates AND note:"review"', true);
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
  it("ignores an old response after closing and reopening the dialog", async () => {
    let resolve!: (value: { locations: Location[]; hasMore: boolean }) => void;
    list.mockReturnValueOnce({ response: new Promise((done) => (resolve = done)) });
    render(<LibraryFilters onSearch={vi.fn()} onClear={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "More filters" }));
    resolve({ locations: [Location.create({ id: 1n, name: "Stale directory" })], hasMore: false });
    await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
    expect(await screen.findByRole("option", { name: "Photos" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Stale directory" })).not.toBeInTheDocument();
  });
  it("retains incoming expressions and boolean grouping", () => {
    render(<LibraryFilters initialQuery="location:99" onSearch={vi.fn()} onClear={vi.fn()} />);
    expect(screen.getByRole("textbox", { name: "Search query" })).toHaveValue("location:99");
    expect(buildLibraryQuery("tag:a OR tag:b", true, "9", "needed")).toBe(
      "(tag:a OR tag:b) AND location:9 AND (has:online AND NOT has:unknown AND NOT has:archive)",
    );
  });
});
