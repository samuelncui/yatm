import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { File, Location, LocationEntryRef, OnlineBinding } from "@/entity";
import { RelocateOriginalDialog } from "./relocate-original";

const { list, relocateOriginal } = vi.hoisted(() => ({ list: vi.fn(), relocateOriginal: vi.fn() }));
vi.mock("@/api", () => ({ locationCli: { list }, fileCatalogCli: { relocateOriginal } }));
const reference = LocationEntryRef.create({
  locationId: 4n,
  path: "new/path.jpg",
  bindingToken: "root",
  facts: { size: 8n, mode: 420, mtimeNs: 9n, identity: "object" },
});
vi.mock("./location-entry-browser", () => ({
  LocationEntryBrowser: ({ onChoose }: { onChoose: (ref: LocationEntryRef) => void }) => <button onClick={() => onChoose(reference)}>Choose path.jpg</button>,
}));
beforeEach(() => {
  list.mockReset().mockReturnValue({
    response: Promise.resolve({ locations: [Location.create({ id: 4n, name: "Pictures", binding: OnlineBinding.CONFIRMED })], hasMore: false }),
  });
  relocateOriginal.mockReset().mockReturnValue({ response: Promise.resolve({}) });
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
  await waitFor(() => expect(relocateOriginal).toHaveBeenCalledExactlyOnceWith({ fileId: 8n, reference }));
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
});
