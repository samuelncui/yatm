import { streamListing } from "@/test/files-fixture";
import { createRef } from "react";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { render } from "@/state/test-render";
import { MemoryRouter } from "react-router";
import { FileBrowser, FileToolbar, type FileBrowserHandle } from "@samuelncui/chonky";
import { beforeEach, expect, it, vi } from "vitest";
import { FilesEntry, FileOperationRef, ListFilesResponse, MeasureFilesResponse, EntryKind, FileScope } from "@/entity";
import { useFileBrowser } from "@/pages/file";
import { ToobarInfo } from "@/components/toolbarInfo";

const { list, search, measure, get } = vi.hoisted(() => ({ list: vi.fn(), search: vi.fn(), measure: vi.fn(), get: vi.fn() }));
vi.mock("@/api", async (original) => ({ ...(await original<typeof import("@/api")>()), filesCli: { list, search, measure, get } }));

const directory = FileOperationRef.create({ target: { oneofKind: "fileId", fileId: 7n } });
const folder = FilesEntry.create({ reference: directory, name: "Photos", kind: EntryKind.DIRECTORY });
const root = FilesEntry.create({ reference: { target: { oneofKind: "fileId", fileId: 0n } }, name: "Library", kind: EntryKind.DIRECTORY });
const noop = () => {};
const refresh = async () => {};
const handle = createRef<FileBrowserHandle>();
function Harness() {
  const browser = useFileBrowser(handle, "measure-test", refresh, noop, noop, undefined, undefined, FileScope.ALL);
  return (
    <>
      <FileBrowser ref={handle} {...browser.browserProps} disableDragAndDrop>
        <FileToolbar>
          <ToobarInfo files={browser.files} measurement={browser.measurement} total={browser.total} />
        </FileToolbar>
      </FileBrowser>
      <button onClick={() => void browser.refresh()}>Reload directory</button>
      <button onClick={() => void browser.search("name:Photos", false)}>Search photos</button>
      <output aria-label="folder bytes">{browser.files[0]?.size ?? "unknown"}</output>
      <output aria-label="visible rows">{browser.files.length}</output>
    </>
  );
}

beforeEach(() => {
  localStorage.clear();
  search.mockReset().mockReturnValue({
    response: Promise.resolve(ListFilesResponse.create({ entries: [folder], directory: root, breadcrumbs: [root], totalEntryCount: 1n, scope: FileScope.ALL })),
  });
  list.mockReset().mockReturnValue(
    streamListing(
      ListFilesResponse.create({
        entries: [folder],
        directory: root,
        breadcrumbs: [root],
        totalEntryCount: 700n,
        scope: FileScope.ALL,
      }),
    ),
  );
  measure.mockReset();
  get.mockReset();
});

