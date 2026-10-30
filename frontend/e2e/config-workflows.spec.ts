import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import {
  DeviceSettings,
  EntryKind,
  EstimateArchiveJobRequest,
  EstimateArchiveJobResponse,
  EstimateRestoreJobRequest,
  EstimateRestoreJobResponse,
  FileScope,
  FileSelection,
  FilesEntry,
  GetJobResponse,
  GetLocationResponse,
  GetPreviewCapabilitiesResponse,
  GetScanJobCreationResponse,
  GetScanJobProgressResponse,
  GetSettingsRequest,
  GetSettingsResponse,
  ImportPositionsRequest,
  ImportPositionsResponse,
  Job,
  JobKind,
  JobStatus,
  LibrarySettings,
  ListFilesResponse,
  ListLocationsResponse,
  ListMediaResponse,
  ListScanJobEntriesResponse,
  Location,
  Media,
  MediaAccess,
  MediaKind,
  PreviewSettings,
  ScanChange,
  ScanEntry,
  SettingsGroup,
  UpdateSettingsRequest,
  UpdateSettingsResponse,
} from "../src/entity";

// UI consumer checks use the same generated gRPC framing as tar-tape.spec.ts. Every service
// request is intercepted: these fixtures cannot query physical devices or production data.
const fulfill = async (route: Route, data: Uint8Array) => {
  const header = Buffer.alloc(5);
  header.writeUInt32BE(data.length, 1);
  const trailer = Buffer.from("grpc-status: 0\r\n");
  const trailerHeader = Buffer.alloc(5);
  trailerHeader[0] = 0x80;
  trailerHeader.writeUInt32BE(trailer.length, 1);
  await route.fulfill({ contentType: "application/grpc-web+proto", body: Buffer.concat([header, data, trailerHeader, trailer]) });
};
const select = async (page: Page, scope: Page | Locator, label: string, choice: string) => {
  await scope.getByRole("combobox", { name: label, exact: true }).click();
  await page.getByRole("option", { name: choice, exact: true }).click();
};
const expectFits = async (scope: Locator) => {
  const size = await scope.evaluate((node) => ({
    left: node.getBoundingClientRect().left,
    right: node.getBoundingClientRect().right,
    viewport: window.innerWidth,
    overflow: node.scrollWidth > node.clientWidth + 1,
    pageOverflow: document.documentElement.scrollWidth > window.innerWidth + 1,
  }));
  expect(size.left).toBeGreaterThanOrEqual(0);
  expect(size.right).toBeLessThanOrEqual(size.viewport);
  expect(size.overflow).toBe(false);
  expect(size.pageOverflow).toBe(false);
};
const preview = () =>
  PreviewSettings.create({
    enabled: true,
    command: "old-preview",
    concurrency: 2,
    timeoutSeconds: 600,
    maxInputPixels: 64000000n,
    generators: [
      {
        enabled: true,
        extensions: [{ name: "jpg", enabled: true }],
        options: { oneofKind: "image", image: { maxWidth: 512, maxHeight: 512, format: "webp", quality: 80 } },
      },
      {
        enabled: true,
        extensions: [{ name: "mp4", enabled: true }],
        options: {
          oneofKind: "video",
          video: {
            posterWidth: 512,
            posterHeight: 512,
            timelineWidth: 320,
            timelineHeight: 180,
            timelineIntervalSeconds: 10,
            timelineMaxFrames: 30,
            format: "webp",
            quality: 75,
          },
        },
      },
    ],
  });
