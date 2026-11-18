import { screen, waitFor } from "@testing-library/react";
import { renderPage } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GeneralSettings, SettingsValue } from "@/entity";
import { activateLocale } from "@/i18n";

const { get, update } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { get, update } }));
import { GeneralSettingsBrowser } from "./settings-general";

const initial = GeneralSettings.create({ confirmRemove: true });
const value = (settings = initial) => SettingsValue.create({ value: { oneofKind: "general", general: GeneralSettings.clone(settings) } });
const open = () => renderPage(<GeneralSettingsBrowser />);

beforeEach(async () => {
  window.localStorage.clear();
  await activateLocale("en");
  get.mockReset().mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update
    .mockReset()
    .mockImplementation(({ value: settings }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(settings) }) }));
});

describe("General Settings", () => {
  it("cancels unsaved Trash confirmation changes", async () => {
    open();
    const toggle = await screen.findByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(screen.getByText("Delete moves selected items to Trash immediately.")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(toggle).toBeChecked();
    expect(update).not.toHaveBeenCalled();
  });

  it("lists every confirmation as shown or hidden and saves a changed one with the inverted stored meaning", async () => {
    get.mockReturnValue({ response: Promise.resolve({ value: value({ ...initial, skipAddMediaConfirmation: true }) }) });
    open();
    const remove = await screen.findByRole("checkbox", { name: "Confirm Delete" });
    const entries = screen.getByRole("checkbox", { name: "Confirm adding selected inventory entries to Library" });
    const media = screen.getByRole("checkbox", { name: "Confirm adding a Media’s whole inventory to Library" });
    await waitFor(() => expect(remove).toBeEnabled());
    expect(screen.getByText("Confirmations")).toBeVisible();
    expect(remove).toBeChecked();
    expect(entries).toBeChecked();
    expect(media).not.toBeChecked();
    await userEvent.click(entries);
    await userEvent.click(media);
    expect(entries).not.toBeChecked();
    expect(media).toBeChecked();
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledExactlyOnceWith({
      value: { value: { oneofKind: "general", general: { ...initial, skipAddEntriesConfirmation: true, skipAddMediaConfirmation: false } } },
    });
    expect(await screen.findByText("Settings saved.")).toBeVisible();
  });

  it("shows all confirmations again in the draft and offers that only while one is hidden", async () => {
    get.mockReturnValue({
      response: Promise.resolve({ value: value({ ...initial, confirmRemove: false, skipAddEntriesConfirmation: true, skipAddMediaConfirmation: true }) }),
    });
    open();
    const remove = await screen.findByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(remove).toBeEnabled());
    const showAll = screen.getByRole("button", { name: "Show all confirmations" });
    const confirmations = [
      remove,
      screen.getByRole("checkbox", { name: "Confirm adding selected inventory entries to Library" }),
      screen.getByRole("checkbox", { name: "Confirm adding a Media’s whole inventory to Library" }),
    ];
    for (const confirmation of confirmations) expect(confirmation).not.toBeChecked();
    await userEvent.click(showAll);
    for (const confirmation of confirmations) expect(confirmation).toBeChecked();
    expect(showAll).toBeDisabled();
    expect(update).not.toHaveBeenCalled();
    // Hiding any one confirmation offers the action again.
    for (const confirmation of confirmations) {
      await userEvent.click(confirmation);
      expect(showAll).toBeEnabled();
      await userEvent.click(confirmation);
      expect(showAll).toBeDisabled();
    }
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledExactlyOnceWith({ value: { value: { oneofKind: "general", general: initial } } });
  });

  it("switches language at once while nothing is unsaved", async () => {
    open();
    const select = await screen.findByRole("combobox", { name: "Interface language" });
    await userEvent.click(select);
    await userEvent.click(screen.getByRole("option", { name: "简体中文" }));
    expect(await screen.findByText("确认提示")).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await activateLocale("en");
  });

  it("asks before switching language over an unsaved draft and keeps the draft when declined", async () => {
    open();
    const toggle = await screen.findByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    const select = screen.getByRole("combobox", { name: "Interface language" });
    expect(select).not.toHaveAttribute("aria-disabled", "true");
    await userEvent.click(select);
    await userEvent.click(screen.getByRole("option", { name: "简体中文" }));
    const dialog = await screen.findByRole("dialog", { name: "Discard unsaved changes?" });
    expect(dialog).toHaveTextContent("Switching the language reloads the interface");
    await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Interface language" })).toHaveTextContent("English");
    expect(screen.getByRole("checkbox", { name: "Confirm Delete" })).not.toBeChecked();
  });

  it("switches language after the discard is confirmed", async () => {
    open();
    const toggle = await screen.findByRole("checkbox", { name: "Confirm Delete" });
    await waitFor(() => expect(toggle).toBeEnabled());
    await userEvent.click(toggle);
    await userEvent.click(screen.getByRole("combobox", { name: "Interface language" }));
    await userEvent.click(screen.getByRole("option", { name: "简体中文" }));
    await userEvent.click(await screen.findByRole("button", { name: "Discard" }));
    expect(await screen.findByText("确认提示")).toBeVisible();
    expect(update).not.toHaveBeenCalled();
    await activateLocale("en");
  });
});
