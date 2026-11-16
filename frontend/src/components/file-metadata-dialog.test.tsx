import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { FileOperationKind, FilesDetail, FileOperationRef } from "@/entity";

const { fileMetadataEdit, tagList, getDetail } = vi.hoisted(() => ({
  fileMetadataEdit: vi.fn(),
  tagList: vi.fn(),
  getDetail: vi.fn(),
}));

vi.mock("@/api", () => ({
  cli: { listTags: tagList },
  filesCli: {
    updateMetadata: fileMetadataEdit,
    get: (...args: unknown[]) => ({ response: getDetail(...args).response.then((detail: unknown) => ({ detail })) }),
  },
}));

import { FileMetadataDialog } from "@/components/file-metadata-dialog";

const call = <T,>(response: T) => ({ response: Promise.resolve(response) });

// The dialog reads each selected row's metadata from its fresh detail; listing rows carry none.
const details = new Map<bigint, FilesDetail>();
const libraryFile = (id: number, name: string, tags: string[] = [], note = "") => {
  const fileId = BigInt(id);
  details.set(
    fileId,
    FilesDetail.create({
      entry: { reference: { target: { oneofKind: "fileId", fileId } }, name, operations: [FileOperationKind.UPDATE_METADATA] },
      organization: { tags, note },
    }),
  );
  return { id: String(id), name };
};

beforeEach(() => {
  fileMetadataEdit.mockReset();
  tagList.mockReset();
  getDetail.mockReset();
  details.clear();
  getDetail.mockImplementation(({ reference }: { reference: FileOperationRef }) =>
    call(reference.target.oneofKind === "fileId" ? details.get(reference.target.fileId) : undefined),
  );
  fileMetadataEdit.mockReturnValue(call({}));
  tagList.mockReturnValue(call({ tags: [{ name: "archive", fileCount: 2n }], nextCursor: "" }));
});

