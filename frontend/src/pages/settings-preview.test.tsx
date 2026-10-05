import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PreviewSettings, SettingsValue } from "@/entity";

const { get, update, capabilities } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), capabilities: vi.fn() }));
vi.mock("@/api", () => ({ settingsCli: { get, update }, previewCli: { getCapabilities: capabilities } }));
import { PreviewSettingsBrowser } from "./settings-preview";

const initial = PreviewSettings.create({
  enabled: false,
  command: "",
  concurrency: 2,
  timeoutSeconds: 600,
  maxInputPixels: 64000000n,
  generators: [
    {
      enabled: true,
      extensions: [
        { name: "jpg", enabled: true },
        { name: "png", enabled: true },
      ],
      options: { oneofKind: "image", image: { maxWidth: 512, maxHeight: 512, format: "webp", quality: 80 } },
    },
    {
      enabled: true,
      extensions: [{ name: "mp4", enabled: true }],
      options: {
        oneofKind: "video",
        video: {
          posterWidth: 512,
          posterHeight: 512,
          timelineWidth: 160,
          timelineHeight: 90,
          timelineIntervalSeconds: 10,
          timelineMaxFrames: 100,
          format: "webp",
          quality: 75,
        },
      },
    },
    {
      enabled: true,
      extensions: [{ name: "heic", enabled: true }],
      options: { oneofKind: "image", image: { maxWidth: 1024, maxHeight: 1024, format: "jpg", quality: 90 } },
    },
  ],
});
const value = (preview = initial) => SettingsValue.create({ value: { oneofKind: "preview", preview: PreviewSettings.clone(preview) } });
const updated = (index = 0): PreviewSettings => {
  const input = update.mock.calls[index][0] as { value: SettingsValue };
  if (input.value.value.oneofKind !== "preview") throw new Error("Expected Preview settings");
  return input.value.value.preview;
};
const imageForm = () => within(screen.getByRole("group", { name: "Image · jpg, png" }));
const videoForm = () => within(screen.getByRole("group", { name: "Video" }));
const choose = async (name: string) => {
  await userEvent.click(screen.getByRole("combobox", { name: "Type" }));
  await userEvent.click(screen.getByRole("option", { name }));
};
beforeEach(() => {
  capabilities.mockReset().mockReturnValue({
    response: Promise.resolve({
      available: true,
      version: "1",
      ffmpegVersion: "7",
      kinds: ["image", "video"],
      inputExtensions: ["jpg", "png", "mp4"],
      inputExtensionsByKind: { image: { extensions: ["jpg", "png", "heic", "cr3"] }, video: { extensions: ["mp4", "mov"] } },
      librawVersion: "0.22.2",
      outputFormats: ["webp", "jpg", "png"],
      reason: "",
    }),
  });
  get.mockReset().mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update
    .mockReset()
    .mockImplementation(({ value: settings }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(settings) }) }));
});
const open = async () => {
  render(<PreviewSettingsBrowser />);
  await screen.findByRole("group", { name: "Image · jpg, png" });
};
describe("Preview settings", () => {
  it("uses consistent sections and places all page actions after the capability details", async () => {
    await open();
    const group = screen.getByRole("group", { name: "Image · jpg, png" });
    expect(group.querySelector(":scope > legend")).toBeNull();
    const supported = screen.getByText("Supported formats");
    expect(supported.compareDocumentPosition(screen.getByRole("button", { name: "Save" })) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    const actions = screen.getByRole("button", { name: "Save" }).parentElement;
    expect(Array.from(actions!.children, (action) => action.textContent?.trim())).toEqual(["Save", "Cancel"]);
    expect(await screen.findByText(/LibRaw 0.22.2/)).toBeVisible();
    await waitFor(() => expect(screen.getByRole("button", { name: "Check again" })).toBeEnabled());
    expect(screen.getByText("Image: jpg, png, heic, cr3")).toBeVisible();
  });

  it("searches format capabilities, preserves a custom extension and cancels unsaved changes", async () => {
    await open();
    await choose("Video");
    const extensions = videoForm().getByRole("combobox", { name: "File extensions" });
    await userEvent.type(extensions, "mov");
    expect(screen.getAllByRole("option")).toHaveLength(1);
    await userEvent.click(screen.getByRole("option", { name: "mov" }));
    await userEvent.type(extensions, ".CUSTOM{Enter}");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated().generators[1].extensions).toEqual([
      { name: "mp4", enabled: true },
      { name: "mov", enabled: true },
      { name: "custom", enabled: true },
    ]);
    await userEvent.type(extensions, "avi{Enter}");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(videoForm().queryByText("avi", { selector: '[class*="MuiChip-label"]' })).not.toBeInTheDocument();
    expect(videoForm().getByText("custom", { selector: '[class*="MuiChip-label"]' })).toBeInTheDocument();
  });

  it("rechecks Preview capabilities without discarding unsaved settings on failure or success", async () => {
    await open();
    const width = imageForm().getByRole("spinbutton", { name: "Maximum width (px)" });
    fireEvent.change(width, { target: { value: "768" } });
    let reject!: (error: Error) => void;
    capabilities.mockReturnValueOnce({ response: new Promise((_, fail) => (reject = fail)) });

    const checkAgain = screen.getByRole("button", { name: "Check again" });
    await userEvent.click(checkAgain);
    expect(capabilities).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/LibRaw 0.22.2/)).toBeVisible();
    expect(checkAgain).toBeDisabled();
    reject(new Error("Helper check failed"));
    expect(await screen.findByText("Helper check failed")).toBeVisible();
    expect(screen.getByText(/LibRaw 0.22.2/)).toBeVisible();
    expect(width).toHaveValue(768);

    await userEvent.click(checkAgain);
    await waitFor(() => expect(capabilities).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(screen.queryByText("Helper check failed")).not.toBeInTheDocument());
    expect(width).toHaveValue(768);
    expect(update).not.toHaveBeenCalled();
  });

  it("removes a mistyped extension through its chip without discarding other edits", async () => {
    await open();
    await choose("Video");
    fireEvent.change(videoForm().getByRole("spinbutton", { name: "Poster width (px)" }), { target: { value: "768" } });
    await userEvent.type(videoForm().getByRole("combobox", { name: "File extensions" }), "bad,extension{Enter}");
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Enter a valid file extension./)).toBeVisible();
    videoForm().getByRole("button", { name: "bad,extension" }).focus();
    await userEvent.keyboard("{Delete}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated().generators[1].extensions).toEqual([{ name: "mp4", enabled: true }]);
    expect(updated().generators[1].options).toMatchObject({ oneofKind: "video", video: { posterWidth: 768 } });
  });

  it("allows moving an extension to another configured route after removing its chip", async () => {
    await open();
    imageForm().getByRole("button", { name: "jpg" }).focus();
    await userEvent.keyboard("{Delete}");
    await choose("Image · heic");
    const other = within(screen.getByRole("group", { name: "Image · heic" }));
    await userEvent.type(other.getByRole("combobox", { name: "File extensions" }), "jpg{Enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated().generators[0].extensions).toEqual([{ name: "png", enabled: true }]);
    expect(updated().generators[2].extensions).toContainEqual({ name: "jpg", enabled: true });
  });

  it("preserves options and extension switches across master and category toggles", async () => {
    await open();
    expect(screen.getByLabelText("Enable generation")).not.toBeChecked();
    await userEvent.click(imageForm().getByRole("combobox", { name: "File extensions" }));
    await userEvent.click(screen.getByRole("option", { name: "png" }));
    await userEvent.keyboard("{Escape}");
    await userEvent.click(imageForm().getByLabelText("Enabled"));
    await userEvent.click(screen.getByLabelText("Enable generation"));
    await userEvent.click(screen.getByLabelText("Enable generation"));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    const preview = updated();
    expect(preview.enabled).toBe(false);
    expect(preview.generators[0]).toMatchObject({
      enabled: false,
      extensions: [
        { name: "jpg", enabled: true },
        { name: "png", enabled: false },
      ],
      options: initial.generators[0].options,
    });
    expect(preview.generators[1]).toEqual(initial.generators[1]);
    await userEvent.click(imageForm().getByLabelText("Enabled"));
    expect(imageForm().queryByText("png", { selector: '[class*="MuiChip-label"]' })).not.toBeInTheDocument();
    expect(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" })).toHaveValue(512);
  });

  it("edits and validates common runtime limits without per-type executable fields", async () => {
    await open();
    expect(screen.queryByLabelText("Image executable")).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole("spinbutton", { name: "Concurrency" }), { target: { value: "17" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Concurrency must be between 1 and 16.");
    expect(update).not.toHaveBeenCalled();
    fireEvent.change(screen.getByRole("spinbutton", { name: "Concurrency" }), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Executable"), { target: { value: "/opt/yatm-preview" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: "Timeout (seconds)" }), { target: { value: "900" } });
    fireEvent.change(screen.getByRole("spinbutton", { name: "Maximum input pixels" }), { target: { value: "32000000" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated()).toMatchObject({
      command: "/opt/yatm-preview",
      concurrency: 3,
      timeoutSeconds: 900,
      maxInputPixels: 32000000n,
    });
  });
  it("edits locally, cancels, and saves all generators without changing other Library preferences", async () => {
    await open();
    const width = imageForm().getByRole("spinbutton", { name: "Maximum width (px)" });
    fireEvent.change(width, { target: { value: "768" } });
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(width).toHaveValue(512);
    expect(update).not.toHaveBeenCalled();
    fireEvent.change(width, { target: { value: "768" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    const expected = PreviewSettings.clone(initial);
    if (expected.generators[0].options.oneofKind === "image") expected.generators[0].options.image.maxWidth = 768;
    expect(update).toHaveBeenCalledExactlyOnceWith({ value: { value: { oneofKind: "preview", preview: expected } } });
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.change(width, { target: { value: "900" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    expect(updated(1).generators[0].options).toMatchObject({ oneofKind: "image", image: { maxWidth: 900 } });
  });

  it("saves video sampling, output and advanced routing settings", async () => {
    await open();
    await choose("Video");
    fireEvent.change(videoForm().getByRole("spinbutton", { name: "Sampling interval (seconds)" }), { target: { value: "20" } });
    fireEvent.change(videoForm().getByRole("spinbutton", { name: "Timeline tile width (px)" }), { target: { value: "201" } });
    await userEvent.click(videoForm().getByRole("combobox", { name: "Output format" }));
    await userEvent.click(screen.getByRole("option", { name: "PNG" }));
    await userEvent.type(videoForm().getByRole("combobox", { name: "File extensions" }), "mov{Enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated().generators[1]).toMatchObject({
      extensions: [
        { name: "mp4", enabled: true },
        { name: "mov", enabled: true },
      ],
      options: { oneofKind: "video", video: { timelineIntervalSeconds: 20, timelineWidth: 201, format: "png" } },
    });
  });

  it("validates ranges, duplicate routes and the combined timeline size before saving", async () => {
    await open();
    await choose("Video");
    const frames = videoForm().getByRole("spinbutton", { name: "Maximum frames" });
    fireEvent.change(frames, { target: { value: "1001" } });
    expect(frames).toBeInvalid();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).not.toHaveBeenCalled();
    fireEvent.change(frames, { target: { value: "1000" } });
    fireEvent.change(videoForm().getByRole("spinbutton", { name: "Timeline tile width (px)" }), { target: { value: "8192" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Reduce the timeline tile size/)).toBeInTheDocument();
    expect(update).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.type(videoForm().getByRole("combobox", { name: "File extensions" }), ".JPG{Enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/The extension "jpg" is assigned more than once./)).toBeInTheDocument();
    expect(update).not.toHaveBeenCalled();
  });

  it("keeps edits after a save failure and explicitly reloads the saved value", async () => {
    await open();
    update.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Library settings changed; reload and retry")) }));
    const width = imageForm().getByRole("spinbutton", { name: "Maximum width (px)" });
    fireEvent.change(width, { target: { value: "768" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Library settings changed; reload and retry")).toBeInTheDocument();
    expect(width).toHaveValue(768);
    get.mockReturnValueOnce({ response: Promise.resolve({ value: value() }) });
    await userEvent.click(screen.getByRole("button", { name: "Reload saved settings" }));
    await waitFor(() => expect(width).toHaveValue(512));
    fireEvent.change(width, { target: { value: "900" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated(1).generators[0].options).toMatchObject({ oneofKind: "image", image: { maxWidth: 900 } });
  });

  it("locks runtime, output, routing and dependent actions during Save without discarding failures or drafts", async () => {
    let reject!: (error: Error) => void;
    update.mockImplementation(() => ({ response: new Promise((_, fail) => (reject = fail)) }));
    await open();
    const width = imageForm().getByRole("spinbutton", { name: "Maximum width (px)" });
    fireEvent.change(width, { target: { value: "768" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    for (const role of ["spinbutton", "switch", "textbox"]) {
      for (const input of screen.queryAllByRole(role)) expect(input).toBeDisabled();
    }
    expect(screen.getByRole("combobox", { name: "Type" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "File extensions" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Output format" })).toHaveAttribute("aria-disabled", "true");
    const chip = imageForm().getByText("jpg").parentElement!;
    expect(chip).toHaveAttribute("aria-disabled", "true");
    fireEvent.keyUp(chip, { key: "Delete" });
    expect(imageForm().getByText("jpg")).toBeVisible();
    expect(screen.getByRole("button", { name: "Check again" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    reject(new Error("Preview Settings unavailable"));
    expect(await screen.findByText("Preview Settings unavailable")).toBeVisible();
    expect(width).toHaveValue(768);
    expect(width).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByText("Preview Settings unavailable")).toBeVisible();
    expect(screen.getByRole("button", { name: "Reload saved settings" })).toBeDisabled();
    reject(new Error("Still unavailable"));
    expect(await screen.findByText("Still unavailable")).toBeVisible();
    expect(width).toHaveValue(768);
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });

  it("retries an initial settings load failure", async () => {
    get.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Service unavailable")) }));
    render(<PreviewSettingsBrowser />);
    expect(await screen.findByText("Service unavailable")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByRole("group", { name: "Image · jpg, png" });
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("searches preview types and shows one card without losing drafts when switching", async () => {
    await open();
    expect(screen.getAllByRole("group", { name: /^(Image|Video)( ·|$)/ })).toHaveLength(1);
    fireEvent.change(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" }), { target: { value: "768" } });
    const selector = screen.getByRole("combobox", { name: "Type" });
    await userEvent.clear(selector);
    await userEvent.type(selector, "heic");
    expect(screen.getAllByRole("option")).toHaveLength(1);
    await userEvent.click(screen.getByRole("option", { name: "Image · heic" }));
    expect(screen.getByRole("group", { name: "Image · heic" })).toBeInTheDocument();
    expect(screen.getAllByRole("group", { name: /^(Image|Video)( ·|$)/ })).toHaveLength(1);
    await choose("Video");
    fireEvent.change(videoForm().getByRole("spinbutton", { name: "Sampling interval (seconds)" }), { target: { value: "20" } });
    await choose("Image · jpg, png");
    expect(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" })).toHaveValue(768);
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    const generators = updated().generators;
    expect(generators[0].options).toMatchObject({ oneofKind: "image", image: { maxWidth: 768 } });
    expect(generators[1].options).toMatchObject({ oneofKind: "video", video: { timelineIntervalSeconds: 20 } });
    expect(screen.queryByText(/generator \d/i)).not.toBeInTheDocument();
  });

  it("opens the invalid card when an unsaved hidden field fails validation", async () => {
    await open();
    fireEvent.change(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" }), { target: { value: "9000" } });
    await choose("Video");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Enter valid dimensions/)).toBeInTheDocument();
    expect(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" })).toHaveValue(9000);
    expect(update).not.toHaveBeenCalled();
  });

  it("allows saving without an installed helper and retains the sibling executable default", async () => {
    capabilities.mockReturnValue({ response: Promise.resolve({ available: false, reason: "Preview helper not found" }) });
    await open();
    expect(await screen.findByText(/Preview helper not found/)).toBeInTheDocument();
    fireEvent.change(imageForm().getByRole("spinbutton", { name: "Maximum width (px)" }), { target: { value: "768" } });
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Preview settings saved.");
    expect(updated()).toMatchObject({
      command: "",
      enabled: false,
      concurrency: 2,
      timeoutSeconds: 600,
      maxInputPixels: 64000000n,
    });
  });
});
