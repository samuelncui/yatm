import { type ReactNode, createRef } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ChonkyActions, FileBrowser, FileList, type FileBrowserHandle } from "@samuelncui/chonky";
import { convertFiles } from "@/api";
import { File } from "@/entity";
import { chonkyI18n } from "@/tools";

// Keep the actual shared row/selection implementation; jsdom has no virtual viewport.
vi.mock("react-virtuoso", () => {
  const Items = ({ totalCount, itemContent }: { totalCount: number; itemContent: (index: number) => ReactNode }) => (
    <div>
      {Array.from({ length: totalCount }, (_, index) => (
        <div key={index}>{itemContent(index)}</div>
      ))}
    </div>
  );
  return { Virtuoso: Items, VirtuosoGrid: Items };
});

describe("Shared file row status", () => {
  it("reserves the same trailing column for directories and exposes supplemental facts accessibly", async () => {
    const { container } = render(
      <FileBrowser
        files={[
          { id: "dir", name: "Photos", isDir: true },
          {
            id: "file",
            name: "photo.jpg",
            status: {
              label: "Local · Backed up · Changes not backed up",
              color: "green",
              marker: { kind: "changed", label: "Changes not backed up", color: "orange" },
            },
          },
        ]}
        disableDragAndDrop
        defaultFileViewActionId={ChonkyActions.EnableListView.id}
      >
        <FileList />
      </FileBrowser>,
    );
    const dot = await screen.findByRole("img", { name: /Changes not backed up/ });
    const slots = container.querySelectorAll("[data-chonky-status-slot]");
    expect(slots).toHaveLength(2);
    expect(slots[0]).toHaveAttribute("aria-hidden", "true");
    expect(slots[0]).toHaveStyle({ width: "28px" });
    expect(dot).toHaveStyle({ width: "28px" });
  });
  it("renders a trailing, focusable tooltip without replacing filename, properties or selection", async () => {
    const ref = createRef<FileBrowserHandle>();
    const files = convertFiles([
      File.create({
        id: 4n,
        name: "notes.txt",
        size: 12n,
        contentSummary: {
          hasOriginal: true,
          signatureKnown: true,
          currentObservationValid: true,
          originalAvailability: 1,
          restorableCurrentCopies: 1n,
          archivedCopies: 1n,
        },
      }),
    ]);
    render(
      <FileBrowser ref={ref} files={files} i18n={chonkyI18n} disableDragAndDrop defaultFileViewActionId={ChonkyActions.EnableListView.id}>
        <FileList />
      </FileBrowser>,
    );
    const dot = await screen.findByRole("img", { name: /Local file present · Current content backed up/ });
    const row = dot.parentElement!;
    expect(row.lastElementChild).toContainElement(dot);
    expect(row.textContent).toContain("notes");
    expect(row.textContent).toContain("12");
    await userEvent.hover(dot);
    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent(/^Local file present · Current content backed up$/);
    await waitFor(() => expect(tooltip).toHaveAttribute("data-popper-placement", "left"));
    await userEvent.click(dot);
    await waitFor(() => expect(ref.current?.getFileSelection()).toEqual(new Set(["4"])));
    expect(dot).toHaveAttribute("tabindex", "0");
  });
});
