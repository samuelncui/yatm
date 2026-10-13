import { act, screen, waitFor } from "@testing-library/react";
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
  it("uses shared loading feedback only until settings arrive and keeps saved values on revisit", async () => {
    let finish!: (reply: { value: SettingsValue }) => void;
    get.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
    const page = render(<JobSettingsBrowser />);
    expect(screen.getByRole("progressbar", { name: "Loading Job settings" })).toBeVisible();
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
    await act(async () => finish({ value: value() }));
    const batch = screen.getByRole("spinbutton", { name: "Read batch" });
    await userEvent.clear(batch);
    await userEvent.type(batch, "128");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByText("Job settings saved.");
    page.rerender(<></>);
    page.rerender(<JobSettingsBrowser />);
    expect(screen.getByRole("spinbutton", { name: "Read batch" })).toHaveValue(128);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    await act(async () => {});
    expect(get).toHaveBeenCalledOnce();
  });

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

it("retries a failed initial read without defaults and keeps its form while explicitly reloading", async () => {
  get.mockReturnValueOnce({ response: Promise.reject(new Error("Read unavailable")) });
  render(<JobSettingsBrowser />);
  expect(await screen.findByText("Read unavailable")).toBeVisible();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  let finish!: (reply: { value: SettingsValue }) => void;
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
  await userEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(screen.getAllByRole("progressbar")).toHaveLength(1);
  expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled();
  expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
  await act(async () => finish({ value: value() }));
  const batch = screen.getByRole("spinbutton", { name: "Read batch" });
  await userEvent.clear(batch);
  await userEvent.type(batch, "128");
  update.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Save unavailable")) }));
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  await screen.findByText("Save unavailable");
  get.mockReturnValueOnce({ response: new Promise((resolve) => (finish = resolve)) });
  await userEvent.click(screen.getByRole("button", { name: "Reload saved settings" }));
  expect(batch).toHaveValue(128);
  expect(batch).toBeDisabled();
  expect(screen.getAllByRole("progressbar")).toHaveLength(1);
  await act(async () => finish({ value: value(512) }));
  expect(batch).toHaveValue(512);
  expect(batch).toBeEnabled();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  expect(get).toHaveBeenCalledTimes(3);
});
