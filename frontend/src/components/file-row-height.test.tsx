import { type ReactNode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { FileList, type FileArray } from "@samuelncui/chonky";
import { FileBrowser } from "./file-browser";
import { EntryKind, FilesEntry } from "@/entity";
import { filesEntryData } from "./files-browser";

// Check the shared row and sizing contract without inventing jsdom layout measurements.
vi.mock("react-virtuoso", () => ({
  Virtuoso: ({
    totalCount,
    itemContent,
    fixedItemHeight,
    defaultItemHeight,
  }: {
    totalCount: number;
    itemContent: (index: number) => ReactNode;
    fixedItemHeight?: number;
    defaultItemHeight?: number;
  }) => (
    <div data-testid="virtual-list" data-fixed-height={fixedItemHeight} data-default-height={defaultItemHeight}>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  ),
}));

const Browser = ({ files, loading }: { files: FileArray; loading?: "initial" | "refreshing" | "more" }) => (
  <FileBrowser files={files} disableDragAndDrop>
    <FileList loading={loading} />
  </FileBrowser>
);
const files = [
  { id: "1", name: "Records", isDir: true },
  { id: "2", name: "retention-policy.txt", details: ["Research/retention-policy.txt"] },
];

describe("Chonky panel loading", () => {
  it("switches a completed initial load directly to supplied rows and reserves empty for an empty result", () => {
    const { rerender } = render(<Browser files={[]} loading="initial" />);
    rerender(<Browser files={files} />);
    expect(screen.getByText("Records")).toBeInTheDocument();
    expect(screen.queryByText("Nothing to show")).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    rerender(<Browser files={[]} loading="initial" />);
    expect(screen.queryByText("Nothing to show")).not.toBeInTheDocument();
    rerender(<Browser files={[]} />);
    expect(screen.getByText("Nothing to show")).toBeInTheDocument();
  });
  it("represents initial loading independently of entries and the empty placeholder", () => {
    render(<Browser files={[]} loading="initial" />);
    expect(screen.getByRole("list")).toHaveAttribute("aria-busy", "true");
    expect(screen.getAllByRole("progressbar")).toHaveLength(1);
    expect(screen.queryByTestId("virtual-list")).not.toBeInTheDocument();
    expect(screen.queryByText("Nothing to show")).not.toBeInTheDocument();
  });
  it.each(["refreshing", "more"] as const)("keeps real rows mounted while %s", async (loading) => {
    const { rerender } = render(<Browser files={files} />);
    const row = await screen.findByText("Records");
    rerender(<Browser files={files} loading={loading} />);
    expect(screen.getByText("Records")).toBe(row);
    expect(screen.getAllByRole("progressbar")).toHaveLength(1);
    rerender(<Browser files={files} />);
    expect(screen.getByText("Records")).toBe(row);
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });
});

describe("Chonky list row heights", () => {
  it("shows per-entry errors beside usable rows using the shared row details", async () => {
    const failed = filesEntryData(FilesEntry.create({ name: "unreadable", kind: EntryKind.FILE, error: "Permission denied" }));
    render(<Browser files={[files[0], failed]} />);
    expect(await screen.findByText("Records")).toBeInTheDocument();
    expect(screen.getByText("unreadable")).toBeInTheDocument();
    expect(screen.getByText("Permission denied")).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "Permission denied" })).toBeInTheDocument();
    expect(screen.getByTestId("virtual-list")).not.toHaveAttribute("data-fixed-height");
  });

  it("measures multiline rows and remeasures updated details without a fixed height", async () => {
    const { rerender } = render(<Browser files={files} />);
    const detail = await screen.findByText("Research/retention-policy.txt");
    const row = detail.closest(".chonky-fileEntryClickableWrapper")!.parentElement!;
    expect(screen.getByTestId("virtual-list")).not.toHaveAttribute("data-fixed-height");
    expect(screen.getByTestId("virtual-list")).toHaveAttribute("data-default-height", "30");
    expect(row.style.height).toBe("");
    expect(row).toHaveStyle({ minHeight: "30px" });

    rerender(<Browser files={[files[0], { ...files[1], details: [...files[1].details!, "Saved Sep 10, 2026", "Archive: Policies/retention.txt"] }]} />);
    expect(await screen.findByText("Archive: Policies/retention.txt")).toBeInTheDocument();
    expect(screen.getByTestId("virtual-list")).not.toHaveAttribute("data-fixed-height");
    expect(row.style.height).toBe("");

    rerender(<Browser files={files.map((file) => ({ ...file, details: [] }))} />);
    await waitFor(() => expect(screen.getByTestId("virtual-list")).toHaveAttribute("data-fixed-height", "30"));
    expect(screen.queryByText("Saved Sep 10, 2026")).not.toBeInTheDocument();
  });

  it("retains compact fixed-height virtualization for ordinary single-line files", async () => {
    render(<Browser files={[files[0]]} />);
    const name = await screen.findByText("Records");
    expect(screen.getByTestId("virtual-list")).toHaveAttribute("data-fixed-height", "30");
    expect(name.closest(".chonky-fileEntryClickableWrapper")!.parentElement).toHaveStyle({ height: "30px" });
  });
});
