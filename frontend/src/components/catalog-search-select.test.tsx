import { useState } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Location, Media, MediaKind, OnlineBinding } from "@/entity";

const { mediaList, locations } = vi.hoisted(() => ({ mediaList: vi.fn(), locations: vi.fn() }));
vi.mock("@/api", () => ({ cli: { mediaList }, locationCli: { list: locations } }));
import { LocationSearchSelect, MediaSearchSelect } from "./catalog-search-select";

const media = (id: number, name = `Archive ${id}`) => Media.create({ id: BigInt(id), name, identity: `VOLUME-${id}`, kind: MediaKind.VOLUME });
const call = (value: unknown) => ({ response: Promise.resolve(value) });
const pending = () => {
  let resolve!: (value: unknown) => void;
  const response = new Promise((done) => {
    resolve = done;
  });
  return { response, resolve };
};
const MediaPicker = ({ initial = null, followingControl = false }: { initial?: Media | null; followingControl?: boolean }) => {
  const [value, setValue] = useState<Media | null>(initial);
  return (
    <>
      <MediaSearchSelect value={value} onChange={setValue} />
      <output aria-label="Selection">{value ? String(value.id) : "None"}</output>
      {followingControl && <button>Next field</button>}
    </>
  );
};
const LocationPicker = () => {
  const [value, setValue] = useState<Location | null>(null);
  return (
    <>
      <LocationSearchSelect value={value} onChange={setValue} />
      <output aria-label="Selection">{value ? String(value.id) : "None"}</output>
    </>
  );
};
beforeEach(() => {
  vi.clearAllMocks();
  mediaList.mockReturnValue(call({ media: [media(1)], hasMore: false }));
  locations.mockReturnValue(call({ locations: [], hasMore: false }));
});

