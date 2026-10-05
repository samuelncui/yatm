import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileVersion, SelectionInspectionResult, PreviewAvailability, type ListFileVersionsResponse } from "@/entity";
import { ChooseVersionDialog } from "./version-picker";

const { list, detail, inspect } = vi.hoisted(() => ({ list: vi.fn(), detail: vi.fn(), inspect: vi.fn() }));
vi.mock("@/api", () => ({
  filesCli: { listVersions: list },
  previewCli: { get: detail },
  restoreJobCli: { estimate: (...args: unknown[]) => ({ response: inspect(...args).response.then((result: unknown) => ({ result })) }) },
  fileBase: "/files",
}));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.resetAllMocks();
  list.mockReturnValue(call({ versions: [FileVersion.create({ id: 1n, fileId: 7n, sizeBytes: 100n, signature: new Uint8Array([1]) })], hasMore: false }));
  detail.mockReturnValue(call({}));
  inspect.mockReturnValue(call(SelectionInspectionResult.create({ fileCount: 1n })));
});

describe("Restore version picker", () => {
  it("keeps saved-copy availability independent of Preview failure and retries Preview alone", async () => {
    detail.mockReturnValue({ response: Promise.reject(new Error("Preview offline")) });
    inspect.mockReturnValue(call(SelectionInspectionResult.create({ missingCopyCount: 1n })));
    render(
      <ChooseVersionDialog
        file={{ id: "7", name: "photo.jpg" }}
        selectedVersion={FileVersion.create({ id: 1n, fileId: 7n, signature: new Uint8Array([1]) })}
        onChoose={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(await screen.findByText("No usable archived copy.")).toBeInTheDocument();
    expect(await screen.findByText("Preview offline")).toBeInTheDocument();
    // Retry re-reads Preview alone: the saved-copy check is not repeated.
    const reads = detail.mock.calls.length;
    detail.mockReturnValue(call({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url: "/recovered" }] }));
    await userEvent.click(screen.getByRole("button", { name: "Retry Preview" }));
    await waitFor(() => expect(screen.queryByText("Preview offline")).not.toBeInTheDocument());
    expect(detail.mock.calls.length).toBeGreaterThan(reads);
    expect(inspect).toHaveBeenCalledTimes(1);
  });

  it("keeps selection controlled until Choose, displaying an old selected version outside the first page", async () => {
    const choose = vi.fn().mockResolvedValue(undefined);
    const close = vi.fn();
    render(
      <ChooseVersionDialog
        file={{ id: "7", name: "photo.jpg" }}
        selectedVersion={FileVersion.create({ id: 20n, fileId: 7n })}
        onChoose={choose}
        onClose={close}
      />,
    );
    expect(screen.getByRole("button", { name: /Version #20/ })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(await screen.findByRole("button", { name: /Version #1$/ }));
    expect(choose).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Choose version" }));
    await waitFor(() => expect(close).toHaveBeenCalledOnce());
    expect(choose).toHaveBeenCalledWith(expect.objectContaining({ id: 1n }));
  });
  it("remains dismissible during loading and does not apply a late response", async () => {
    let complete!: (reply: ListFileVersionsResponse) => void;
    list.mockReturnValue({
      response: new Promise<ListFileVersionsResponse>((resolve) => {
        complete = resolve;
      }),
    });
    const close = vi.fn();
    const choose = vi.fn();
    const page = render(<ChooseVersionDialog file={{ id: "7", name: "photo.jpg" }} onChoose={choose} onClose={close} />);
    const signal = list.mock.calls[0][1].abort as AbortSignal;
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(close).toHaveBeenCalledOnce();
    page.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => complete({ versions: [FileVersion.create({ id: 1n })], hasMore: false }));
    expect(choose).not.toHaveBeenCalled();
  });
  it("discards a previous file's late version list", async () => {
    let complete!: (reply: ListFileVersionsResponse) => void;
    list.mockImplementation(({ fileId }) =>
      fileId === 7n
        ? {
            response: new Promise<ListFileVersionsResponse>((resolve) => {
              complete = resolve;
            }),
          }
        : call({ versions: [FileVersion.create({ id: 2n, fileId: 8n })], hasMore: false }),
    );
    const page = render(<ChooseVersionDialog file={{ id: "7", name: "first" }} onChoose={vi.fn()} onClose={vi.fn()} />);
    const signal = list.mock.calls[0][1].abort as AbortSignal;
    page.rerender(<ChooseVersionDialog file={{ id: "8", name: "second" }} onChoose={vi.fn()} onClose={vi.fn()} />);
    expect(signal.aborted).toBe(true);
    await screen.findByRole("button", { name: /Version #2/ });
    await act(async () => complete({ versions: [FileVersion.create({ id: 1n })], hasMore: false }));
    expect(screen.queryByRole("button", { name: /Version #1/ })).not.toBeInTheDocument();
  });
  it("shows the selected version's preview, unavailable copy and explicit after-time warning", async () => {
    list.mockReturnValue(
      call({ versions: [FileVersion.create({ id: 1n, fileId: 7n, firstArchivedAtNs: 1700000000000000300n, signature: new Uint8Array([1]) })], hasMore: false }),
    );
    detail.mockReturnValue(call({ availability: PreviewAvailability.READY, assets: [{ role: "thumbnail", url: "/files/previews/7/thumbnail?version_id=1" }] }));
    inspect.mockReturnValue(call(SelectionInspectionResult.create({ fileCount: 1n, missingCopyCount: 1n })));
    const page = render(
      <ChooseVersionDialog
        file={{ id: "7", name: "photo.jpg" }}
        selectedVersion={FileVersion.create({ id: 1n, fileId: 7n, firstArchivedAtNs: 1700000000000000300n, signature: new Uint8Array([1]) })}
        cutoff={1700000000000000200n}
        onChoose={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(await screen.findByText("No usable archived copy.")).toBeInTheDocument();
    expect(screen.getByText("This custom version was saved after the selected time.")).toBeInTheDocument();
    await waitFor(() => expect(page.container.parentElement?.querySelector("img")).toHaveAttribute("src", "/files/previews/7/thumbnail?version_id=1"));
  });
  it("clears stale availability and load errors when recovery options change", async () => {
    const version = FileVersion.create({ id: 1n, fileId: 7n });
    inspect.mockReturnValueOnce(call(SelectionInspectionResult.create({ missingCopyCount: 1n })));
    const props = { file: { id: "7", name: "photo.jpg" }, selectedVersion: version, onChoose: vi.fn(), onClose: vi.fn() };
    const page = render(<ChooseVersionDialog {...props} />);
    await screen.findByText("No usable archived copy.");
    inspect.mockReturnValueOnce({ response: Promise.reject(new Error("Check failed")) });
    page.rerender(<ChooseVersionDialog {...props} allowDamagedCopies />);
    await screen.findByText(/Check failed/);
    expect(screen.queryByText("No usable archived copy.")).not.toBeInTheDocument();
    page.rerender(<ChooseVersionDialog {...props} />);
    await waitFor(() => expect(screen.queryByText(/Check failed/)).not.toBeInTheDocument());
    expect(screen.queryByText("No usable archived copy.")).not.toBeInTheDocument();
  });

  it("keeps loaded choices and the local selection on a failed page, and retries from the first page", async () => {
    const first = FileVersion.create({ id: 1n, fileId: 7n });
    const selected = FileVersion.create({ id: 20n, fileId: 7n });
    list.mockReturnValueOnce(call({ versions: [first], hasMore: true }));
    const choose = vi.fn().mockResolvedValue(undefined);
    render(<ChooseVersionDialog file={{ id: "7", name: "Photo" }} selectedVersion={selected} onChoose={choose} onClose={vi.fn()} />);
    await screen.findByRole("button", { name: /Version #1$/ });
    let fail!: (error: Error) => void;
    list.mockReturnValueOnce({
      response: new Promise((_, reject) => {
        fail = reject;
      }),
    });
    await userEvent.click(screen.getByRole("button", { name: "More versions" }));
    expect(list.mock.calls[1][0]).toEqual({ fileId: 7n, afterId: 1n, limit: 20 });
    await act(async () => fail(new Error("Page unavailable")));
    await screen.findByText("Page unavailable");
    expect(screen.getByRole("button", { name: /Version #1$/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Version #20/ })).toHaveAttribute("aria-pressed", "true");
    list.mockReturnValueOnce(call({ versions: [first], hasMore: false }));
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByText("Page unavailable")).not.toBeInTheDocument());
    expect(list.mock.calls[2][0]).toEqual({ fileId: 7n, afterId: 0n, limit: 20 });
    await userEvent.click(screen.getByRole("button", { name: "Choose version" }));
    expect(choose).toHaveBeenCalledWith(selected);
  });
});
