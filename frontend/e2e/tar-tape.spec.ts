import { expect, test, type Locator, type Page, type Route } from "@playwright/test";
import {
  ArchiveTapeWriteMode,
  CopyStatus,
  DeviceKind,
  DeviceFeature,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceFeatureAvailability,
  DeviceFeatureValidation,
  DeviceFeatureEvidence,
  TapeGeometryLimits,
  TapeReadOrder,
  ScanResultPolicy,
  GetScanJobCreationResponse,
  DeviceSettings,
  DeviceSummary,
  GetArchiveJobProgressResponse,
  GetJobRequest,
  GetJobResponse,
  GetRestoreJobProgressResponse,
  GetScanJobProgressResponse,
  GetSettingsRequest,
  GetSettingsResponse,
  InspectDeviceResponse,
  InspectMediaRequest,
  InspectMediaResponse,
  Job,
  JobKind,
  JobStatus,
  ListDevicesResponse,
  ListLocationsResponse,
  ListMediaRequest,
  ListMediaResponse,
  ListRestoreJobMediaResponse,
  Media,
  MediaKind,
  Progress,
  ReadMediaTarget,
  ReadScanMediaRequest,
  ReadScanMediaResponse,
  RestoreMediaRequest,
  RestoreMediaResponse,
  SettingsGroup,
  TapeMediaProfile,
  TapeRecordMode,
  TarWriteOptions,
  UpdateSettingsRequest,
  UpdateSettingsResponse,
  WriteArchiveMediaRequest,
  WriteArchiveMediaResponse,
} from "../src/entity";

// Rendered consumer acceptance uses the existing browser runner and generated API messages.
// It exercises forms and submissions; it does not qualify physical I/O or a release package.
const baseline = TarWriteOptions.create({ recordMode: TapeRecordMode.VARIABLE, recordBytes: 262144, hardwareCompression: false });
const devices = [DeviceSummary.create({ device: "drive-a", kind: DeviceKind.TAPE }), DeviceSummary.create({ device: "drive-b", kind: DeviceKind.TAPE })];
const rawTape = Media.create({
  id: 7n,
  name: "Tar cartridge",
  identity: "ABC123",
  kind: MediaKind.TAPE,
  profile: {
    kind: {
      oneofKind: "tape",
      tape: TapeMediaProfile.create({
        format: "tar_pax_v1",
        tar: { initializationId: "4af327b4-4449-4080-9b6e-c11bf836c6f7", recordMode: TapeRecordMode.VARIABLE, recordBytes: 262144, hardwareCompression: false },
      }),
    },
  },
});
const ltfsTape = Media.create({ ...rawTape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format: "ltfs_v1" }) } } });

