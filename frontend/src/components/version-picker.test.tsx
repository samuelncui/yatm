import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileVersion, InspectSelectionReply, PreviewManifest, type ListFileVersionsReply } from "@/entity";
import { ChooseVersionDialog } from "./version-picker";

const { list, detail, inspect } = vi.hoisted(() => ({ list: vi.fn(), detail: vi.fn(), inspect: vi.fn() }));
vi.mock("@/api", () => ({ fileCatalogCli: { listVersions: list, getVersion: detail, inspectSelection: inspect }, fileBase: "/files" }));
const call = (value: unknown) => ({ response: Promise.resolve(value) });
beforeEach(() => {
  vi.resetAllMocks();
  list.mockReturnValue(call({ versions: [FileVersion.create({ id: 1n, fileId: 7n, size: 100n })], hasMore: false }));
  detail.mockReturnValue(call({}));
  inspect.mockReturnValue(call(InspectSelectionReply.create({ files: 1n })));
});

describe("Restore version picker", () => {
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
    let complete!: (reply: ListFileVersionsReply) => void;
    list.mockReturnValue({
      response: new Promise<ListFileVersionsReply>((resolve) => {
        complete = resolve;
      }),
    });
    const close = vi.fn();
    const choose = vi.fn();
    const page = render(<ChooseVersionDialog file={{ id: "7", name: "photo.jpg" }} onChoose={choose} onClose={close} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(close).toHaveBeenCalledOnce();
    page.unmount();
    await act(async () => complete({ versions: [FileVersion.create({ id: 1n })], hasMore: false }));
    expect(choose).not.toHaveBeenCalled();
  });
  it("discards a previous file's late version list", async () => {
    let complete!: (reply: ListFileVersionsReply) => void;
    list.mockImplementation(({ fileId }) =>
      fileId === 7n
        ? {
            response: new Promise<ListFileVersionsReply>((resolve) => {
              complete = resolve;
            }),
          }
        : call({ versions: [FileVersion.create({ id: 2n, fileId: 8n })], hasMore: false }),
    );
    const page = render(<ChooseVersionDialog file={{ id: "7", name: "first" }} onChoose={vi.fn()} onClose={vi.fn()} />);
    page.rerender(<ChooseVersionDialog file={{ id: "8", name: "second" }} onChoose={vi.fn()} onClose={vi.fn()} />);
    await screen.findByRole("button", { name: /Version #2/ });
    await act(async () => complete({ versions: [FileVersion.create({ id: 1n })], hasMore: false }));
    expect(screen.queryByRole("button", { name: /Version #1/ })).not.toBeInTheDocument();
  });
  it("shows the selected version's preview, unavailable copy and explicit after-time warning", async () => {
    list.mockReturnValue(call({ versions: [FileVersion.create({ id: 1n, fileId: 7n, firstArchivedAtMs: 300n })], hasMore: false }));
    detail.mockReturnValue(call({ preview: PreviewManifest.create({ assets: [{ role: "thumbnail" }] }) }));
    inspect.mockReturnValue(call(InspectSelectionReply.create({ files: 1n, missingCopies: 1n })));
    const page = render(
      <ChooseVersionDialog
        file={{ id: "7", name: "photo.jpg" }}
        selectedVersion={FileVersion.create({ id: 1n, fileId: 7n, firstArchivedAtMs: 300n })}
        cutoff={200n}
        onChoose={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(await screen.findByText("No usable archived copy.")).toBeInTheDocument();
    expect(screen.getByText("This custom version was saved after the selected time.")).toBeInTheDocument();
    expect(page.container.parentElement?.querySelector("img")).toHaveAttribute("src", "/files/previews/7/thumbnail?version_id=1");
  });
  it("clears stale availability and load errors when recovery options change", async () => {
    const version = FileVersion.create({ id: 1n, fileId: 7n });
    inspect.mockReturnValueOnce(call(InspectSelectionReply.create({ missingCopies: 1n })));
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
});
