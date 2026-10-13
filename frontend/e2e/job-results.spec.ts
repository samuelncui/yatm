import { expect, test } from "@playwright/test";
import { JobKind, JobResultOrder, ListScanJobEntriesRequest, ListScanJobEntriesResponse, ScanEntry } from "../src/entity";
import type { JobServiceClient } from "../src/entity";

// Exercise the real virtualizer and row frame against a large, delayed manifest.
// The isolated Demo supplies the normal Job page; only its result RPC is replaced.
test("Job results keep their scrollbar when long rows replace unloaded windows", async ({ page }) => {
  const total = 100_000;
  const requests: ListScanJobEntriesRequest[] = [];
  let release: (() => void) | undefined;
  let delayNext = false;
  await page.route("**/yatm.v1.ScanJobService/ListEntries", async (route) => {
    const request = ListScanJobEntriesRequest.fromBinary(route.request().postDataBuffer()!.subarray(5));
    requests.push(request);
    const backwards = request.order === JobResultOrder.DESCENDING;
    const first = request.cursor ? Number(request.cursor) + (backwards ? -1 : 1) : Number(request.offset ?? 0n) + 1;
    const entries = Array.from({ length: request.limit }, (_, index) => first + index * (backwards ? -1 : 1))
      .filter((id) => id >= 1 && id <= total)
      .map((id) => ScanEntry.create({ id: BigInt(id), path: `${"long folder name/".repeat(id % 8)}/file-${id}.txt` }));
    const data = ListScanJobEntriesResponse.toBinary(
      ListScanJobEntriesResponse.create({ entries, totalEntryCount: request.includeTotal ? BigInt(total) : undefined, hasMore: true }),
    );
    const header = Buffer.alloc(5);
    header.writeUInt32BE(data.length, 1);
    const trailer = Buffer.from("grpc-status: 0\r\n");
    const trailerHeader = Buffer.alloc(5);
    trailerHeader[0] = 0x80;
    trailerHeader.writeUInt32BE(trailer.length, 1);
    if (delayNext) {
      delayNext = false;
      await new Promise<void>((resolve) => (release = resolve));
    }
    await route.fulfill({
      contentType: "application/grpc-web+proto",
      body: Buffer.concat([header, data, trailerHeader, trailer]),
    });
  });
  await page.setViewportSize({ width: 700, height: 800 });
  await page.goto("/jobs");
  await page.getByRole("button", { name: "Results", exact: true }).first().waitFor();
  await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
  const id = await page.evaluate(async (kind) => {
    const client = (window as unknown as { cli: { jobs: JobServiceClient } }).cli.jobs;
    const response = await client.list({ filter: { kind } }).response;
    return response.jobs[0]?.id.toString();
  }, JobKind.SCAN);
  expect(id).toBeTruthy();
  await page.goto(`/jobs/${id}`);
  await page.getByRole("button", { name: "Results", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Scan results" });
  const scroller = dialog.locator('[data-virtuoso-scroller="true"]');
  await expect(dialog.getByText(/file-1\.txt$/, { exact: false })).toBeVisible();
  const geometry = () => scroller.evaluate((node) => ({ height: node.scrollHeight, top: node.scrollTop }));
  const initial = await geometry();
  // Fixed rows make total height exact, independent of which windows are loaded.
  expect(initial.height).toBe(total * 54);

  delayNext = true;
  await scroller.evaluate((node) => node.scrollTo({ top: 50_000 * 54 }));
  await expect.poll(() => Boolean(release)).toBe(true);
  const pending = await geometry();
  release!();
  await expect(dialog.getByRole("progressbar", { name: "Loading Job results" })).toHaveCount(0);
  await expect.poll(geometry).toEqual(pending);
  expect(pending.height).toBe(initial.height);
  expect(requests.filter((request) => request.includeTotal)).toHaveLength(1);
  expect(requests.some((request) => request.offset !== undefined)).toBe(true);

  // Scrolling up a viewport uses the nearby leading cursor, not another offset.
  const lastOffset = requests.filter((request) => request.offset !== undefined).at(-1)!;
  await scroller.evaluate((node, top) => node.scrollTo({ top }), Math.max(0, Number(lastOffset.offset) - 15) * 54);
  await expect.poll(() => requests.some((request) => request.order === JobResultOrder.DESCENDING)).toBe(true);
  await expect(dialog.getByRole("progressbar", { name: "Loading Job results" })).toHaveCount(0);
  expect((await geometry()).height).toBe(initial.height);
});
