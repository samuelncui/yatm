import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import { LibrarySettings, UpdateLibrarySettingsReply } from "@/entity";
const { get, update } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { getLibrary: get, updateLibrary: update } }));
import { SettingsBrowser } from "./settings";
beforeEach(() => {
  get.mockReset();
  update.mockReset();
});
describe("Library Settings", () => {
  it("owns the persisted visibility option and preserves its revision", async () => {
    const settings = LibrarySettings.create({ includeUnbackedFiles: true, revision: 4n, autoCollectFiles: true, confirmPermanentDelete: true });
    get.mockReturnValue({ response: Promise.resolve(settings) });
    update.mockReturnValue({
      response: Promise.resolve(UpdateLibrarySettingsReply.create({ settings: { ...settings, includeUnbackedFiles: false, revision: 5n } })),
    });
    render(
      <MemoryRouter>
        <SettingsBrowser />
      </MemoryRouter>,
    );
    const toggle = screen.getByRole("checkbox", { name: "Include unbacked files" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(update).toHaveBeenCalledWith({ settings: { ...settings, includeUnbackedFiles: false } });
    await waitFor(() => expect(toggle).not.toBeChecked());
    expect(screen.getByText(/Locations are unaffected/)).toBeInTheDocument();
  });
  it("reviews background collection before enabling it and links the created Jobs", async () => {
    const settings = LibrarySettings.create({ includeUnbackedFiles: true, revision: 2n, confirmPermanentDelete: true });
    get.mockReturnValue({ response: Promise.resolve(settings) });
    update.mockReturnValue({
      response: Promise.resolve(UpdateLibrarySettingsReply.create({ settings: { ...settings, autoCollectFiles: true }, collectionJobIds: [80n] })),
    });
    render(
      <MemoryRouter>
        <SettingsBrowser />
      </MemoryRouter>,
    );
    const toggle = screen.getByRole("checkbox", { name: "Automatically add Location files to Library" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Enable" }));
    expect(await screen.findByRole("link", { name: "Collection Job 80" })).toHaveAttribute("href", "/jobs/80");
    expect(update).toHaveBeenCalledExactlyOnceWith({ settings: { ...settings, autoCollectFiles: true } });
  });
});