const fulfill = async (route: Route, data: Uint8Array) => {
  const header = Buffer.alloc(5);
  header.writeUInt32BE(data.length, 1);
  const trailer = Buffer.from("grpc-status: 0\r\n");
  const trailerHeader = Buffer.alloc(5);
  trailerHeader[0] = 0x80;
  trailerHeader.writeUInt32BE(trailer.length, 1);
  await route.fulfill({ contentType: "application/grpc-web+proto", body: Buffer.concat([header, data, trailerHeader, trailer]) });
};
const fail = async (route: Route, message: string) => {
  const trailer = Buffer.from(`grpc-status: 9\r\ngrpc-message: ${encodeURIComponent(message)}\r\n`);
  const header = Buffer.alloc(5);
  header[0] = 0x80;
  header.writeUInt32BE(trailer.length, 1);
  await route.fulfill({ contentType: "application/grpc-web+proto", body: Buffer.concat([header, trailer]) });
};
const requestBytes = (route: Route) => route.request().postDataBuffer()!.subarray(5);
const select = async (page: Page, scope: Page | Locator, name: string, option: string) => {
  await scope.getByRole("combobox", { name, exact: true }).click();
  await page.getByRole("option", { name: option, exact: true }).click();
};
const expectFits = async (scope: Locator) => {
  const geometry = await scope.evaluate((element) => ({
    left: element.getBoundingClientRect().left,
    right: element.getBoundingClientRect().right,
    viewport: window.innerWidth,
    overflow: element.scrollWidth > element.clientWidth + 1,
    pageOverflow: document.documentElement.scrollWidth > window.innerWidth + 1,
  }));
  expect(geometry.left).toBeGreaterThanOrEqual(0);
  expect(geometry.right).toBeLessThanOrEqual(geometry.viewport);
  expect(geometry.overflow).toBe(false);
  expect(geometry.pageOverflow).toBe(false);
};
const fixture = async (page: Page) => {
  const state = {
    settings: DeviceSettings.create({
      devices: [
        {
          device: "drive-a",
          options: { oneofKind: "tape", tape: { readOrder: TapeReadOrder.UNSPECIFIED, defaultFormat: "tar_pax_v1", tarWriteOptions: baseline } },
        },
        {
          device: "drive-b",
          options: {
            oneofKind: "tape",
            tape: { readOrder: TapeReadOrder.UNSPECIFIED, defaultFormat: "ltfs_v1", tarWriteOptions: { ...baseline, recordBytes: 131072 } },
          },
        },
      ],
    }),
    registered: [] as Media[],
    readTape: rawTape,
    inspections: [] as string[],
    cartridgeInspections: 0,
    writes: [] as WriteArchiveMediaRequest[],
    reads: [] as ReadMediaTarget[],
    settingsUpdates: 0,
    requireHint: false,
    scanResultPolicy: ScanResultPolicy.VERIFY_COPIES,
    qualifiedReload: false,
    reloadSupport: DeviceFeatureSupport.SUPPORTED,
    reloadValidation: undefined as DeviceFeatureValidation | undefined,
    unexpected: [] as string[],
  };
  await page.route("**/services/**", async (route) => {
    const method = new URL(route.request().url()).pathname.replace(/^\/services\//, "");
    switch (method) {
      case "yatm.v1.LocationService/List":
        return fulfill(route, ListLocationsResponse.toBinary(ListLocationsResponse.create()));
      case "yatm.v1.SettingsService/Get": {
        const request = GetSettingsRequest.fromBinary(requestBytes(route));
        expect(request.group).toBe(SettingsGroup.DEVICE);
        return fulfill(route, GetSettingsResponse.toBinary(GetSettingsResponse.create({ value: { value: { oneofKind: "device", device: state.settings } } })));
      }
      case "yatm.v1.SettingsService/Update": {
        const request = UpdateSettingsRequest.fromBinary(requestBytes(route));
        expect(request.value?.value.oneofKind).toBe("device");
        const value = request.value?.value;
        if (value?.oneofKind !== "device") throw new Error("Device Settings update required");
        state.settings = DeviceSettings.clone(value.device);
        state.settingsUpdates++;
        return fulfill(route, UpdateSettingsResponse.toBinary(UpdateSettingsResponse.create({ value: request.value })));
      }
      case "yatm.v1.MediaService/ListDevices":
        return fulfill(route, ListDevicesResponse.toBinary(ListDevicesResponse.create({ devices })));
      case "yatm.v1.MediaService/InspectDevice": {
        state.inspections.push(method);
        return fulfill(
          route,
          InspectDeviceResponse.toBinary(
            InspectDeviceResponse.create({
              device: devices[0],
              report: {
                oneofKind: "tape",
                tape: {
                  encryptionEnabled: false,
                  recommendationDetail: "Baseline write options",
                  recommendedWriteOptions: baseline,
                  features: [
                    DeviceFeature.create({
                      feature: DeviceFeatureKind.END_OF_WRAP_POSITION,
                      support: DeviceFeatureSupport.SUPPORTED,
                      evidence: DeviceFeatureEvidence.REPORTED,
                      limits: {
                        oneofKind: "geometry",
                        geometry: TapeGeometryLimits.create({ allWraps: true, reportedWraps: 0, source: "synthetic long REOWP response" }),
                      },
                    }),
                    DeviceFeature.create({
                      feature: DeviceFeatureKind.SOFTWARE_READ_ORDER,
                      validation: DeviceFeatureValidation.UNVALIDATED,
                      availability: DeviceFeatureAvailability.UNAVAILABLE,
                      detail: "No qualified geometry/timing model; Auto falls back to Logical",
                    }),
                    DeviceFeature.create({
                      feature: DeviceFeatureKind.NON_EJECTING_RELOAD,
                      support: state.reloadSupport,
                      availability: state.qualifiedReload ? DeviceFeatureAvailability.READY : DeviceFeatureAvailability.UNAVAILABLE,
                      validation: state.reloadValidation ?? (state.qualifiedReload ? DeviceFeatureValidation.VALIDATED : DeviceFeatureValidation.UNVALIDATED),
                      detail: "Non-ejecting reload is not physically qualified",
                    }),
                  ],
                },
              },
            }),
          ),
        );
      }
      case "yatm.v1.MediaService/Inspect": {
        state.cartridgeInspections++;
        const request = InspectMediaRequest.fromBinary(requestBytes(route));
        const reply =
          new URL(page.url()).pathname === "/jobs/20"
            ? InspectMediaResponse.create({ identity: request.identity ?? "" })
            : InspectMediaResponse.create({ identity: state.readTape.identity, media: state.readTape });
        return fulfill(route, InspectMediaResponse.toBinary(reply));
      }
      case "yatm.v1.MediaService/List": {
        const request = ListMediaRequest.fromBinary(requestBytes(route));
        const media =
          request.param.oneofKind === "ids"
            ? [state.readTape].filter((value) => request.param.oneofKind === "ids" && request.param.ids.ids.includes(value.id))
            : request.param.oneofKind === "list" && request.param.list.kinds.includes(MediaKind.TAPE)
              ? state.registered
              : [];
        return fulfill(route, ListMediaResponse.toBinary(ListMediaResponse.create({ media })));
      }
      case "yatm.v1.JobService/Get": {
        const request = GetJobRequest.fromBinary(requestBytes(route));
        const kind = request.id === 20n ? JobKind.ARCHIVE : request.id === 21n ? JobKind.RESTORE : JobKind.SCAN;
        return fulfill(
          route,
          GetJobResponse.toBinary(
            GetJobResponse.create({
              job: Job.create({ id: request.id, kind, status: JobStatus.READY, mediaId: kind === JobKind.SCAN ? state.readTape.id : undefined }),
            }),
          ),
        );
      }
      case "yatm.v1.ArchiveJobService/GetProgress":
        return fulfill(
          route,
          GetArchiveJobProgressResponse.toBinary(GetArchiveJobProgressResponse.create({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n }) })),
        );
      case "yatm.v1.RestoreJobService/GetProgress":
        return fulfill(
          route,
          GetRestoreJobProgressResponse.toBinary(GetRestoreJobProgressResponse.create({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n }) })),
        );
      case "yatm.v1.ScanJobService/GetCreation":
        return fulfill(
          route,
          GetScanJobCreationResponse.toBinary(GetScanJobCreationResponse.create({ request: { spec: { resultPolicy: state.scanResultPolicy } } })),
        );
      case "yatm.v1.ScanJobService/GetProgress":
        return fulfill(
          route,
          GetScanJobProgressResponse.toBinary(GetScanJobProgressResponse.create({ progress: Progress.create({ totalKnown: true, totalFileCount: 1n }) })),
        );
      case "yatm.v1.RestoreJobService/ListMedia":
        return fulfill(
          route,
          ListRestoreJobMediaResponse.toBinary(ListRestoreJobMediaResponse.create({ media: [{ mediaId: state.readTape.id, status: CopyStatus.PENDING }] })),
        );
      case "yatm.v1.ArchiveJobService/WriteMedia": {
        state.writes.push(WriteArchiveMediaRequest.fromBinary(requestBytes(route)));
        return fulfill(route, WriteArchiveMediaResponse.toBinary(WriteArchiveMediaResponse.create()));
      }
      case "yatm.v1.RestoreJobService/RestoreMedia": {
        const target = RestoreMediaRequest.fromBinary(requestBytes(route)).target!;
        state.reads.push(target);
        if (state.requireHint && target.backend.oneofKind === "tape" && target.backend.tape.mediaId === undefined)
          return fail(route, "Select a Media hint before reading this Tape.");
        return fulfill(route, RestoreMediaResponse.toBinary(RestoreMediaResponse.create()));
      }
      case "yatm.v1.ScanJobService/ReadMedia": {
        state.reads.push(ReadScanMediaRequest.fromBinary(requestBytes(route)).target!);
        return fulfill(route, ReadScanMediaResponse.toBinary(ReadScanMediaResponse.create()));
      }
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
  test(`Device Settings preserves inactive values and exposes Advanced errors on ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    await page.goto("/settings/devices");
    await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
    const advanced = page.getByRole("button", { name: /^Advanced/ });
    await expect(advanced).toHaveAttribute("aria-expanded", "false");
    await advanced.click();
    const size = page.getByRole("spinbutton", { name: "Record size (bytes)" });
    await size.fill("513");
    await advanced.click();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(advanced).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByText(/Record size must be a multiple/)).toBeVisible();
    await expect(size).toHaveValue("513");
    await size.scrollIntoViewIfNeeded();
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`device-settings-error-${viewport.name}.png`) });
    expect(state.settingsUpdates).toBe(0);
    await size.fill("524288");
    // Read order remains available outside Advanced after correcting a hidden field.
    await select(page, page, "Read order", "Auto");
    await select(page, page, "Default Tape format", "LTFS");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText("Device settings saved.")).toBeVisible();
    expect(state.settings.devices[0].options.oneofKind === "tape" && state.settings.devices[0].options.tape.tarWriteOptions).toEqual({
      ...baseline,
      recordBytes: 524288,
    });
    expect(state.settings.devices[0].options.oneofKind === "tape" && state.settings.devices[0].options.tape.readOrder).toBe(TapeReadOrder.AUTO);
    expect(state.settings.devices[1].options.oneofKind === "tape" && state.settings.devices[1].options.tape.readOrder).toBe(TapeReadOrder.UNSPECIFIED);
    expect(state.inspections).toEqual([]);
    await page.getByRole("combobox", { name: "Read order" }).scrollIntoViewIfNeeded();
    await expectFits(page.locator(".app-page"));
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`device-settings-${viewport.name}.png`) });
    expect(state.unexpected).toEqual([]);
  });

  test(`Device model JSON stays in its Apply dialog until saved on ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    await page.goto("/settings/devices");
    await page.getByRole("navigation", { name: "Settings views" }).getByRole("tab", { name: "Locations", exact: true }).click();
    await page.getByRole("navigation", { name: "Settings views" }).getByRole("tab", { name: "Devices", exact: true }).click();
    const section = page.getByRole("button", { name: "Manual read parameters", exact: true });
    await section.focus();
    await page.keyboard.press("Enter");
    await page.getByRole("button", { name: "Edit read parameters", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Edit manual read parameters", exact: true });
    const field = dialog.getByRole("textbox", { name: "Read parameters JSON" });
    await field.fill('{"reading":{"filemarkOverheadSeconds":-1}}');
    await page.goBack();
    const backPrompt = page.getByRole("dialog", { name: "Discard unsaved changes?", exact: true });
    await expect(backPrompt).toBeVisible();
    await backPrompt.getByRole("button", { name: "Keep editing" }).click();
    await expect(field).toHaveValue('{"reading":{"filemarkOverheadSeconds":-1}}');
    await dialog.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(dialog.getByText("Read parameters filemark overhead must be nonnegative.")).toBeVisible();
    await expectFits(dialog);
    await expect(dialog).toHaveCSS("opacity", "1");
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`model-json-error-${viewport.name}.png`) });
    await field.press("Escape");
    const discard = page.getByRole("dialog", { name: "Discard unsaved changes?", exact: true });
    await expect(discard.getByRole("button", { name: "Keep editing" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(discard).toHaveCount(0);
    await expect(field).toHaveValue('{"reading":{"filemarkOverheadSeconds":-1}}');
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await discard.getByRole("button", { name: "Discard", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    expect(state.settingsUpdates).toBe(0);
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    await page.getByRole("button", { name: "Edit read parameters", exact: true }).click();
    await field.fill('{"reading":{"bytesPerSecond":93000000}}');
    await dialog.getByRole("button", { name: "Apply", exact: true }).click();
    expect(state.settingsUpdates).toBe(0);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByText("Device settings saved.")).toBeVisible();
    expect(state.settingsUpdates).toBe(1);
    await page.getByRole("button", { name: "Use automatic parameters", exact: true }).click();
    await page.getByRole("navigation", { name: "Settings views" }).getByRole("tab", { name: "Jobs", exact: true }).click();
    await expect(discard).toBeVisible();
    await discard.getByRole("button", { name: "Keep editing" }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByRole("button", { name: "Use automatic parameters", exact: true })).toBeEnabled();
    expect(state.settingsUpdates).toBe(1);
    expect(state.inspections).toEqual([]);
    expect(state.unexpected).toEqual([]);
  });

  test(`Load Tape resolves FORMAT defaults and keeps APPEND immutable on ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    await page.goto("/jobs/20");
    await page.getByRole("button", { name: "Choose archive storage", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Choose archive storage", exact: true });
    await select(page, dialog, "Storage type", "Tape");
    await select(page, dialog, "Tape device", "drive-a");
    await expect(dialog.getByRole("combobox", { name: "Tape format" })).toContainText("Tar");
    await dialog.getByRole("button", { name: /^Advanced/ }).click();
    await dialog.getByRole("spinbutton", { name: "Record size (bytes)" }).fill("524288");
    await select(page, dialog, "Tape format", "LTFS");
    await select(page, dialog, "Tape format", "Tar");
    await select(page, dialog, "Tape device", "drive-b");
    await expect(dialog.getByRole("combobox", { name: "Tape format" })).toContainText("Tar");
    await dialog.getByRole("button", { name: /^Advanced/ }).click();
    await expect(dialog.getByRole("spinbutton", { name: "Record size (bytes)" })).toHaveValue("524288");
    await dialog.getByRole("textbox", { name: "Tape barcode", exact: true }).fill("NEW123");
    await dialog.getByRole("button", { name: "Check Tape", exact: true }).click();
    await dialog.getByRole("textbox", { name: "Tape Name", exact: true }).fill("New tar Tape");
    await expect(dialog.getByRole("button", { name: "Start archive", exact: true })).toBeDisabled();
    await dialog.getByRole("textbox", { name: "Confirm FORMAT barcode" }).fill("NEW123");
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-format-${viewport.name}.png`) });
    await dialog.getByRole("combobox", { name: "Storage type", exact: true }).scrollIntoViewIfNeeded();
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-format-top-${viewport.name}.png`) });
    await dialog.getByRole("textbox", { name: "Confirm FORMAT barcode" }).press("Enter");
    await expect.poll(() => state.writes.length).toBe(1);
    expect(state.writes[0].target?.backend.oneofKind === "tape" && state.writes[0].target.backend.tape).toMatchObject({
      mode: ArchiveTapeWriteMode.FORMAT,
      format: "tar_pax_v1",
      tarOptions: { ...baseline, recordBytes: 524288 },
    });

    state.registered = [rawTape];
    await page.getByRole("button", { name: "Choose archive storage", exact: true }).click();
    await select(page, dialog, "Storage type", "Tape");
    await select(page, dialog, "Tape device", "drive-a");
    await select(page, dialog, "Tape action", "APPEND");
    await dialog.getByRole("textbox", { name: "Tape barcode", exact: true }).fill("ABC123");
    await dialog.getByRole("button", { name: "Check Tape", exact: true }).click();
    await expect(dialog.getByText(/immutable cartridge profile/)).toBeVisible();
    await expect(dialog.getByRole("combobox", { name: "Tape format" })).toHaveAttribute("aria-disabled", "true");
    await expect(dialog.getByRole("spinbutton", { name: "Record size (bytes)" })).toHaveCount(0);
    await dialog.getByRole("button", { name: "Start archive", exact: true }).click();
    await expect.poll(() => state.writes.length).toBe(2);
    expect(state.writes[1].target?.backend.oneofKind === "tape" && state.writes[1].target.backend.tape.tarOptions).toBeUndefined();
    expect(state.unexpected).toEqual([]);
  });
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "narrow", width: 390, height: 844 },
]) {
  test(`tar Restore/Scan support automatic and manual selection while LTFS retains its adapter on ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    for (const [id, openName, startName] of [
      [21, "Choose archive storage to read", "Start restore"],
      [22, "Choose Media to read", "Start reading"],
    ] as const) {
      await page.goto(`/jobs/${id}`);
      if (id === 21) await expect(page.getByText("Tar cartridge: pending", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: openName, exact: true }).click();
      const dialog = page.getByRole("dialog");
      await select(page, dialog, "Tape device", "drive-a");
      await expect(dialog.getByRole("combobox", { name: "Tape Media" })).toContainText("Automatic identification");
      await dialog.getByRole("button", { name: startName, exact: true }).click();
      await expect.poll(() => state.reads.length).toBe(id === 21 ? 1 : 3);
      const automatic = state.reads.at(-1)!;
      expect(automatic.backend.oneofKind === "tape" && automatic.backend.tape.mediaId).toBeUndefined();
      expect(automatic.expectedMediaId).toBe(0n);
      await page.getByRole("button", { name: openName, exact: true }).click();
      await select(page, dialog, "Tape device", "drive-a");
      await select(page, dialog, "Tape Media", "Tar cartridge · ABC123");
      await expectFits(dialog);
      await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-read-${id}-${viewport.name}.png`) });
      await dialog.getByRole("button", { name: startName, exact: true }).click();
      await expect.poll(() => state.reads.length).toBe(id === 21 ? 2 : 4);
      const hinted = state.reads.at(-1)!;
      expect(hinted.backend.oneofKind === "tape" && hinted.backend.tape.mediaId).toBe(7n);
      expect(hinted.expectedMediaId).toBe(0n);
    }
    expect(state.cartridgeInspections).toBe(0);
    for (const [index, format] of ["ltfs_v1", "ltfs_v0"].entries()) {
      state.readTape = Media.create({ ...ltfsTape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format }) } } });
      await page.goto("/jobs/21");
      await expect(page.getByText("Tar cartridge: pending", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "Choose archive storage to read", exact: true }).click();
      const dialog = page.getByRole("dialog");
      const rawInspections = state.inspections.length;
      await select(page, dialog, "Tape device", "drive-a");
      await expect(dialog.getByRole("button", { name: "Start restore", exact: true })).toBeEnabled();
      expect(state.cartridgeInspections).toBe(index + 1);
      expect(state.inspections).toHaveLength(rawInspections);
      if (format === "ltfs_v0") {
        await expect(page.getByRole("listbox")).toHaveCount(0);
        await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`ltfs-v0-read-${viewport.name}.png`) });
      }
      await dialog.getByRole("button", { name: "Start restore", exact: true }).click();
      await expect.poll(() => state.reads.length).toBe(index + 5);
      expect(state.reads.at(-1)?.expectedMediaId).toBe(7n);
    }
    expect(state.unexpected).toEqual([]);
  });
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "narrow", width: 390, height: 844 },
]) {
  test(`a tar automatic-identification failure retains the form for an explicit Media hint on ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    state.requireHint = true;
    await page.goto("/jobs/21");
    await expect(page.getByText("Tar cartridge: pending", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Choose archive storage to read", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await select(page, dialog, "Tape device", "drive-a");
    await dialog.getByRole("button", { name: "Start restore", exact: true }).click();
    await expect(dialog.getByText("Select a Media hint before reading this Tape.")).toBeVisible();
    await expect(dialog.getByRole("combobox", { name: "Tape device" })).toContainText("drive-a");
    await expect(dialog.getByRole("combobox", { name: "Tape Media" })).toContainText("Automatic identification");
    await select(page, dialog, "Tape Media", "Tar cartridge · ABC123");
    await dialog.getByRole("button", { name: "Start restore", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    expect(state.reads).toHaveLength(2);
    expect(state.reads[1].backend.oneofKind === "tape" && state.reads[1].backend.tape.mediaId).toBe(7n);
    expect(state.cartridgeInspections).toBe(0);
    expect(state.writes).toEqual([]);
    expect(state.unexpected).toEqual([]);
  });
}

for (const viewport of [
  { name: "desktop", width: 1440, height: 1000 },
  { name: "narrow", width: 390, height: 844 },
]) {
  test(`tar read preparation scopes refresh and reports unqualified Auto on ${viewport.name}`, async ({ page }, testInfo) => {
    await page.setViewportSize(viewport);
    const state = await fixture(page);
    for (const [id, openName, startName] of [
      [21, "Choose archive storage to read", "Start restore"],
      [22, "Choose Media to read", "Start reading"],
    ] as const) {
      await page.goto(`/jobs/${id}`);
      if (id === 21) await expect(page.getByText("Tar cartridge: pending", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: openName, exact: true }).click();
      const dialog = page.getByRole("dialog");
      await select(page, dialog, "Tape device", "drive-a");
      await dialog.getByRole("button", { name: /^Advanced/ }).click();
      const refresh = dialog.getByRole("checkbox", { name: "Refresh position map before reading" });
      await expect(refresh).toBeDisabled();
      await expect(dialog.getByRole("button", { name: startName, exact: true })).toBeEnabled();
      await expect(page.getByRole("listbox")).toHaveCount(0);
      await refresh.scrollIntoViewIfNeeded();
      await expectFits(dialog);
      await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-read-refresh-${id}-${viewport.name}.png`) });
      await dialog.getByRole("button", { name: "Device details", exact: true, expanded: false }).click();
      const details = dialog;
      await expect(details.getByText("No qualified geometry/timing model; Auto falls back to Logical")).toBeVisible();
      await expect(details.getByText("Fresh: Unknown", { exact: true })).toBeVisible();
      await details.getByText("software read order", { exact: true }).scrollIntoViewIfNeeded();
      await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-read-order-${id}-${viewport.name}.png`) });
      await details.getByRole("button", { name: "Device details", exact: true, expanded: true }).click();
      await dialog.getByRole("button", { name: startName, exact: true }).click();
      await expect.poll(() => state.reads.length).toBe(id === 21 ? 1 : 2);
      const submitted = state.reads.at(-1)!;
      expect(submitted.backend.oneofKind === "tape" && submitted.backend.tape.refreshPositionMap).toBe(false);
    }
    // A qualified synthetic protocol fixture exercises the request action without qualifying real H6/H7/reload hardware.
    state.qualifiedReload = true;
    await page.getByRole("button", { name: "Choose Media to read", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await select(page, dialog, "Tape device", "drive-a");
    await dialog.getByRole("button", { name: /^Advanced/ }).click();
    await dialog.getByRole("checkbox", { name: "Refresh position map before reading" }).check();
    await dialog.getByRole("button", { name: "Start reading", exact: true }).click();
    await expect.poll(() => state.reads.length).toBe(3);
    const refreshed = state.reads.at(-1)!;
    expect(refreshed.backend.oneofKind === "tape" && refreshed.backend.tape.refreshPositionMap).toBe(true);
    state.scanResultPolicy = ScanResultPolicy.PUBLISH_INVENTORY;
    await page.getByRole("button", { name: "Choose Media to read", exact: true }).click();
    await select(page, dialog, "Tape device", "drive-a");
    await expect(dialog.getByRole("button", { name: "Start reading", exact: true })).toBeEnabled();
    await expect(dialog.getByRole("checkbox", { name: "Refresh position map before reading" })).toHaveCount(0);
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    for (const format of ["ltfs_v0", "ltfs_v1"]) {
      state.readTape = Media.create({ ...ltfsTape, profile: { kind: { oneofKind: "tape", tape: TapeMediaProfile.create({ format }) } } });
      await page.goto("/jobs/21");
      await expect(page.getByText("Tar cartridge: pending", { exact: true })).toBeVisible();
      await page.getByRole("button", { name: "Choose archive storage to read", exact: true }).click();
      await select(page, dialog, "Tape device", "drive-a");
      await expect(dialog.getByRole("button", { name: "Start restore", exact: true })).toBeEnabled();
      await expect(dialog.getByRole("checkbox", { name: "Refresh position map before reading" })).toHaveCount(0);
      await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    }
    expect(state.unexpected).toEqual([]);
  });
}

test("tar refresh accepts protocol eligibility independently of physical validation and read order", async ({ page }, testInfo) => {
  const state = await fixture(page);
  state.qualifiedReload = true;
  const dialog = page.getByRole("dialog");
  const refresh = dialog.getByRole("checkbox", { name: "Refresh position map before reading" });
  const open = async (id: 21 | 22) => {
    await page.goto(`/jobs/${id}`);
    await page.getByRole("button", { name: id === 21 ? "Choose archive storage to read" : "Choose Media to read", exact: true }).click();
    await select(page, dialog, "Tape device", "drive-a");
    await expect(page.locator('[role="listbox"]')).toHaveCount(0);
    const advanced = dialog.getByRole("button", { name: /^Advanced/ });
    if (await advanced.count()) await advanced.click();
  };
  const submit = async (id: 21 | 22, expectedRefresh: boolean) => {
    const previous = state.reads.length;
    await dialog.getByRole("button", { name: id === 21 ? "Start restore" : "Start reading", exact: true }).click();
    await expect.poll(() => state.reads.length).toBe(previous + 1);
    const target = state.reads.at(-1)!;
    expect(target.backend.oneofKind === "tape" && target.backend.tape.refreshPositionMap).toBe(expectedRefresh);
    await expect(dialog).toHaveCount(0);
  };

  // Both non-FAILED observations allow an explicit action; opening either form still leaves it off.
  for (const [id, validation] of [
    [21, DeviceFeatureValidation.UNVALIDATED],
    [22, DeviceFeatureValidation.UNSPECIFIED],
  ] as const) {
    state.reloadValidation = validation;
    await open(id);
    await expect(refresh).toBeEnabled();
    await expect(refresh).not.toBeChecked();
    await expect(dialog.getByText(/The map is not guaranteed to be fresh/)).toBeVisible();
    await refresh.check();
    await expectFits(dialog);
    await page.screenshot({ animations: "disabled", path: testInfo.outputPath(`tar-refresh-explicit-${validation}.png`) });
    await submit(id, true);
  }

  // Failed validation and unsupported protocol keep ordinary reading available without refresh.
  for (const [support, validation] of [
    [DeviceFeatureSupport.SUPPORTED, DeviceFeatureValidation.FAILED],
    [DeviceFeatureSupport.UNSUPPORTED, DeviceFeatureValidation.UNVALIDATED],
  ] as const) {
    state.reloadSupport = support;
    state.reloadValidation = validation;
    await open(21);
    await expect(refresh).toBeDisabled();
    await expect(refresh).not.toBeChecked();
    await expect(dialog.getByRole("button", { name: "Start restore", exact: true })).toBeEnabled();
    await submit(21, false);
  }

  // Saving Auto through the rendered Settings form never selects the separate preparation action.
  state.reloadSupport = DeviceFeatureSupport.SUPPORTED;
  state.reloadValidation = DeviceFeatureValidation.UNVALIDATED;
  await page.goto("/settings/devices");
  await select(page, page, "Read order", "Auto");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Device settings saved.")).toBeVisible();
  expect(state.settings.devices[0].options.oneofKind === "tape" && state.settings.devices[0].options.tape.readOrder).toBe(TapeReadOrder.AUTO);
  await open(21);
  await expect(refresh).toBeEnabled();
  await expect(refresh).not.toBeChecked();
  await submit(21, false);

  // The same positive device report cannot expose refresh for inventory Scan or LTFS Restore.
  state.scanResultPolicy = ScanResultPolicy.PUBLISH_INVENTORY;
  await open(22);
  await expect(refresh).toHaveCount(0);
  await submit(22, false);
  state.readTape = ltfsTape;
  await open(21);
  await expect(refresh).toHaveCount(0);
  await submit(21, false);
  expect(state.cartridgeInspections).toBe(1);
  expect(state.writes).toEqual([]);
  expect(state.unexpected).toEqual([]);
  await testInfo.attach("refresh-submitted-targets.json", {
    contentType: "application/json",
    body: JSON.stringify({ targets: state.reads.map((target) => ReadMediaTarget.toJson(target)), settings: DeviceSettings.toJson(state.settings) }, null, 2),
  });
});
