import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { beforeEach, expect, it, vi } from "vitest";
import { render } from "@/state/test-render";
import { JobSettings, LibrarySettings, SettingsGroup, SettingsValue } from "@/entity";

const { get, update, chunk, finishChunk, libraryChunk, finishLibraryChunk } = vi.hoisted(() => {
  let finishChunk!: () => void;
  const chunk = new Promise<void>((resolve) => (finishChunk = resolve));
  let finishLibraryChunk!: () => void;
  const libraryChunk = new Promise<void>((resolve) => (finishLibraryChunk = resolve));
  return { get: vi.fn(), update: vi.fn(), chunk, finishChunk, libraryChunk, finishLibraryChunk };
});
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { get, update } }));
vi.mock("@/pages/settings-jobs", async (original) => {
  await chunk;
  return original();
});
vi.mock("@/pages/settings", async (original) => {
  await libraryChunk;
  return original();
});
import App from "@/app";

const jobValue = (readBatch = 256) =>
  SettingsValue.create({
    value: {
      oneofKind: "job",
      job: JobSettings.create({ execution: { readBatch, readBufferMax: 4096, writeBufferMax: 4096, writeBatchSize: 256, flushIntervalMs: 1000 } }),
    },
  });
const jobsTab = () => within(screen.getByRole("navigation", { name: "Settings views" })).getByRole("tab", { name: "Jobs" });
const open = (path = "/settings/jobs") => render(<RouterProvider router={createMemoryRouter([{ path: "*", element: <App /> }], { initialEntries: [path] })} />);

beforeEach(() => {
  get.mockReset().mockImplementation(({ group }: { group: SettingsGroup }) => ({
    response: Promise.resolve({
      value:
        group === SettingsGroup.JOB
          ? jobValue()
          : SettingsValue.create({ value: { oneofKind: "library", library: LibrarySettings.create({ confirmRemove: true }) } }),
    }),
  }));
  update.mockReset().mockImplementation(({ value }: { value: SettingsValue }) => ({ response: Promise.resolve({ value }) }));
});

it("keeps one Settings shell from the cold route chunk through the first Settings read", async () => {
  let finishRead!: (reply: { value: SettingsValue }) => void;
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finishRead = resolve)) });
  open();
  const heading = screen.getByRole("heading", { name: "Jobs" });
  expect(screen.getByText(/next attempt starts/)).toBeVisible();
  expect(screen.getAllByRole("progressbar")).toHaveLength(1);
  expect(screen.getByRole("progressbar", { name: "Loading Job settings" })).toBeVisible();
  expect(jobsTab()).toHaveAttribute("aria-selected", "true");
  expect(get).not.toHaveBeenCalled();
  await act(async () => finishChunk());
  await waitFor(() => expect(get).toHaveBeenCalledExactlyOnceWith({ group: SettingsGroup.JOB }));
  expect(screen.getByRole("heading", { name: "Jobs" })).toBe(heading);
  expect(screen.getAllByRole("progressbar")).toHaveLength(1);
  expect(screen.getByRole("progressbar", { name: "Loading Job settings" })).toBeVisible();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  await act(async () => finishRead({ value: jobValue() }));
  await userEvent.click(screen.getByRole("button", { name: /Advanced/ }));
  expect(screen.getByRole("spinbutton", { name: "Manifest batch (items)" })).toHaveValue(256);
  expect(screen.getByRole("heading", { name: "Jobs" })).toBe(heading);
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
});

it("protects unsaved Settings across tabs and discards only after confirmation", async () => {
  finishChunk();
  open();
  await userEvent.click(await screen.findByRole("button", { name: /Advanced/ }));
  const batch = await screen.findByRole("spinbutton", { name: "Manifest batch (items)" });
  await userEvent.clear(batch);
  await userEvent.type(batch, "128");
  const library = within(screen.getByRole("navigation", { name: "Settings views" })).getByRole("tab", { name: "Library" });
  await userEvent.click(library);
  let prompt = within(await screen.findByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(prompt.getByRole("button", { name: "Keep editing" }));
  expect(batch).toHaveValue(128);
  expect(get).toHaveBeenCalledOnce();
  await userEvent.click(library);
  prompt = within(await screen.findByRole("dialog", { name: "Discard unsaved changes?" }));
  await userEvent.click(prompt.getByRole("button", { name: "Discard" }));
  expect(library).toHaveAttribute("aria-selected", "true");
  expect(screen.getByRole("heading", { name: "Library" })).toBeVisible();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  expect(screen.getByRole("progressbar", { name: "Loading Library settings" })).toBeVisible();
  expect(get).toHaveBeenCalledOnce();
  await act(async () => finishLibraryChunk());
  await screen.findByRole("checkbox", { name: "Confirm Delete" });
  expect(library).toHaveAttribute("aria-selected", "true");
  await userEvent.click(jobsTab());
  await userEvent.click(await screen.findByRole("button", { name: /Advanced/ }));
  expect(await screen.findByRole("spinbutton", { name: "Manifest batch (items)" })).toHaveValue(256);
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  expect(get).toHaveBeenCalledTimes(2);
  expect(update).not.toHaveBeenCalled();
});

it("keeps the matching Settings tab selected for a trailing-slash route", async () => {
  finishChunk();
  open("/settings/jobs/");
  await userEvent.click(await screen.findByRole("button", { name: /Advanced/ }));
  await screen.findByRole("spinbutton", { name: "Manifest batch (items)" });
  expect(jobsTab()).toHaveAttribute("aria-selected", "true");
});

it("warns on page unload only while Settings have unsaved edits", async () => {
  finishChunk();
  open();
  await userEvent.click(await screen.findByRole("button", { name: /Advanced/ }));
  const batch = screen.getByRole("spinbutton", { name: "Manifest batch (items)" });
  await userEvent.clear(batch);
  await userEvent.type(batch, "128");
  const dirtyUnload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(dirtyUnload);
  expect(dirtyUnload.defaultPrevented).toBe(true);
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(batch).toHaveValue(256);
  const cleanUnload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(cleanUnload);
  expect(cleanUnload.defaultPrevented).toBe(false);
  await userEvent.clear(batch);
  await userEvent.type(batch, "128");
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Job settings saved.");
  const savedUnload = new Event("beforeunload", { cancelable: true });
  window.dispatchEvent(savedUnload);
  expect(savedUnload.defaultPrevented).toBe(false);
  const library = within(screen.getByRole("navigation", { name: "Settings views" })).getByRole("tab", { name: "Library" });
  await userEvent.click(library);
  await screen.findByRole("heading", { name: "Library" });
  expect(screen.queryByRole("dialog", { name: "Discard unsaved changes?" })).not.toBeInTheDocument();
});