describe("Server-backed catalog selection", () => {
  it("waits until open and debounces queries without filtering the server result locally", async () => {
    mediaList.mockReturnValue(call({ media: [media(12, "Server match")], hasMore: false }));
    render(<MediaPicker followingControl />);
    expect(mediaList).not.toHaveBeenCalled();
    const input = screen.getByRole("combobox", { name: "Media" });
    await userEvent.type(input, "VOLUME-12");
    expect(await screen.findByRole("option", { name: "Server match Volume · VOLUME-12" })).toBeInTheDocument();
    expect(mediaList).toHaveBeenCalledTimes(1);
    expect(mediaList).toHaveBeenCalledWith(
      { param: { oneofKind: "list", list: { query: "VOLUME-12", kinds: [MediaKind.VOLUME, MediaKind.TAPE], afterId: 0n, limit: 30n } } },
      { abort: expect.any(AbortSignal) },
    );
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("None");
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("12");
    await userEvent.type(input, " changed");
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("None");
  });

  it("replaces bounded pages and navigates with ID cursors, including by keyboard", async () => {
    mediaList.mockReturnValueOnce(call({ media: Array.from({ length: 30 }, (_, index) => media(index + 1)), hasMore: true }));
    mediaList.mockReturnValue(call({ media: [media(80)], hasMore: false }));
    render(<MediaPicker followingControl />);
    const input = screen.getByRole("combobox", { name: "Media" });
    await userEvent.click(input);
    expect(await screen.findAllByRole("option")).toHaveLength(30);
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(input).toHaveFocus();
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("option", { name: /Archive 80/ })).toBeInTheDocument();
    expect(input).toHaveFocus();
    expect(screen.queryByRole("option", { name: /^Archive 1 Volume/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("None");
    expect(mediaList).toHaveBeenLastCalledWith(
      expect.objectContaining({ param: expect.objectContaining({ list: expect.objectContaining({ afterId: 30n }) }) }),
      expect.anything(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    await waitFor(() =>
      expect(mediaList).toHaveBeenLastCalledWith(
        expect.objectContaining({ param: expect.objectContaining({ list: expect.objectContaining({ afterId: 0n }) }) }),
        expect.anything(),
      ),
    );
  });

  it("does not allow stale searches to replace the current query or reopen a closed popup", async () => {
    const old = pending();
    mediaList.mockReturnValueOnce(old);
    mediaList.mockReturnValue(call({ media: [media(2, "New result")], hasMore: false }));
    render(<MediaPicker />);
    const input = screen.getByRole("combobox", { name: "Media" });
    await userEvent.type(input, "old");
    await waitFor(() => expect(mediaList).toHaveBeenCalledTimes(1));
    const oldSignal = mediaList.mock.calls[0][1].abort as AbortSignal;
    await userEvent.clear(input);
    await userEvent.type(input, "new");
    expect(await screen.findByRole("option", { name: /New result/ })).toBeInTheDocument();
    expect(oldSignal.aborted).toBe(true);
    await act(async () => old.resolve({ media: [media(1, "Old result")], hasMore: false }));
    expect(screen.queryByRole("option", { name: /Old result/ })).not.toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("shows an empty state and allows retry after server errors", async () => {
    mediaList.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Search failed")) }));
    mediaList.mockReturnValue(call({ media: [], hasMore: false }));
    render(<MediaPicker followingControl />);
    const input = screen.getByRole("combobox", { name: "Media" });
    await userEvent.click(input);
    expect(await screen.findByRole("alert")).toHaveTextContent("Search failed");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Retry" })).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(input).toHaveFocus();
    await userEvent.tab();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByText("No matching Media")).toBeInTheDocument();
    expect(input).toHaveFocus();
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("None");
  });

  it("lets Tab leave the popup and Escape return from its controls to the input", async () => {
    mediaList.mockReturnValue(call({ media: [media(1)], hasMore: true }));
    render(<MediaPicker followingControl />);
    const input = screen.getByRole("combobox", { name: "Media" });
    await userEvent.click(input);
    await screen.findByRole("option");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next field" })).toHaveFocus();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    await userEvent.tab({ shift: true });
    expect(input).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    await screen.findByRole("option");
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Next" })).toHaveFocus();
    await userEvent.keyboard("{Escape}");
    expect(input).toHaveFocus();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  });

  it("shows an unnamed Media identity only once", async () => {
    mediaList.mockReturnValue(call({ media: [media(7, "")], hasMore: false }));
    render(<MediaPicker />);
    await userEvent.click(screen.getByRole("combobox", { name: "Media" }));
    const option = await screen.findByRole("option", { name: "VOLUME-7 Volume" });
    await userEvent.click(option);
    expect(screen.getByRole("combobox", { name: "Media" })).toHaveValue("VOLUME-7 · Volume");
  });

  it("cancels pending requests when closed or unmounted", async () => {
    const first = pending();
    const second = pending();
    mediaList.mockReturnValueOnce(first).mockReturnValueOnce(second);
    const view = render(<MediaPicker />);
    await userEvent.click(screen.getByRole("combobox", { name: "Media" }));
    await waitFor(() => expect(mediaList).toHaveBeenCalledTimes(1));
    await userEvent.keyboard("{Escape}");
    expect(mediaList.mock.calls[0][1].abort.aborted).toBe(true);
    await userEvent.click(screen.getByRole("combobox", { name: "Media" }));
    await waitFor(() => expect(mediaList).toHaveBeenCalledTimes(2));
    view.unmount();
    expect(mediaList.mock.calls[1][1].abort.aborted).toBe(true);
    await act(async () => {
      first.resolve({ media: [media(1)], hasMore: false });
      second.resolve({ media: [media(2)], hasMore: false });
    });
  });
  it("can clear a selected value without losing the pending first-page response", async () => {
    const first = pending();
    mediaList.mockReturnValue(first);
    render(<MediaPicker initial={media(8)} />);
    await userEvent.click(screen.getByRole("combobox", { name: "Media" }));
    await waitFor(() => expect(mediaList).toHaveBeenCalledTimes(1));
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    await act(async () => first.resolve({ media: [media(9)], hasMore: false }));
    expect(await screen.findByRole("option", { name: /Archive 9/ })).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("None");
  });

  it("uses the same paged search for Location paths and disables imported roots", async () => {
    locations.mockReturnValue(
      call({
        locations: [
          Location.create({ id: 4n, name: "Photos", rootPath: "/data/photos", binding: OnlineBinding.CONFIRMED }),
          Location.create({ id: 5n, name: "Imported", rootPath: "/data/imported", binding: OnlineBinding.UNCONFIRMED }),
        ],
        hasMore: false,
      }),
    );
    render(<LocationPicker />);
    await userEvent.type(screen.getByRole("combobox", { name: "Location" }), "/data");
    const unavailable = await screen.findByRole("option", { name: /Imported/ });
    expect(unavailable).toHaveAttribute("aria-disabled", "true");
    expect(locations).toHaveBeenCalledWith({ query: "/data", afterId: 0n, limit: 30 }, { abort: expect.any(AbortSignal) });
    await userEvent.click(screen.getByRole("option", { name: /Photos/ }));
    expect(screen.getByRole("status", { name: "Selection" })).toHaveTextContent("4");
  });
});
