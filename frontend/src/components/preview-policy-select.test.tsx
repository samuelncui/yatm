import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { PreviewSettings } from "@/entity";
import { AppStateProvider } from "@/state/react";
import { usePreviewGeneration } from "./preview-policy-select";

const { capabilities, settings } = vi.hoisted(() => ({ capabilities: vi.fn(), settings: vi.fn() }));
vi.mock("@/api", () => ({ previewCli: { getCapabilities: capabilities }, settingsCli: { get: settings } }));
beforeEach(() => vi.resetAllMocks());

it("defers capability reads until enabled and rejects an obsolete failure after reactivation", async () => {
  const draft = PreviewSettings.create({ enabled: true });
  let reject!: (error: Error) => void;
  capabilities.mockReturnValueOnce({
    response: new Promise((_, fail) => {
      reject = fail;
    }),
  });
  const view = renderHook(({ active }) => usePreviewGeneration(active, draft), { initialProps: { active: false }, wrapper: AppStateProvider });
  expect(capabilities).not.toHaveBeenCalled();
  expect(settings).not.toHaveBeenCalled();
  view.rerender({ active: true });
  const oldSignal = capabilities.mock.calls[0][1].abort as AbortSignal;
  view.rerender({ active: false });
  expect(oldSignal.aborted).toBe(true);
  capabilities.mockReturnValueOnce({ response: Promise.resolve({ available: true }) });
  view.rerender({ active: true });
  await waitFor(() => expect(view.result.current.available).toBe(true));
  await act(async () => reject(new Error("Obsolete capability failure")));
  expect(view.result.current.error).toBe("");
  expect(view.result.current.available).toBe(true);
  const signal = capabilities.mock.calls[1][1].abort as AbortSignal;
  view.unmount();
  expect(signal.aborted).toBe(true);
});

it("uses a supplied Settings draft without committing it or rechecking capabilities on edits", async () => {
  capabilities.mockReturnValue({ response: Promise.resolve({ available: true }) });
  const view = renderHook(({ draft }) => usePreviewGeneration(true, draft), {
    initialProps: { draft: PreviewSettings.create({ enabled: false }) },
    wrapper: AppStateProvider,
  });
  await waitFor(() => expect(view.result.current.loading).toBe(false));
  expect(view.result.current.reason).toBe("Preview generation is disabled.");
  view.rerender({ draft: PreviewSettings.create({ enabled: true }) });
  expect(view.result.current.available).toBe(true);
  expect(capabilities).toHaveBeenCalledOnce();
  expect(settings).not.toHaveBeenCalled();
});