describe("FileMetadataDialog", () => {
  it("keeps a failed save and its draft in the dialog, then reports refresh failure separately", async () => {
    fileMetadataEdit.mockImplementationOnce(() => ({ response: Promise.reject(new Error("Save unavailable")) }));
    const onClose = vi.fn();
    const onSaved = vi.fn().mockRejectedValue(new Error("Refresh unavailable"));
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt")]} onClose={onClose} onSaved={onSaved} />);
    await userEvent.type(await screen.findByRole("textbox", { name: "Note" }), "Retain this draft");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Save unavailable");
    expect(screen.getByRole("textbox", { name: "Note" })).toHaveValue("Retain this draft");
    expect(onClose).not.toHaveBeenCalled();
    expect(onSaved).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce());
    expect(onSaved).toHaveBeenCalledOnce();
    expect(fileMetadataEdit).toHaveBeenCalledTimes(2);
  });

  it("searches Tags on the server and loads further pages without a total cap", async () => {
    tagList.mockImplementation(({ prefix, cursor }: { prefix: string; cursor?: string }) =>
      call(
        prefix
          ? { tags: [{ name: "alpha", fileCount: 1n }], nextCursor: "" }
          : cursor === "more"
            ? { tags: [{ name: "tag-100", fileCount: 1n }], nextCursor: "" }
            : { tags: Array.from({ length: 100 }, (_, index) => ({ name: `tag-${index}`, fileCount: 1n })), nextCursor: "more" },
      ),
    );
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt")]} onClose={() => {}} onSaved={async () => {}} />);

    await userEvent.click(await screen.findByRole("combobox", { name: "Tags" }));
    await waitFor(() => expect(tagList).toHaveBeenCalledWith({ prefix: "" }));
    await userEvent.click(await screen.findByRole("button", { name: "Load more tags" }));
    await waitFor(() => expect(tagList).toHaveBeenCalledWith({ prefix: "", cursor: "more" }));
    expect(await screen.findByRole("option", { name: "tag-100" })).toBeInTheDocument();
    await userEvent.type(screen.getByRole("combobox", { name: "Tags" }), "al");
    await waitFor(() => expect(tagList).toHaveBeenCalledWith({ prefix: "al" }));
    await userEvent.click(await screen.findByRole("option", { name: "alpha" }));
    expect(screen.getByText("alpha")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled();
    expect(screen.queryByText(/first 50/i)).not.toBeInTheDocument();
  });

  it("ignores a previous query's late page after the search changes", async () => {
    let finishOldPage: (reply: { tags: { name: string; fileCount: bigint }[]; nextCursor: string }) => void = () => {};
    const oldPage = new Promise<{ tags: { name: string; fileCount: bigint }[]; nextCursor: string }>((resolve) => {
      finishOldPage = resolve;
    });
    tagList.mockImplementation(({ prefix, cursor }: { prefix: string; cursor?: string }) => {
      if (prefix === "a" && cursor === "next") return { response: oldPage };
      if (prefix === "a") return call({ tags: [{ name: "alpha", fileCount: 1n }], nextCursor: "next" });
      if (prefix === "b") return call({ tags: [{ name: "beta", fileCount: 1n }], nextCursor: "" });
      return call({ tags: [], nextCursor: "" });
    });
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt")]} onClose={() => {}} onSaved={async () => {}} />);

    const input = await screen.findByRole("combobox", { name: "Tags" });
    await userEvent.type(input, "a");
    expect(await screen.findByRole("option", { name: "alpha" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Load more tags" }));
    await waitFor(() => expect(tagList).toHaveBeenCalledWith({ prefix: "a", cursor: "next" }));
    await userEvent.clear(input);
    await userEvent.type(input, "b");
    expect(await screen.findByRole("option", { name: "beta" })).toBeInTheDocument();

    finishOldPage({ tags: [{ name: "aardvark", fileCount: 1n }], nextCursor: "" });
    expect(screen.queryByRole("option", { name: "aardvark" })).not.toBeInTheDocument();
  });

  it("uses the freshly read Location reference and preserves its tags and note", async () => {
    const original = FileOperationRef.create({
      target: { oneofKind: "location", location: { locationId: 4n, path: "image.jpg", facts: { sizeBytes: 1n, mode: 420, mtimeNs: 1n } } },
    });
    const fresh = FileOperationRef.clone(original);
    if (fresh.target.oneofKind === "location") fresh.target.location.facts = { sizeBytes: 1n, mode: 420, mtimeNs: 2n };
    getDetail.mockReturnValue(
      call(
        FilesDetail.create({
          entry: { reference: fresh, name: "image.jpg", operations: [FileOperationKind.UPDATE_METADATA] },
          organization: { tags: ["retained"], note: "existing note" },
        }),
      ),
    );
    render(
      <FileMetadataDialog
        open
        files={[{ id: "location-file:4:image.jpg", name: "image.jpg", operationReference: original }]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );
    expect(await screen.findByRole("textbox", { name: "Note" })).toHaveValue("existing note");
    await userEvent.type(screen.getByRole("combobox", { name: "Tags" }), "added{enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(fileMetadataEdit).toHaveBeenCalledWith({ references: [fresh], addTags: ["added"], removeTags: [], note: undefined });
    expect(getDetail).toHaveBeenCalledExactlyOnceWith({ reference: original });
  });

  it("does not expose an editor when the fresh detail disallows metadata changes", async () => {
    getDetail.mockReturnValue(call(FilesDetail.create({ entry: { reference: { target: { oneofKind: "fileId", fileId: 7n } }, name: "locked" } })));
    render(<FileMetadataDialog open files={[{ id: "7", name: "locked" }]} onClose={() => {}} onSaved={async () => {}} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Metadata editing is unavailable");
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
    expect(fileMetadataEdit).not.toHaveBeenCalled();
  });

  it("replaces one File's tags and note", async () => {
    const events: string[] = [];
    const onClose = vi.fn(() => events.push("close"));
    const onSaved = vi.fn(async () => {
      events.push("refresh");
    });
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt", ["archive"], "old")]} onClose={onClose} onSaved={onSaved} />);

    expect(screen.queryByText(/Organize files with searchable|Use tags to group|Add searchable context|Changes apply to this file/)).not.toBeInTheDocument();
    expect(await screen.findByText("Leave empty to clear the note.")).toBeInTheDocument();
    const tags = await screen.findByRole("combobox", { name: "Tags" });
    await userEvent.type(tags, "Favorite{enter}");
    const note = screen.getByRole("textbox", { name: "Note" });
    await userEvent.clear(note);
    await userEvent.type(note, "new note");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(fileMetadataEdit).toHaveBeenCalledWith({
        references: [{ target: { oneofKind: "fileId", fileId: 7n } }],
        addTags: ["favorite"],
        removeTags: [],
        note: "new note",
      }),
    );
    expect(onSaved).toHaveBeenCalled();
    expect(events).toEqual(["close", "refresh"]);
  });

  it("adds one tag to multiple Files without changing their notes", async () => {
    render(
      <FileMetadataDialog open files={[libraryFile(7, "one", [], "one"), libraryFile(8, "two", [], "two")]} onClose={() => {}} onSaved={async () => {}} />,
    );

    const tags = await screen.findByRole("combobox", { name: "Tags" });
    await userEvent.type(tags, "favorite{enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(fileMetadataEdit).toHaveBeenCalledWith({
        references: [7n, 8n].map((fileId) => ({ target: { oneofKind: "fileId", fileId } })),
        addTags: ["favorite"],
        removeTags: [],
        note: undefined,
      }),
    );
  });

  it("calculates bulk additions and removals from the edited shared tags", async () => {
    render(
      <FileMetadataDialog
        open
        files={[libraryFile(7, "one", ["archive", "one"], "one"), libraryFile(8, "two", ["archive", "two"], "two")]}
        onClose={() => {}}
        onSaved={async () => {}}
      />,
    );

    const tags = await screen.findByRole("combobox", { name: "Tags" });
    await userEvent.click(tags);
    await userEvent.keyboard("{Backspace}");
    await userEvent.type(tags, "favorite{enter}");
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(fileMetadataEdit).toHaveBeenCalledWith({
        references: [7n, 8n].map((fileId) => ({ target: { oneofKind: "fileId", fileId } })),
        addTags: ["favorite"],
        removeTags: ["archive"],
        note: undefined,
      }),
    );
  });

  it("keeps bulk note editing opt-in", async () => {
    render(
      <FileMetadataDialog open files={[libraryFile(7, "one", [], "one"), libraryFile(8, "two", [], "two")]} onClose={() => {}} onSaved={async () => {}} />,
    );

    expect(await screen.findByText("2 selected files")).toBeInTheDocument();
    const noteSection = screen.getByText("Note", { selector: "strong" }).closest("section");
    expect(noteSection).not.toBeNull();
    const note = within(noteSection!).getByRole("textbox", { name: "Note" });
    expect(note).toBeDisabled();

    await userEvent.click(within(noteSection!).getByRole("switch", { name: "Edit" }));
    expect(note).toBeEnabled();
  });

  it("does not treat a Tag search query as an unsaved edit", async () => {
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt", ["archive"], "old")]} onClose={() => {}} onSaved={async () => {}} />);

    expect(await screen.findByText("No changes to save")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
    await userEvent.type(screen.getByRole("combobox", { name: "Tags" }), "search-only");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
    expect(fileMetadataEdit).not.toHaveBeenCalled();
  });

  it("limits each request to 16 added tags", async () => {
    render(<FileMetadataDialog open files={[libraryFile(7, "file.txt")]} onClose={() => {}} onSaved={async () => {}} />);

    const tags = await screen.findByRole("combobox", { name: "Tags" });
    for (let index = 0; index < 17; index += 1) {
      await userEvent.type(tags, `tag-${index}{enter}`);
    }

    expect(screen.getByText("You can add at most 16 tags at once.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  }, 15000);

  it("starts with fresh editor state whenever the dialog is reopened", async () => {
    const files = [libraryFile(7, "file.txt", ["archive"], "old")];
    const { rerender } = render(<FileMetadataDialog open files={files} onClose={() => {}} onSaved={async () => {}} />);

    const note = await screen.findByRole("textbox", { name: "Note" });
    await userEvent.clear(note);
    await userEvent.type(note, "unsaved");
    expect(note).toHaveValue("unsaved");

    rerender(<FileMetadataDialog open={false} files={files} onClose={() => {}} onSaved={async () => {}} />);
    rerender(<FileMetadataDialog open files={files} onClose={() => {}} onSaved={async () => {}} />);

    expect(await screen.findByRole("textbox", { name: "Note" })).toHaveValue("old");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });
});
