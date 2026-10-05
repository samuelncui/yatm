import { expect, test, type Locator, type Page } from "@playwright/test";

const rows = (pane: Locator) => pane.locator("[data-chonky-file-id]");
const scroller = (pane: Locator) => pane.locator('[data-virtuoso-scroller="true"]');
const loaded = async (pane: Locator) => {
  await expect(rows(pane).first()).toBeVisible();
  await expect(pane.locator("[data-chonky-sparse-placeholder]")).toHaveCount(0);
};

async function findShared(page: Page) {
  await page.goto("/tools/identical?source=locations");
  await page.getByRole("combobox", { name: "Locations", exact: true }).fill("Shared files");
  await page.getByRole("option", { name: "Shared files", exact: true }).click();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Find", exact: true }).click();
  const pane = page.getByRole("region", { name: "Identical file groups" });
  await loaded(pane);
  return pane;
}

async function sort(pane: Locator, name: string) {
  await pane.getByRole("button", { name: "Options", exact: true }).click();
  await pane
    .page()
    .getByRole("menuitem", { name: new RegExp(`^Sort by ${name}`) })
    .click();
  await loaded(pane);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/file");
  await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
});

test("Identical pages, sorts, hides and collapses without another global Find", async ({ page }) => {
  let finds = 0;
  page.on("request", (request) => {
    if (request.url().endsWith("/FindIdentical")) finds++;
  });
  const pane = await findShared(page);
  const first = rows(pane).first();
  await first.click();
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();

  await pane.getByRole("button", { name: "Options", exact: true }).click();
  await expect(page.getByRole("menuitem", { name: /Sort by date|Switch to|Show folders first/ })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await sort(pane, "name");
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();
  await sort(pane, "size");
  await pane.getByRole("button", { name: "Options", exact: true }).click();
  await expect(page.getByRole("menuitem", { name: "Sort by size ↓", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");

  for (const fraction of [0.9, 0.35, 0.7, 0]) {
    const previous = await rows(pane).first().getAttribute("data-chonky-file-id");
    await scroller(pane).evaluate((element, fraction) => element.scrollTo({ top: element.scrollHeight * fraction }), fraction);
    await expect(rows(pane).first()).not.toHaveAttribute("data-chonky-file-id", previous!);
    await loaded(pane);
    expect(await rows(pane).count()).toBeLessThan(100);
  }
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();
  await page.getByRole("switch", { name: "Show hidden files" }).check();
  await loaded(pane);
  await expect(pane.getByRole("checkbox", { name: /Select \.member-/ }).first()).toBeVisible();
  const group = pane.locator("[data-chonky-group-id] button[aria-expanded]").first();
  await group.click();
  await expect(group).toHaveAttribute("aria-expanded", "false");
  await group.click();
  await expect(group).toHaveAttribute("aria-expanded", "true");
  await loaded(pane);
  await expect(page.getByText(/Could not load some rows|needs a new Find/)).toHaveCount(0);
  expect(finds).toBe(1);
});

test("ordinary file panes expose working list options", async ({ page }) => {
  await page.getByRole("button", { name: "Dual pane", exact: true }).click();
  for (const name of ["Left file pane", "Right file pane"]) {
    const pane = page.getByRole("region", { name });
    await loaded(pane);
    await pane.getByRole("button", { name: "Options", exact: true }).click();
    await expect(page.getByRole("menuitem", { name: /Switch to/ })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await sort(pane, "size");
    await sort(pane, "date");
    await sort(pane, "name");
  }
});

test("another Delete starts while the previous response is pending, without another Find", async ({ page }) => {
  let finds = 0;
  let deletes = 0;
  let releaseFirst!: () => void;
  const firstResponse = new Promise<void>((resolve) => (releaseFirst = resolve));
  await page.route("**/yatm.v1.FilesService/Remove", async (route) => {
    const index = ++deletes;
    const response = await route.fetch();
    if (index === 1) await firstResponse;
    await route.fulfill({ response });
  });
  page.on("request", (request) => {
    if (request.url().endsWith("/FindIdentical")) finds++;
  });
  const pane = await findShared(page);
  const first = rows(pane).first();
  const id = await first.getAttribute("data-chonky-file-id");
  const secondID = await rows(pane).nth(1).getAttribute("data-chonky-file-id");
  await first.click();
  await first.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Delete", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
  try {
    await expect.poll(() => deletes).toBe(1);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(pane.locator(`[data-chonky-file-id="${id}"]`)).toContainText("Deleting…");
    const second = pane.locator(`[data-chonky-file-id="${secondID}"]`);
    await second.click();
    await second.click({ button: "right" });
    await page.getByRole("menuitem", { name: "Delete", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Delete", exact: true }).click();
    await expect.poll(() => deletes).toBe(2);
    await expect(second).toHaveCount(0);
  } finally {
    releaseFirst();
  }
  await expect(pane.locator(`[data-chonky-file-id="${id}"]`)).toHaveCount(0);
  await loaded(pane);
  expect(finds).toBe(1);
});
