import { expect, test, type Locator, type Route } from "@playwright/test";
import {
  DeviceFeature,
  DeviceFeatureAvailability,
  DeviceFeatureEvidence,
  DeviceFeatureKind,
  DeviceFeatureSupport,
  DeviceFeatureValidation,
  DeviceKind,
  DeviceReason,
  DeviceSummary,
  InspectDeviceRequest,
  InspectDeviceResponse,
  ListDevicesResponse,
  ListLocationsResponse,
  ListMediaResponse,
  Media,
  MediaKind,
} from "../src/entity";

// Mocked browser consumer validation: serve real generated gRPC messages to the app,
// without a Tape backend, cartridge inspection, or release-package acceptance.
test("Media device details preserve passive diagnostics and usable desktop/narrow dialogs", async ({ page }, testInfo) => {
  const devices = [
    DeviceSummary.create({ device: "/dev/busy-drive", kind: DeviceKind.TAPE, busy: true, detail: "Archive owns the device" }),
    DeviceSummary.create({ device: "/dev/free-drive", kind: DeviceKind.TAPE }),
    DeviceSummary.create({ device: "/dev/other-drive", kind: DeviceKind.TAPE }),
  ];
  const tape = Media.create({ id: 7n, name: "Browser fixture Tape", kind: MediaKind.TAPE, identity: "ABC001" });
  const unexpected: string[] = [];
  let deviceLists = 0;
  const inspections: string[] = [];
  const fulfill = async (route: Route, data: Uint8Array) => {
    const header = Buffer.alloc(5);
    header.writeUInt32BE(data.length, 1);
    const trailer = Buffer.from("grpc-status: 0\r\n");
    const trailerHeader = Buffer.alloc(5);
    trailerHeader[0] = 0x80;
    trailerHeader.writeUInt32BE(trailer.length, 1);
    await route.fulfill({ contentType: "application/grpc-web+proto", body: Buffer.concat([header, data, trailerHeader, trailer]) });
  };
  await page.route("**/services/**", async (route) => {
    const method = new URL(route.request().url()).pathname.replace(/^\/services\//, "");
    switch (method) {
      case "yatm.v1.LocationService/List":
        return fulfill(route, ListLocationsResponse.toBinary(ListLocationsResponse.create()));
      case "yatm.v1.MediaService/List":
        return fulfill(route, ListMediaResponse.toBinary(ListMediaResponse.create({ media: [tape] })));
      case "yatm.v1.MediaService/ListDevices":
        deviceLists++;
        return fulfill(route, ListDevicesResponse.toBinary(ListDevicesResponse.create({ devices })));
      case "yatm.v1.MediaService/InspectDevice": {
        const request = InspectDeviceRequest.fromBinary(route.request().postDataBuffer()!.subarray(5));
        inspections.push(request.device);
        const device = devices.find((value) => value.device === request.device)!;
        const reply = device.busy
          ? InspectDeviceResponse.create({ device, reason: DeviceReason.BUSY, detail: "No observation while busy" })
          : InspectDeviceResponse.create({
              device,
              model: "Passive browser fixture",
              accessIdentity: "reported-device-identity/".repeat(8),
              report: {
                oneofKind: "tape",
                tape: {
                  writeProtected: false,
                  encryptionEnabled: false,
                  recommendationDetail: "",
                  features: [
                    DeviceFeature.create({
                      feature: DeviceFeatureKind.LOCATE,
                      support: DeviceFeatureSupport.SUPPORTED,
                      availability: DeviceFeatureAvailability.UNKNOWN,
                      validation: DeviceFeatureValidation.UNVALIDATED,
                      evidence: DeviceFeatureEvidence.REPORTED,
                    }),
                    DeviceFeature.create({
                      feature: DeviceFeatureKind.HARDWARE_ENCRYPTION,
                      support: DeviceFeatureSupport.UNKNOWN,
                      availability: DeviceFeatureAvailability.UNAVAILABLE,
                      validation: DeviceFeatureValidation.UNVALIDATED,
                      reason: DeviceReason.NOT_IMPLEMENTED,
                    }),
                  ],
                },
              },
            });
        return fulfill(route, InspectDeviceResponse.toBinary(reply));
      }
      default:
        // Any cartridge Inspect or physical-operation request fails this consumer test.
        unexpected.push(method);
        await route.abort();
    }
  });
  const expectFits = async (dialog: Locator) => {
    const dimensions = await dialog.evaluate((element) => {
      const bounds = element.getBoundingClientRect();
      return {
        left: bounds.left,
        right: bounds.right,
        viewport: window.innerWidth,
        dialogOverflow: element.scrollWidth > element.clientWidth + 1,
        pageOverflow: document.documentElement.scrollWidth > window.innerWidth + 1,
      };
    });
    expect(dimensions.left).toBeGreaterThanOrEqual(0);
    expect(dimensions.right).toBeLessThanOrEqual(dimensions.viewport);
    expect(dimensions.dialogOverflow).toBe(false);
    expect(dimensions.pageOverflow).toBe(false);
  };

  for (const viewport of [
    { label: "desktop", width: 1440, height: 1000 },
    { label: "narrow", width: 390, height: 844 },
  ]) {
    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto("/media");
    await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
    await page.locator('[data-chonky-file-id="7"]').click();
    await page.getByRole("button", { name: "Actions", exact: true }).click();
    await page.getByRole("menuitem", { name: "Properties", exact: true }).click();
    const media = page.getByRole("dialog", { name: "Browser fixture Tape · Properties" });
    await expect(media.getByRole("button", { name: "Refresh devices" })).toBeEnabled();
    await media.getByRole("combobox", { name: "Tape device" }).click();
    await expect(page.getByRole("option", { name: "/dev/busy-drive · Archive owns the device" })).toHaveAttribute("aria-disabled", "true");
    await expect(page.getByRole("option", { name: "/dev/free-drive", exact: true })).not.toHaveAttribute("aria-disabled", "true");
    await page.keyboard.press("Escape");
    const previousLists = deviceLists;
    const previousInspections = inspections.length;
    await media.getByRole("button", { name: "Refresh devices" }).click();
    await expect.poll(() => deviceLists).toBe(previousLists + 1);
    await expect(media.getByRole("button", { name: "Refresh devices" })).toBeEnabled();
    expect(inspections).toHaveLength(previousInspections);
    await expectFits(media);

    await media.getByRole("button", { name: "Device details", exact: true }).click();
    const details = page.getByRole("dialog", { name: "Device details", exact: true });
    await expect(details.getByText("No observation while busy", { exact: false })).toBeVisible();
    await expect(details.getByText("Observed: Unknown", { exact: true })).toBeVisible();
    await details.getByRole("combobox", { name: "Device to inspect" }).click();
    await page.getByRole("option", { name: "/dev/free-drive", exact: true }).click();
    await expect(page.getByRole("listbox")).toHaveCount(0);
    await expect(details.getByText("Model: Passive browser fixture", { exact: true })).toBeVisible();
    await expect(details.getByText("Medium present: Unknown", { exact: true })).toBeVisible();
    await expect(details.getByText("Write protected: false", { exact: true })).toBeVisible();
    await expect(details.getByText("Encryption enabled: false", { exact: true })).toBeVisible();
    await expect(details.getByText(/Support: supported · Availability: unknown · Validation: unvalidated/)).toBeVisible();
    await expect(details.getByText("Evidence: reported", { exact: true })).toBeVisible();
    await expect(details.getByText("Reason: not implemented", { exact: true })).toBeVisible();
    await expectFits(details);
    await page.screenshot({ path: testInfo.outputPath(`tape-device-${viewport.label}.png`) });
    await details.getByText("Reason: not implemented", { exact: true }).scrollIntoViewIfNeeded();
    await expectFits(details);
    await page.screenshot({ path: testInfo.outputPath(`tape-device-${viewport.label}-features.png`) });
    await details.getByRole("button", { name: "Refresh device details" }).click();
    await expect.poll(() => inspections.length).toBe(previousInspections + 3);
    await details.getByRole("button", { name: "Close", exact: true }).click();
    await expect(details).toHaveCount(0);
    await media.getByRole("button", { name: "Close details", exact: true }).click();
    await expect(media).toHaveCount(0);
  }
  expect(unexpected).toEqual([]);
});
