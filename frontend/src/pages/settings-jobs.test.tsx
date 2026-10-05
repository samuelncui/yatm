import { screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { JobExecutionSettings, JobSettings, SettingsValue } from "@/entity";

const { get, update } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn() }));
vi.mock("@/api", () => ({ settingsCli: { get, update } }));
import { JobSettingsBrowser } from "./settings-jobs";

const execution = (readBatch: number) =>
  JobExecutionSettings.create({ readBatch, readBufferMax: 4096, writeBufferMax: 4096, writeBatchSize: 256, flushIntervalMs: 1000 });
const value = (readBatch = 256) => SettingsValue.create({ value: { oneofKind: "job", job: JobSettings.create({ execution: execution(readBatch) }) } });

beforeEach(() => {
  get.mockReset().mockReturnValue({ response: Promise.resolve({ value: value() }) });
  update
    .mockReset()
    .mockImplementation(({ value: settings }: { value: SettingsValue }) => ({ response: Promise.resolve({ value: SettingsValue.clone(settings) }) }));
});

describe("Job Settings", () => {
  it("saves pipeline limits through the shared typed Settings API", async () => {
    render(<JobSettingsBrowser />);
    const batch = await screen.findByRole("spinbutton", { name: "Read batch" });
    await waitFor(() => expect(batch).toHaveValue(256));
    await userEvent.clear(batch);
    await userEvent.type(batch, "128");
    expect(screen.getByRole("spinbutton", { name: "Write batch" })).toHaveAttribute("max", "4096");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(update).toHaveBeenCalledExactlyOnceWith({
      value: { value: { oneofKind: "job", job: { execution: execution(128) } } },
    });
    await waitFor(() => expect(batch).toHaveValue(128));
    expect(screen.getByText("Job settings saved.")).toBeInTheDocument();
    expect(screen.getByText(/next attempt starts/)).toBeVisible();
  });

  it("refuses a write batch larger than its buffer before asking the service", async () => {
    render(<JobSettingsBrowser />);
    const batch = await screen.findByRole("spinbutton", { name: "Write batch" });
    await waitFor(() => expect(batch).toHaveValue(256));
    await userEvent.clear(batch);
    await userEvent.type(batch, "8192");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/Write batch must not exceed the write buffer/)).toBeInTheDocument();
    expect(update).not.toHaveBeenCalled();
  });

  it("cancels unsaved changes", async () => {
    render(<JobSettingsBrowser />);
    const batch = await screen.findByRole("spinbutton", { name: "Read batch" });
    await userEvent.clear(batch);
    await userEvent.type(batch, "128");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(batch).toHaveValue(256);
    expect(update).not.toHaveBeenCalled();
  });

  it("disables every input until the authoritative Save response returns", async () => {
    let finish!: (reply: { value: SettingsValue }) => void;
    update.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
    render(<JobSettingsBrowser />);
    const batch = await screen.findByRole("spinbutton", { name: "Read batch" });
    await userEvent.clear(batch);
    await userEvent.type(batch, "128");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    for (const input of screen.getAllByRole("spinbutton")) expect(input).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    finish({ value: value(128) });
    await waitFor(() => expect(batch).toBeEnabled());
    expect(batch).toHaveValue(128);
  });

  it("keeps a failed Save draft editable and its error visible while retrying", async () => {
    let reject!: (error: Error) => void;
    update.mockImplementation(() => ({ response: new Promise((_, fail) => (reject = fail)) }));
    render(<JobSettingsBrowser />);
    const batch = await screen.findByRole("spinbutton", { name: "Read batch" });
    await userEvent.clear(batch);
    await userEvent.type(batch, "128");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    reject(new Error("Job Settings unavailable"));
    expect(await screen.findByText("Job Settings unavailable")).toBeVisible();
    expect(batch).toHaveValue(128);
    expect(batch).toBeEnabled();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByText("Job Settings unavailable")).toBeVisible();
    expect(batch).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reload saved settings" })).toBeDisabled();
    reject(new Error("Still unavailable"));
    expect(await screen.findByText("Still unavailable")).toBeVisible();
    expect(batch).toHaveValue(128);
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled();
  });
});
