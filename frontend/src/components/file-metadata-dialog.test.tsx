import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { fileMetadataEdit, tagList } = vi.hoisted(() => ({
  fileMetadataEdit: vi.fn(),
  tagList: vi.fn(),
}));

vi.mock("@/api", () => ({ cli: { fileMetadataEdit, tagList } }));

import { FileMetadataDialog } from "@/components/file-metadata-dialog";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });

beforeEach(() => {
  fileMetadataEdit.mockReset();
  tagList.mockReset();
  fileMetadataEdit.mockReturnValue(call({}));
  tagList.mockReturnValue(call({ tags: [{ name: "archive", fileCount: 2n }], nextCursor: "" }));
});

describe("FileMetadataDialog", () => {
  it("replaces one File's tags and note", async () => {
    const events: string[] = [];
    const onClose = vi.fn(() => events.push("close"));
    const onSaved = vi.fn(async () => {
      events.push("refresh");
    });
    render(
      <FileMetadataDialog
        open
        files={[{ id: "7", name: "file.txt", parentId: "0", tags: ["archive"], note: "old", detailsAvailable: true }]}
        onClose={onClose}
        onSaved={onSaved}
      />,
    );

    expect(screen.queryByText(/Organize files with searchable|Use tags to group|Add searchable context|Changes apply to this file/)).not.toBeInTheDocument();
    expect(screen.getByText("Leave empty to clear the note.")).toBeInTheDocument();
    const tags = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.type(tags, "Favorite{enter}");
    const note = screen.getByRole("textbox", { name: "Note" });
    await userEvent.clear(note);
    await userEvent.type(note, "new note");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(fileMetadataEdit).toHaveBeenCalledWith({ ids: [7n], addTags: ["favorite"], removeTags: [], note: "new note" }));
    expect(onSaved).toHaveBeenCalled();
    expect(events).toEqual(["close", "refresh"]);
  });

  it("adds one tag to multiple Files without changing their notes", async () => {
    render(
      <FileMetadataDialog
        open
        files={[
          { id: "7", name: "one", parentId: "0", tags: [], note: "one", detailsAvailable: true },
          { id: "8", name: "two", parentId: "0", tags: [], note: "two", detailsAvailable: true },
        ]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    const tags = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.type(tags, "favorite{enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(fileMetadataEdit).toHaveBeenCalledWith({ ids: [7n, 8n], addTags: ["favorite"], removeTags: [], note: undefined }));
  });

  it("calculates bulk additions and removals from the edited shared tags", async () => {
    render(
      <FileMetadataDialog
        open
        files={[
          { id: "7", name: "one", parentId: "0", tags: ["archive", "one"], note: "one", detailsAvailable: true },
          { id: "8", name: "two", parentId: "0", tags: ["archive", "two"], note: "two", detailsAvailable: true },
        ]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    const tags = screen.getByRole("combobox", { name: "Tags" });
    await userEvent.click(tags);
    await userEvent.keyboard("{Backspace}");
    await userEvent.type(tags, "favorite{enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(fileMetadataEdit).toHaveBeenCalledWith({
        ids: [7n, 8n],
        addTags: ["favorite"],
        removeTags: ["archive"],
        note: undefined,
      }),
    );
  });

  it("keeps bulk note editing opt-in", async () => {
    render(
      <FileMetadataDialog
        open
        files={[
          { id: "7", name: "one", parentId: "0", tags: [], note: "one", detailsAvailable: true },
          { id: "8", name: "two", parentId: "0", tags: [], note: "two", detailsAvailable: true },
        ]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    expect(screen.getByText("2 selected files")).toBeInTheDocument();
    const noteSection = screen.getByText("Note", { selector: "strong" }).closest("section");
    expect(noteSection).not.toBeNull();
    const note = within(noteSection!).getByRole("textbox", { name: "Note" });
    expect(note).toBeDisabled();

    await userEvent.click(within(noteSection!).getByRole("switch", { name: "Edit" }));
    expect(note).toBeEnabled();
  });

  it("does not submit unchanged single-file metadata", () => {
    render(
      <FileMetadataDialog
        open
        files={[{ id: "7", name: "file.txt", parentId: "0", tags: ["archive"], note: "old", detailsAvailable: true }]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    expect(screen.getByText("No changes to save")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });

  it("limits each request to 16 added tags", async () => {
    render(
      <FileMetadataDialog
        open
        files={[{ id: "7", name: "file.txt", parentId: "0", tags: [], note: "", detailsAvailable: true }]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    const tags = screen.getByRole("combobox", { name: "Tags" });
    for (let index = 0; index < 17; index += 1) {
      await userEvent.type(tags, `tag-${index}{enter}`);
    }

    expect(screen.getByText("You can add at most 16 tags at once.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });

  it("starts with fresh editor state whenever the dialog is reopened", async () => {
    const files = [{ id: "7", name: "file.txt", parentId: "0", tags: ["archive"], note: "old", detailsAvailable: true as const }];
    const { rerender } = render(<FileMetadataDialog open files={files} onClose={() => {}} onSaved={async () => {}} />);

    const note = screen.getByRole("textbox", { name: "Note" });
    await userEvent.clear(note);
    await userEvent.type(note, "unsaved");
    expect(note).toHaveValue("unsaved");

    rerender(<FileMetadataDialog open={false} files={files} onClose={() => {}} onSaved={async () => {}} />);
    rerender(<FileMetadataDialog open files={files} onClose={() => {}} onSaved={async () => {}} />);

    expect(screen.getByRole("textbox", { name: "Note" })).toHaveValue("old");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });
});