const fixture = async (page: Page) => {
  const location = Location.create({ id: 2n, name: "Fixture Location", rootPath: "/fixture", restoreTarget: true });
  const media = Media.create({ id: 7n, name: "Fixture Volume", kind: MediaKind.VOLUME, identity: "fixture", capabilities: { read: MediaAccess.RANDOM } });
  const state = {
    preview: preview(),
    updates: 0,
    capabilityReads: 0,
    creationReads: 0,
    failCapabilities: false,
    admissions: [] as boolean[],
    unexpected: [] as string[],
  };
  await page.route("**/services/**", async (route) => {
    const method = new URL(route.request().url()).pathname.replace(/^\/services\//, "");
    const bytes = route.request().postDataBuffer()!.subarray(5);
    switch (method) {
      case "yatm.v1.LocationService/List":
        return fulfill(route, ListLocationsResponse.toBinary(ListLocationsResponse.create({ locations: [location] })));
      case "yatm.v1.LocationService/Get":
        return fulfill(route, GetLocationResponse.toBinary(GetLocationResponse.create({ location })));
      case "yatm.v1.MediaService/List":
        return fulfill(route, ListMediaResponse.toBinary(ListMediaResponse.create({ media: [media] })));
      case "yatm.v1.FilesService/List":
        return fulfill(
          route,
          ListFilesResponse.toBinary(
            ListFilesResponse.create({
              scope: FileScope.ALL,
              directory: FilesEntry.create({
                name: "Library",
                kind: EntryKind.DIRECTORY,
                reference: { target: { oneofKind: "fileId", fileId: 0n } },
              }),
              totalEntryCount: 0n,
            }),
          ),
        );
      case "yatm.v1.ArchiveJobService/Estimate": {
        const request = EstimateArchiveJobRequest.fromBinary(bytes);
        return fulfill(
          route,
          EstimateArchiveJobResponse.toBinary(
            EstimateArchiveJobResponse.create({ result: { fileCount: 1n, totalBytes: 512n, selections: request.selections } }),
          ),
        );
      }
      case "yatm.v1.RestoreJobService/Estimate": {
        const request = EstimateRestoreJobRequest.fromBinary(bytes);
        return fulfill(
          route,
          EstimateRestoreJobResponse.toBinary(
            EstimateRestoreJobResponse.create({ result: { fileCount: 1n, totalBytes: 512n, selections: request.selections } }),
          ),
        );
      }
      case "yatm.v1.SettingsService/Get": {
        const request = GetSettingsRequest.fromBinary(bytes);
        const value =
          request.group === SettingsGroup.PREVIEW
            ? { oneofKind: "preview" as const, preview: state.preview }
            : request.group === SettingsGroup.DEVICE
              ? { oneofKind: "device" as const, device: DeviceSettings.create() }
              : { oneofKind: "library" as const, library: LibrarySettings.create({ includeFilesWithoutVersions: true }) };
        return fulfill(route, GetSettingsResponse.toBinary(GetSettingsResponse.create({ value: { value } })));
      }
      case "yatm.v1.SettingsService/Update": {
        const request = UpdateSettingsRequest.fromBinary(bytes);
        if (request.value?.value.oneofKind === "preview") state.preview = PreviewSettings.clone(request.value.value.preview);
        state.updates++;
        return fulfill(route, UpdateSettingsResponse.toBinary(UpdateSettingsResponse.create({ value: request.value })));
      }
      case "yatm.v1.PreviewService/GetCapabilities": {
        state.capabilityReads++;
        if (state.failCapabilities) {
          state.failCapabilities = false;
          const trailer = Buffer.from("grpc-status: 14\r\ngrpc-message: Temporary%20Preview%20failure\r\n");
          const header = Buffer.alloc(5);
          header[0] = 0x80;
          header.writeUInt32BE(trailer.length, 1);
          return route.fulfill({ contentType: "application/grpc-web+proto", body: Buffer.concat([header, trailer]) });
        }
        return fulfill(
          route,
          GetPreviewCapabilitiesResponse.toBinary(
            GetPreviewCapabilitiesResponse.create({
              available: true,
              version: state.preview.command,
              kinds: ["image", "video"],
              inputExtensionsByKind: { image: { extensions: ["jpg"] }, video: { extensions: ["mp4"] } },
              outputFormats: ["webp", "jpg", "png"],
            }),
          ),
        );
      }
      case "yatm.v1.FilesService/ImportPositions": {
        state.admissions.push(ImportPositionsRequest.fromBinary(bytes).dryrun);
        return fulfill(
          route,
          ImportPositionsResponse.toBinary(ImportPositionsResponse.create({ fileCount: 2n, directoryCount: 1n, existingCount: 3n, skippedFileCount: 4n })),
        );
      }
      case "yatm.v1.JobService/Get":
        return fulfill(
          route,
          GetJobResponse.toBinary(GetJobResponse.create({ job: Job.create({ id: 30n, kind: JobKind.SCAN, status: JobStatus.COMPLETED }) })),
        );
      case "yatm.v1.ScanJobService/GetProgress":
        return fulfill(
          route,
          GetScanJobProgressResponse.toBinary(GetScanJobProgressResponse.create({ progress: { totalKnown: true, totalFileCount: 2n, copiedFileCount: 2n } })),
        );
      case "yatm.v1.ScanJobService/GetCreation":
        state.creationReads++;
        return fulfill(route, GetScanJobCreationResponse.toBinary(GetScanJobCreationResponse.create({ request: { spec: { compareLibrary: true } } })));
      case "yatm.v1.ScanJobService/ListEntries":
        return fulfill(
          route,
          ListScanJobEntriesResponse.toBinary(
            ListScanJobEntriesResponse.create({
              totalEntryCount: 2n,
              entries: [
                ScanEntry.create({ id: 1n, path: "known.jpg", change: ScanChange.ADDED, signature: new Uint8Array([1]), matchingCopies: 0n }),
                ScanEntry.create({ id: 2n, path: "unknown.jpg", change: ScanChange.ADDED }),
              ],
            }),
          ),
        );
      default:
        state.unexpected.push(method);
        return route.abort();
    }
  });
  return state;
};

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "narrow", width: 390, height: 844 },
]) {
  test(`Preview controls preserve drafts, retry and distinguish PNG on ${viewport.name}`, async ({ page }, info) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    state.failCapabilities = true;
    await page.goto("/settings/preview");
    await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
    await page.getByRole("button", { name: "Retry", exact: true }).click();
    await expect.poll(() => state.capabilityReads).toBe(2);
    await select(page, page, "Output format", "PNG");
    await expect(page.getByRole("spinbutton", { name: "Quality", exact: true })).toBeDisabled();
    await expect(page.getByRole("spinbutton", { name: "Quality", exact: true })).toHaveValue("80");
    await expect(page.getByText("PNG is lossless; quality does not apply.")).toBeVisible();
    await page.getByRole("button", { name: /^Advanced runtime/ }).click();
    await page.getByRole("textbox", { name: "Executable", exact: true }).fill("new-preview");
    await expect(page.getByText(/Save to check this executable/)).toBeVisible();
    await page.getByRole("button", { name: /^Supported formats/ }).click();
    await expect(page.getByRole("button", { name: "Check saved executable again" })).toBeDisabled();
    const reads = state.capabilityReads;
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect.poll(() => state.updates).toBe(1);
    await expect.poll(() => state.capabilityReads).toBe(reads + 1);
    await expect(page.getByText("Helper new-preview")).toBeVisible();
    await expectFits(page.locator(".app-page"));
    await page.screenshot({ animations: "disabled", path: info.outputPath(`preview-${viewport.name}.png`) });
    await select(page, page, "Type", "Video");
    const advanced = page.getByRole("button", { name: /^Advanced output/ });
    await advanced.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByText(/Samples cover the whole video/)).toBeVisible();
    await expect(page.getByRole("spinbutton", { name: "Minimum sampling interval (seconds)" })).toBeVisible();
    await page.screenshot({ animations: "disabled", path: info.outputPath(`preview-video-${viewport.name}.png`) });
    expect(state.unexpected).toEqual([]);
  });

  test(`Scan policies, inventory review and matching counts remain explicit on ${viewport.name}`, async ({ page }, info) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    await page.addInitScript(() => localStorage.setItem("files:import-positions-without-prompt", "1"));
    await page.goto("/scan?location=2&previews=1");
    await page.getByRole("button", { name: /^Advanced/ }).click();
    await select(page, page, "Signatures", "Reuse known signatures only");
    await page.getByRole("button", { name: /^Advanced/ }).click();
    await expect(page.getByText(/Preview generation skips files whose signatures are unknown/)).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Signatures", exact: true })).toHaveCount(0);
    await select(page, page, "Results", "View results only");
    await expect(page.getByText(/Selected reads and Preview generation still run/)).toBeVisible();
    await page.getByRole("button", { name: "Read new and changed files", exact: true }).click();
    await page.getByRole("button", { name: /^Advanced/ }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("combobox", { name: "Signatures" })).toContainText("Read new and changed files");
    await page.getByRole("checkbox", { name: "Count matching archived copies" }).check();
    await expectFits(page.locator(".app-page"));
    await page.screenshot({ animations: "disabled", path: info.outputPath(`scan-options-${viewport.name}.png`) });
    await page.goto("/scan?media=7");
    await select(page, page, "Results", "Update inventory");
    await page.getByRole("button", { name: "Review what will be added", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("button", { name: "Show confirmation next time" })).toBeVisible();
    expect(state.admissions).toEqual([true]);
    await dialog.getByRole("button", { name: "Show confirmation next time" }).click();
    await expect.poll(() => page.evaluate(() => localStorage.getItem("files:import-positions-without-prompt"))).toBeNull();
    await expectFits(dialog);
    await expect(dialog).toHaveCSS("opacity", "1");
    await page.screenshot({ animations: "disabled", path: info.outputPath(`inventory-review-${viewport.name}.png`) });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(state.admissions).toEqual([true]);
    await page.goto("/jobs/30");
    await page.getByRole("button", { name: "Results", exact: true }).click();
    const results = page.getByRole("dialog", { name: "Scan results" });
    await expect(results.getByText(/Matching archived copies: 0/)).toBeVisible();
    await expect(results.getByText(/Archived copies unknown \(no signature\)/)).toBeVisible();
    expect(state.creationReads).toBe(1);
    await expectFits(results);
    await expect(results).toHaveCSS("opacity", "1");
    await page.screenshot({ animations: "disabled", path: info.outputPath(`scan-matches-${viewport.name}.png`) });
    expect(state.unexpected).toEqual([]);
  });

  test(`Archive and Restore expose optional settings in keyboard-accessible folds on ${viewport.name}`, async ({ page }, info) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    const selection = FileSelection.toJsonString(
      FileSelection.create({ scope: FileScope.ALL, target: { oneofKind: "location", location: { locationId: 2n, path: "photo.jpg" } } }),
    );
    await page.addInitScript((saved) => {
      for (const kind of ["archive", "restore"])
        sessionStorage.setItem(`job-selection:${kind}`, JSON.stringify([{ key: saved, name: "photo.jpg", path: "photo.jpg", selection: saved }]));
    }, selection);
    await page.goto("/archive");
    await page.getByRole("button", { name: "Archive…", exact: true }).click();
    let dialog = page.getByRole("dialog", { name: "Archive", exact: true });
    await select(page, dialog, "Previews", "Generate missing");
    const previewFold = dialog.getByRole("button", { name: /^Advanced Preview/ });
    await previewFold.focus();
    await page.keyboard.press("Enter");
    await dialog.getByRole("checkbox", { name: "Recheck source hashes for previews" }).check();
    await expect(dialog.getByText(/The Archive copy hashes content independently/)).toBeVisible();
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: info.outputPath(`archive-preview-${viewport.name}.png`) });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.goto("/restore");
    await page.getByRole("button", { name: "Restore…", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "Restore", exact: true });
    const advanced = dialog.getByRole("button", { name: /^Advanced/ });
    await advanced.focus();
    await page.keyboard.press("Enter");
    await dialog.getByRole("checkbox", { name: "Allow damaged copies" }).check();
    await advanced.click();
    await expect(dialog.getByText(/Complete damaged output may be retained for recovery/)).toBeVisible();
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: info.outputPath(`restore-advanced-${viewport.name}.png`) });
    expect(state.unexpected).toEqual([]);
  });
  test(`Location, Library import and Job deletion explain their scope on ${viewport.name}`, async ({ page }, info) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    await page.goto("/settings/locations/new");
    await expect(page.getByRole("textbox", { name: "Ignore", exact: true })).toHaveValue("");
    await expect(page.getByText(/server.*default|default.*server/)).toBeVisible();
    await expectFits(page.locator(".app-page"));
    await page.screenshot({ animations: "disabled", path: info.outputPath(`location-new-${viewport.name}.png`) });
    await page.goto("/settings/library");
    await expect(page.getByText(/Export includes the Library, Locations and original links/)).toBeVisible();
    await page.getByRole("button", { name: "Import", exact: true }).click();
    let dialog = page.getByRole("dialog");
    await expect(dialog.getByText(/including Locations and original links/)).toBeVisible();
    await expect(dialog.getByText(/Disk files are not/)).toBeVisible();
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: info.outputPath(`library-import-${viewport.name}.png`) });
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.goto("/jobs/30");
    await page.getByRole("button", { name: "Delete Job", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "Delete Job 30?", exact: true });
    await expect(dialog.getByText(/attachments.*older versions/)).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: info.outputPath(`job-delete-${viewport.name}.png`) });
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    expect(state.unexpected).toEqual([]);
  });
}
