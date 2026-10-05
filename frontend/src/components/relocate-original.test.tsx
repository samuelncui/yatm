import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { File, Location, LocationEntryRef } from "@/entity";
import { RelocateOriginalDialog } from "./relocate-original";

const { list, relocateOriginal, report } = vi.hoisted(() => ({ list: vi.fn(), relocateOriginal: vi.fn(), report: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list }, filesCli: { relocateOriginal } }));
vi.mock("react-toastify", () => ({ toast: { error: report } }));
const reference = LocationEntryRef.create({
  locationId: 4n,
  path: "new/path.jpg",
  facts: { sizeBytes: 8n, mode: 420, mtimeNs: 9n },
});
vi.mock("./location-entry-browser", () => ({
  LocationEntryBrowser: ({ onChoose }: { onChoose: (ref: LocationEntryRef) => void }) => <button onClick={() => onChoose(reference)}>Choose path.jpg</button>,
}));
beforeEach(() => {
  report.mockReset();
  list.mockReset().mockReturnValue({
    response: Promise.resolve({ locations: [Location.create({ id: 4n, name: "Pictures" })], hasMore: false }),
  });
  relocateOriginal.mockReset().mockReturnValue({ response: Promise.resolve({}) });
});

it("closes a successful association before refreshing and never offers to repeat it on a refresh failure", async () => {
  const onSaved = vi.fn().mockRejectedValue(new Error("Refresh unavailable"));
  const onClose = vi.fn();
  const { unmount } = render(<RelocateOriginalDialog file={File.create({ id: 8n, name: "organized.jpg" })} onSaved={onSaved} onClose={onClose} />);
  onClose.mockImplementation(unmount);
  await choose();
  await userEvent.click(screen.getByRole("button", { name: "Link original" }));
  await waitFor(() => expect(report).toHaveBeenCalledWith("Refresh unavailable"));
  expect(relocateOriginal).toHaveBeenCalledOnce();
  expect(onClose).toHaveBeenCalledOnce();
  expect(onSaved).toHaveBeenCalledOnce();
  expect(onClose.mock.invocationCallOrder[0]).toBeLessThan(onSaved.mock.invocationCallOrder[0]);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
});
const choose = async () => {
  await waitFor(() => expect(list).toHaveBeenCalled());
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  await userEvent.click(await screen.findByRole("option", { name: "Pictures" }));
  await userEvent.click(screen.getByRole("button", { name: "Choose path.jpg" }));
};

it("reviews the exact File and observed path before replacing the original reference", async () => {
  const onSaved = vi.fn().mockResolvedValue(undefined),
    onClose = vi.fn();
  render(<RelocateOriginalDialog file={File.create({ id: 8n, name: "organized.jpg" })} onSaved={onSaved} onClose={onClose} />);
  await choose();
  expect(screen.getByRole("alert")).toHaveTextContent("Link organized.jpg to Pictures/new/path.jpg?");
  expect(relocateOriginal).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Link original" }));
  await waitFor(() => expect(relocateOriginal).toHaveBeenCalledExactlyOnceWith({ fileId: 8n, reference, dryrun: false }));
  expect(onSaved).toHaveBeenCalledOnce();
  expect(onClose).toHaveBeenCalledOnce();
});

it("retains the reviewed selection when the server refuses an occupied path", async () => {
  relocateOriginal.mockImplementation(() => ({ response: Promise.reject(new Error("This path belongs to another File")) }));
  const onClose = vi.fn();
  render(<RelocateOriginalDialog file={File.create({ id: 8n, name: "organized.jpg" })} onSaved={vi.fn()} onClose={onClose} />);
  await choose();
  await userEvent.click(screen.getByRole("button", { name: "Link original" }));
  expect(await screen.findByText("This path belongs to another File")).toBeInTheDocument();
  expect(within(screen.getByRole("dialog")).getByRole("button", { name: "Choose another file" })).toBeEnabled();
  expect(onClose).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Choose another file" }));
  expect(screen.queryByText("This path belongs to another File")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Choose path.jpg" })).toBeEnabled();
  await userEvent.click(screen.getByRole("button", { name: "Choose path.jpg" }));
  expect(screen.getByRole("alert")).toHaveTextContent("Link organized.jpg to Pictures/new/path.jpg?");
  expect(relocateOriginal).toHaveBeenCalledOnce();
});

it("serializes Location pages and retains the reviewed original while retrying the failed page", async () => {
  const first = Location.create({ id: 4n, name: "Pictures" });
  list.mockReturnValueOnce({ response: Promise.resolve({ locations: [first], hasMore: true }) });
  render(<RelocateOriginalDialog file={File.create({ id: 8n, name: "organized.jpg" })} onSaved={vi.fn()} onClose={vi.fn()} />);
  await choose();
  let reject!: (error: Error) => void;
  list.mockReturnValueOnce({
    response: new Promise((_, fail) => {
      reject = fail;
    }),
  });
  const more = screen.getByRole("button", { name: "More Locations" });
  await userEvent.dblClick(more);
  expect(list).toHaveBeenCalledTimes(2);
  expect(more).toBeDisabled();
  await act(async () => reject(new Error("Next Locations unavailable")));
  expect(await screen.findByText("Next Locations unavailable")).toBeInTheDocument();
  expect(screen.getByText(/This replaces its original reference/)).toHaveTextContent("Pictures/new/path.jpg");
  expect(screen.getByRole("button", { name: "Link original" })).toBeEnabled();
  list.mockReturnValueOnce({ response: Promise.resolve({ locations: [first, Location.create({ id: 5n, name: "Other" })], hasMore: false }) });
  await userEvent.click(screen.getByRole("button", { name: "Retry Locations" }));
  await waitFor(() => expect(screen.queryByText("Next Locations unavailable")).not.toBeInTheDocument());
  expect(list.mock.calls[1][0]).toEqual({ afterId: 4n, limit: 50, query: "" });
  expect(list.mock.calls[2][0]).toEqual(list.mock.calls[1][0]);
  await userEvent.click(screen.getByRole("combobox", { name: "Location" }));
  expect(screen.getAllByRole("option", { name: "Pictures" })).toHaveLength(1);
  expect(screen.getByRole("option", { name: "Other" })).toBeInTheDocument();
  expect(relocateOriginal).not.toHaveBeenCalled();
});

it.each(["cancel", "unmount"] as const)("aborts Location reads and ignores late errors on %s", async (action) => {
  let reject!: (error: Error) => void;
  list.mockReturnValueOnce({
    response: new Promise((_, fail) => {
      reject = fail;
    }),
  });
  const onClose = vi.fn();
  const view = render(<RelocateOriginalDialog file={File.create({ id: 8n, name: "organized.jpg" })} onSaved={vi.fn()} onClose={onClose} />);
  onClose.mockImplementation(view.unmount);
  const signal = list.mock.calls[0][1].abort as AbortSignal;
  if (action === "cancel") await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  else view.unmount();
  expect(signal.aborted).toBe(true);
  await act(async () => reject(new Error("Closed Location request failed")));
  expect(report).not.toHaveBeenCalled();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(relocateOriginal).not.toHaveBeenCalled();
});