it("restores the actual icon-only toolbar action and measures the query without detail hydration", async () => {
  measure.mockReturnValue({
    responses: (async function* () {
      yield MeasureFilesResponse.create({ result: { oneofKind: "item", item: { reference: directory, knownBytes: 2048n, complete: true, error: "" } } });
      yield MeasureFilesResponse.create({ result: { oneofKind: "summary", summary: { knownBytes: 4096n, complete: true, error: "" } } });
    })(),
  });
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  await screen.findByText("unknown");
  await waitFor(() => expect(list).toHaveBeenCalledOnce());
  await waitFor(() => expect(screen.getByLabelText("visible rows")).toHaveTextContent("1"));
  // The count is the listing's settled number, shown beside the size summary.
  await waitFor(() => expect(screen.getByLabelText("Loaded items size")).toHaveTextContent("700 entries"));
  expect(measure).not.toHaveBeenCalled();
  const button = screen.getByRole("button", { name: "Data Usage" });
  expect(button).not.toHaveTextContent("Data Usage");
  fireEvent.click(screen.getByRole("button", { name: "Search photos" }));
  await waitFor(() => expect(search).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button", { name: "Data Usage" }));
  await screen.findByLabelText("Data usage for all matching items");
  expect(measure).toHaveBeenCalledOnce();
  expect(measure.mock.calls[0][0]).toMatchObject({ query: "name:Photos", recursive: true, scope: FileScope.ALL });
  expect(measure.mock.calls[0][0]).not.toHaveProperty("cursor");
  expect(screen.getByLabelText("folder bytes")).toHaveTextContent("2048");
  expect(get).not.toHaveBeenCalled();
  expect(search).toHaveBeenCalledOnce();
});

it("keeps incomplete totals explicit and invalidates them on explicit refresh", async () => {
  measure.mockReturnValue({
    responses: (async function* () {
      yield MeasureFilesResponse.create({ result: { oneofKind: "summary", summary: { knownBytes: 20n, complete: false, error: "size is unknown" } } });
    })(),
  });
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  await waitFor(() => expect(list).toHaveBeenCalledOnce());
  await waitFor(() => expect(screen.getByLabelText("visible rows")).toHaveTextContent("1"));
  // The count is the listing's settled number, shown beside the size summary.
  await waitFor(() => expect(screen.getByLabelText("Loaded items size")).toHaveTextContent("700 entries"));
  fireEvent.click(screen.getByRole("button", { name: "Data Usage" }));
  expect(await screen.findByLabelText("Known data usage · size is unknown")).toHaveTextContent("? 20 B");
  fireEvent.click(screen.getByRole("button", { name: "Reload directory" }));
  await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  expect(screen.queryByLabelText("Known data usage · size is unknown")).not.toBeInTheDocument();
  expect(measure).toHaveBeenCalledOnce();
});

it("cancels a running measurement and rejects its late summary", async () => {
  let finish!: () => void;
  const pending = new Promise<void>((resolve) => {
    finish = resolve;
  });
  measure.mockReturnValue({
    responses: (async function* () {
      await pending;
      yield MeasureFilesResponse.create({ result: { oneofKind: "summary", summary: { knownBytes: 100n, complete: true, error: "" } } });
    })(),
  });
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  await waitFor(() => expect(list).toHaveBeenCalledOnce());
  await waitFor(() => expect(screen.getByLabelText("visible rows")).toHaveTextContent("1"));
  // The count is the listing's settled number, shown beside the size summary.
  await waitFor(() => expect(screen.getByLabelText("Loaded items size")).toHaveTextContent("700 entries"));
  fireEvent.click(screen.getByRole("button", { name: "Data Usage" }));
  await screen.findByLabelText("Calculating data usage");
  fireEvent.click(screen.getByRole("button", { name: "Cancel Data Usage" }));
  await waitFor(() => expect(measure.mock.calls[0][1].abort.aborted).toBe(true));
  await act(async () => finish());
  expect(screen.queryByLabelText("Data usage for all matching items")).not.toBeInTheDocument();
  expect(screen.getByLabelText("Calculation canceled")).toBeInTheDocument();
});

it("aborts measurement on query navigation and ignores a late result from the previous view", async () => {
  let finish!: () => void;
  const pending = new Promise<void>((resolve) => {
    finish = resolve;
  });
  measure.mockReturnValue({
    responses: (async function* () {
      await pending;
      yield MeasureFilesResponse.create({ result: { oneofKind: "summary", summary: { knownBytes: 100n, complete: true, error: "" } } });
    })(),
  });
  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
  await waitFor(() => expect(screen.getByLabelText("visible rows")).toHaveTextContent("1"));
  // The count is the listing's settled number, shown beside the size summary.
  await waitFor(() => expect(screen.getByLabelText("Loaded items size")).toHaveTextContent("700 entries"));
  fireEvent.click(screen.getByRole("button", { name: "Data Usage" }));
  await screen.findByLabelText("Calculating data usage");
  fireEvent.click(screen.getByRole("button", { name: "Search photos" }));
  await waitFor(() => expect(measure.mock.calls[0][1].abort.aborted).toBe(true));
  await act(async () => finish());
  expect(screen.queryByLabelText("Data usage for all matching items")).not.toBeInTheDocument();
  expect(measure).toHaveBeenCalledOnce();
});
