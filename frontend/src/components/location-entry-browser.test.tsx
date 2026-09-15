import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { Location, LocationEntryRef } from "@/entity";

const { listEntries } = vi.hoisted(() => ({ listEntries: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  locationCli: { listEntries },
}));
import { LocationEntryBrowser } from "./location-entry-browser";

// Keep Chonky's real click handling; jsdom has no virtual viewport geometry.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </>
  ),
}));

afterEach(() => vi.restoreAllMocks());

it.each([false, true])("uses double-click for details, or explicit picker selection (picker %s)", async (picker) => {
  const open = vi.spyOn(window, "open").mockImplementation(() => null);
  const choose = vi.fn();
  const reference = LocationEntryRef.create({ locationId: 4n, path: "report.txt", bindingToken: "bound", facts: { mode: 420, size: 42n } });
  listEntries.mockReturnValue({ response: Promise.resolve({ entries: [{ path: reference.path, reference }], hasMore: false }) });
  render(<LocationEntryBrowser source={Location.create({ id: 4n, name: "Documents" })} onChoose={picker ? choose : undefined} />, { wrapper: MemoryRouter });
  await userEvent.dblClick((await screen.findAllByTitle("report.txt"))[0]);
  if (picker) {
    await waitFor(() => expect(choose).toHaveBeenCalledWith(reference));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  } else {
    expect(await screen.findByRole("dialog", { name: "File properties" })).toHaveTextContent("report.txt");
    expect(choose).not.toHaveBeenCalled();
  }
  expect(open).not.toHaveBeenCalled();
});
