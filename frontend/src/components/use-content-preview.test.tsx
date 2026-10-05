import { act, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { FileVersion, GetPreviewResponse, PreviewAvailability } from "@/entity";
import { ContentPreview } from "./file-content";
import { ChooseVersionDialog } from "./version-picker";

const { preview, versions, estimate } = vi.hoisted(() => ({ preview: vi.fn(), versions: vi.fn(), estimate: vi.fn() }));
vi.mock("@/api", async (original) => ({
  ...(await original<typeof import("@/api")>()),
  previewCli: { get: preview },
  filesCli: { listVersions: versions },
  restoreJobCli: { estimate },
}));

const ready = (url: string) => GetPreviewResponse.create({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url }] });
const showPreview = (consumer: "detail" | "picker", signature: Uint8Array) =>
  consumer === "detail" ? (
    <ContentPreview signature={signature} />
  ) : (
    <ChooseVersionDialog
      file={{ id: "7", name: "Photo" }}
      selectedVersion={FileVersion.create({ id: BigInt(signature[0] ?? 0), fileId: 7n, signature })}
      onChoose={vi.fn()}
      onClose={vi.fn()}
    />
  );

beforeEach(() => {
  vi.resetAllMocks();
  versions.mockReturnValue({ response: Promise.resolve({ versions: [], hasMore: false }) });
  estimate.mockReturnValue({ response: Promise.resolve({ result: { missingCopyCount: 0n } }) });
});

it.each([
  ["detail", false],
  ["picker", false],
  ["detail", true],
  ["picker", true],
] as const)("%s aborts a replaced Preview and ignores its late reply (failure: %s)", async (consumer, failed) => {
  let complete!: (reply: unknown) => void;
  let fail!: (error: Error) => void;
  preview.mockReturnValueOnce({
    response: new Promise((resolve, reject) => {
      complete = resolve;
      fail = reject;
    }),
  });
  const view = render(showPreview(consumer, new Uint8Array([1])));
  const oldSignal = preview.mock.calls[0][1].abort as AbortSignal;
  preview.mockReturnValue({ response: Promise.resolve(ready("/current")) });
  view.rerender(showPreview(consumer, new Uint8Array([2])));
  expect(oldSignal.aborted).toBe(true);
  await waitFor(() => expect(document.querySelector("img")).toHaveAttribute("src", "/current"));
  await act(async () => {
    if (failed) fail(new Error("Stale Preview failure"));
    else complete(ready("/stale"));
  });
  expect(document.querySelector("img")).toHaveAttribute("src", "/current");
  expect(screen.queryByRole("button", { name: "Retry Preview" })).not.toBeInTheDocument();
  const currentSignal = preview.mock.calls.at(-1)![1].abort as AbortSignal;
  view.unmount();
  expect(currentSignal.aborted).toBe(true);
});

it("keeps simultaneous detail and picker reads independent when one consumer closes", async () => {
  const responses: ((reply: unknown) => void)[] = [];
  preview.mockImplementation(() => ({ response: new Promise((resolve) => responses.push(resolve)) }));
  const signature = new Uint8Array([1]);
  const picker = showPreview("picker", signature);
  const view = render(
    <>
      <div key="detail">
        <ContentPreview signature={signature} />
      </div>
      <div key="picker">{picker}</div>
    </>,
  );
  await waitFor(() => expect(preview).toHaveBeenCalledTimes(2));
  const [detailSignal, pickerSignal] = preview.mock.calls.map(([, options]) => options.abort as AbortSignal);
  view.rerender(
    <>
      <div key="picker">{picker}</div>
    </>,
  );
  expect(detailSignal.aborted).toBe(true);
  expect(pickerSignal.aborted).toBe(false);
  await act(async () => {
    responses[0](ready("/closed"));
    responses[1](ready("/picker"));
  });
  await waitFor(() => expect(document.querySelector("img")).toHaveAttribute("src", "/picker"));
  expect(document.querySelectorAll("img")).toHaveLength(1);
});

it.each(["detail", "picker"] as const)("%s clears errors and skips the read when the signature becomes unknown", async (consumer) => {
  preview.mockReturnValue({ response: Promise.reject(new Error("Preview offline")) });
  const view = render(showPreview(consumer, new Uint8Array([1])));
  await screen.findByRole("button", { name: "Retry Preview" });
  view.rerender(showPreview(consumer, new Uint8Array()));
  expect(screen.queryByText("Preview offline")).not.toBeInTheDocument();
  expect(preview).toHaveBeenCalledOnce();
});

it("keeps a completed Preview for the same signature during refresh, then replaces its assets", async () => {
  const signature = new Uint8Array([1]);
  preview.mockReturnValueOnce({ response: Promise.resolve(ready("/complete")) });
  const view = render(<ContentPreview signature={signature} refresh={1} />);
  await waitFor(() => expect(document.querySelector("img")).toHaveAttribute("src", "/complete"));
  let complete!: (value: GetPreviewResponse) => void;
  preview.mockReturnValueOnce({
    response: new Promise((resolve) => {
      complete = resolve;
    }),
  });
  view.rerender(<ContentPreview signature={signature} refresh={2} />);
  expect(document.querySelector("img")).toHaveAttribute("src", "/complete");
  await act(async () => complete(ready("/refreshed")));
  expect(document.querySelector("img")).toHaveAttribute("src", "/refreshed");
});
