import { type ReactNode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChonkyActions, FileBrowser, FileList, type FileArray } from "@samuelncui/chonky";

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

const Browser = ({ files }: { files: FileArray }) => (
  <FileBrowser files={files} disableDragAndDrop defaultFileViewActionId={ChonkyActions.EnableListView.id}>
    <FileList />
  </FileBrowser>
);
const files = [
  { id: "1", name: "Records", isDir: true },
  { id: "2", name: "retention-policy.txt", details: ["Research/retention-policy.txt"] },
];

describe("Chonky list row heights", () => {
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
