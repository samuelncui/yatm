import { type ReactNode, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createTheme, ThemeProvider, useTheme } from "@mui/material/styles";
import { FileScope, FileSelection, FileVersion, InspectSelectionReply } from "@/entity";
import { SelectionWaitlist, type SelectionEntry } from "./selection-waitlist";
const { inform, filesPage, inspect } = vi.hoisted(() => ({ inform: vi.fn(), filesPage: vi.fn(), inspect: vi.fn() }));
vi.mock("react-toastify", () => ({ toast: { info: inform } }));
vi.mock("@/components/files-browser", async (original) => ({ ...(await original<typeof import("@/components/files-browser")>()), filesPage }));
vi.mock("@/api", async (original) => ({ ...(await original<typeof import("@/api")>()), fileCatalogCli: { inspectSelection: inspect } }));

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
const initial: SelectionEntry[] = [
  { key: "a", name: "one.jpg", path: "Trip/one.jpg", fileID: "1", version: FileVersion.create({ id: 11n, size: 10n }) },
  { key: "b", name: "two.jpg", path: "Other/two.jpg", fileID: "2", version: FileVersion.create({ id: 12n, size: 20n }) },
];
const folder: SelectionEntry = {
  key: "folder",
  name: "Trip",
  path: "Library/Trip",
  isDir: true,
  selection: FileSelection.create({ target: { oneofKind: "library", library: { fileId: 1n } }, scope: FileScope.SAVED }),
};
beforeEach(() => {
  filesPage.mockReset();
  inspect.mockReset();
  inspect.mockReturnValue({
    response: Promise.resolve(InspectSelectionReply.create({ resolvedVersions: [{ fileId: 8n, version: { id: 80n, fileId: 8n, size: 12n } }] })),
  });
});
const Harness = ({ choose, footer }: { choose: (entry: SelectionEntry) => void; footer?: ReactNode }) => {
  const [entries, setEntries] = useState(initial);
  return (
    <SelectionWaitlist
      kind="restore"
      label="Restore"
      entries={entries}
      busy={false}
      onChooseVersion={choose}
      footer={footer}
      onClear={() => setEntries([])}
      onRemove={(keys) => setEntries((entries) => entries.filter((entry) => !keys.has(entry.key)))}
    />
  );
};
describe("Chonky selection waitlist", () => {
  it("keeps footer controls inside one browser frame but outside file scrolling and context menus", async () => {
    render(
      <Harness
        choose={vi.fn()}
        footer={
          <label>
            Restore subdirectory
            <input aria-label="Restore subdirectory" />
          </label>
        }
      />,
    );
    const field = screen.getByRole("textbox", { name: "Restore subdirectory" });
    const list = (await screen.findByText("Trip/one.jpg")).closest('[role="list"]')!;
    expect(field.closest(".chonky-chonkyRoot")).toBe(list.closest(".chonky-chonkyRoot"));
    expect(field.closest(".chonky-browserFooter")).not.toBeNull();
    expect(list.closest(".chonky-browserBody")).not.toBeNull();
    expect(list).not.toContainElement(field);
    await userEvent.click(await screen.findByText("Trip/one.jpg"));
    await userEvent.type(field, "Recovered files");
    expect(field).toHaveValue("Recovered files");
    expect(screen.getByRole("button", { name: "Remove from list" })).toBeEnabled();
    expect(fireEvent.contextMenu(field)).toBe(true);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(fireEvent.contextMenu(screen.getByText("Trip/one.jpg"))).toBe(false);
    expect(await screen.findByRole("menu")).toBeInTheDocument();
  });

  it("does not reserve a footer or change the list container when no footer is provided", async () => {
    const { container } = render(<Harness choose={vi.fn()} />);
    const list = (await screen.findByText("Trip/one.jpg")).closest('[role="list"]')!;
    expect(container.querySelector(".chonky-browserFooter")).toBeNull();
    expect(list.parentElement).toHaveClass("chonky-chonkyRoot");
  });

  it("preserves the application theme for footer forms", () => {
    const FooterTheme = () => <span>Footer color: {useTheme().palette.primary.main}</span>;
    render(
      <ThemeProvider theme={createTheme({ palette: { primary: { main: "#123abc" } } })}>
        <Harness choose={vi.fn()} footer={<FooterTheme />} />
      </ThemeProvider>,
    );
    expect(screen.getByText("Footer color: #123abc")).toBeInTheDocument();
  });
  it("keeps normal multiselection, removes selections without file operations and clears the list", async () => {
    const user = userEvent.setup();
    render(<Harness choose={vi.fn()} />);
    await user.click(await screen.findByText("Trip/one.jpg"));
    await user.keyboard("{Control>}");
    await user.click(screen.getByText("Other/two.jpg"));
    await user.keyboard("{/Control}");
    await user.click(screen.getByRole("button", { name: "Remove from list" }));
    await waitFor(() => expect(screen.queryByText("Trip/one.jpg")).not.toBeInTheDocument());
    expect(screen.queryByText("Other/two.jpg")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Delete files/ })).not.toBeInTheDocument();
  });
  it("changes a selected version through the ordinary toolbar", async () => {
    const choose = vi.fn();
    render(<Harness choose={choose} />);
    await userEvent.click(await screen.findByText("Trip/one.jpg"));
    await userEvent.click(screen.getByRole("button", { name: "Change version" }));
    expect(choose).toHaveBeenCalledWith(initial[0], initial[0].version);
    await userEvent.click(screen.getByRole("button", { name: "Clear list" }));
    await waitFor(() => expect(screen.queryByText("Other/two.jpg")).not.toBeInTheDocument());
  });
  it("opens the version picker by double-clicking a regular waitlist file", async () => {
    const choose = vi.fn();
    render(<Harness choose={choose} />);
    await userEvent.dblClick(await screen.findByText("Trip/one.jpg"));
    expect(choose).toHaveBeenCalledWith(initial[0], initial[0].version);
  });
  it("explains that a version choice requires one file instead of silently doing nothing", async () => {
    const choose = vi.fn();
    const user = userEvent.setup();
    render(<Harness choose={choose} />);
    await user.click(await screen.findByText("Trip/one.jpg"));
    await user.keyboard("{Control>}");
    await user.click(screen.getByText("Other/two.jpg"));
    await user.keyboard("{/Control}");
    await user.click(screen.getByRole("button", { name: "Change version" }));
    expect(choose).not.toHaveBeenCalled();
    expect(inform).toHaveBeenCalledWith("Select one file to choose its version.");
  });

  it.each(["archive", "restore"])("navigates nested %s folders and returns to the waitlist without changing selection roots", async (kind) => {
    filesPage.mockImplementation((reference) =>
      Promise.resolve({
        files: reference.target.fileId === 1n ? [{ id: "7", name: "Day one", isDir: true }] : [{ id: "8", name: "image.jpg", size: 12 }],
        nextCursor: "",
        scope: FileScope.SAVED,
      }),
    );
    const choose = vi.fn();
    const remove = vi.fn();
    const label = kind === "restore" ? "Restore" : "Backup";
    render(<SelectionWaitlist kind={kind} label={label} entries={[folder]} busy={false} onRemove={remove} onClear={vi.fn()} onChooseVersion={choose} />);
    await userEvent.dblClick(await screen.findByText("Library/Trip"));
    await userEvent.dblClick(await screen.findByText("Library/Trip/Day one"));
    await userEvent.click(await screen.findByText("Library/Trip/Day one/image.jpg"));
    expect(screen.getByRole("button", { name: "Remove from list" })).toBeDisabled();
    if (kind === "restore") {
      await screen.findByText(/Saved .*Latest/);
      await userEvent.click(screen.getByRole("button", { name: "Change version" }));
      expect(choose).toHaveBeenCalledWith(expect.objectContaining({ fileID: "8", name: "image.jpg" }), expect.objectContaining({ id: 80n }));
    }
    await userEvent.click(screen.getByRole("button", { name: "Go up a directory" }));
    await screen.findByText("Library/Trip/Day one");
    await userEvent.click(screen.getByRole("button", { name: `${label} waitlist` }));
    await screen.findByText("Library/Trip");
    expect(remove).not.toHaveBeenCalled();
  });

  it("shows custom versions inside a selected directory without creating automatic duplicates", async () => {
    filesPage.mockResolvedValue({ files: [{ id: "8", name: "image.jpg" }], nextCursor: "", scope: FileScope.SAVED });
    const custom: SelectionEntry = {
      key: "version:81",
      name: "image.jpg",
      path: "Trip/image.jpg",
      fileID: "8",
      version: FileVersion.create({ id: 81n, fileId: 8n }),
    };
    const choose = vi.fn();
    render(
      <SelectionWaitlist
        kind="restore"
        label="Restore"
        entries={[folder, custom]}
        busy={false}
        onRemove={vi.fn()}
        onClear={vi.fn()}
        onChooseVersion={choose}
      />,
    );
    await userEvent.dblClick(await screen.findByText("Library/Trip"));
    const row = await screen.findByText("Library/Trip/image.jpg");
    expect(screen.getAllByText(/Custom version/)).toHaveLength(1);
    await userEvent.dblClick(row);
    expect(choose).toHaveBeenCalledWith(expect.objectContaining({ key: "version:81" }), custom.version);
  });
});
