import { expect, test, type Locator, type Page } from "@playwright/test";

// Inspect actual scrollbars rather than Chromium's headless hidden-scrollbar mode.
test.use({ launchOptions: { ignoreDefaultArgs: ["--hide-scrollbars"] } });

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
  await expect(pane.getByRole("progressbar", { name: "Sorting files" })).toHaveCount(0);
  await loaded(pane);
}

const rowNames = (pane: Locator) => rows(pane).evaluateAll((elements) => elements.map((element) => element.querySelector("[title]")?.getAttribute("title")));
const sortIcon = { asc: "arrow-down-short-wide", desc: "arrow-up-short-wide" };

async function expectOptions(pane: Locator, sort: string, direction: "asc" | "desc", foldersFirst?: boolean) {
  const page = pane.page();
  await pane.getByRole("button", { name: "Options", exact: true }).click();
  const active = page.getByRole("menuitem", { name: `Sort by ${sort}`, exact: true });
  await expect(active).toHaveClass(/chonky-activeButton/);
  await expect(active.locator("svg")).toHaveAttribute("data-icon", sortIcon[direction]);
  if (foldersFirst !== undefined)
    await expect(page.getByRole("menuitem", { name: "Show folders first", exact: true }).locator("svg")).toHaveAttribute(
      "data-icon",
      foldersFirst ? "toggle-on" : "toggle-off",
    );
  await page.keyboard.press("Escape");
}

async function chooseSource(pane: Locator, name: string) {
  await pane.getByRole("button", { name: "Choose file source" }).click();
  await pane.page().getByRole("menuitem", { name, exact: true }).click();
  await loaded(pane);
}

async function expectSmallestGroup(pane: Locator) {
  await scroller(pane).evaluate((element) => element.scrollTo({ top: 0 }));
  await loaded(pane);
  await expect(rows(pane).first()).toContainText(" · 15 B");
  const names = (await rowNames(pane)).slice(0, 2);
  expect(names[0]).toMatch(/^group-\d+-a\.txt$/);
  expect(names[1]).toBe(names[0]!.replace(/-a\.txt$/, "-b.txt"));
  // Equal-size groups tie by component ID, so no particular title comes first.
  await expect(pane.locator("[data-chonky-group-id] button[aria-expanded]").first()).toHaveAttribute("aria-label", names[0]!);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/file");
  await expect(page.locator('meta[name="yatm-commit"]')).toHaveAttribute("content", process.env.YATM_BROWSER_COMMIT!);
});

test("Identical has a visible scrollbar without extra content padding on first display", async ({ page }) => {
  const pane = await findShared(page);
  await expect(rows(pane).getByRole("checkbox")).toHaveCount(0);
  for (const width of [1440, 1000]) {
    await page.setViewportSize({ width, height: 1000 });
    await loaded(pane);
    const geometry = await scroller(pane).evaluate((element) => {
      const bounds = element.getBoundingClientRect();
      const clearEdge = bounds.left + element.clientLeft + element.clientWidth;
      const row = element.querySelector("[data-chonky-file-id]")!;
      return {
        scrollbarWidth: (element as HTMLElement).offsetWidth - element.clientWidth,
        unusedContentWidth: clearEdge - row.getBoundingClientRect().right,
        horizontalOverflow: element.scrollWidth > element.clientWidth,
        covered: [...element.querySelectorAll("[data-chonky-group-id] button, [data-chonky-file-id]")].filter(
          (child) => child.getBoundingClientRect().right > clearEdge + 1,
        ).length,
      };
    });
    expect(geometry.scrollbarWidth).toBeGreaterThan(0);
    expect(Math.abs(geometry.unusedContentWidth)).toBeLessThanOrEqual(1);
    expect(geometry.horizontalOverflow).toBe(false);
    expect(geometry.covered).toBe(0);
  }
});

test("Identical sorts whole groups by size without blank rows, retaining selection, paging and collapse", async ({ page }) => {
  let finds = 0;
  page.on("request", (request) => {
    if (request.url().endsWith("/FindIdentical")) finds++;
  });
  const pane = await findShared(page);
  const groups = pane.locator("[data-chonky-group-id] button[aria-expanded]");
  // The Demo has 51-byte Large group members, 27-byte notes, 23-byte checklists,
  // and 15/16-byte Many groups members. Size order must move their headers.
  await expect(rows(pane).first()).toContainText("Large group/");
  await expectOptions(pane, "size", "desc");
  await rows(pane).first().click();
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();
  // Chonky scrolls the selected member into view. Return to the beginning before
  // asserting the first group header; sorting must retain that viewport.
  await scroller(pane).evaluate((element) => element.scrollTo({ top: 0 }));
  await expect.poll(() => scroller(pane).evaluate((element) => element.scrollTop)).toBe(0);
  // The virtualizer may trim offscreen rows after measuring their real heights.
  // Pending sort must retain the visible content, not a particular overscan count.
  const visibleNames = () =>
    rows(pane).evaluateAll((elements) => {
      const viewport = elements[0].closest('[data-virtuoso-scroller="true"]')!.getBoundingClientRect();
      return elements
        .filter((element) => {
          const row = element.getBoundingClientRect();
          return row.top < viewport.bottom && row.bottom > viewport.top;
        })
        .map((element) => element.querySelector("[title]")?.getAttribute("title"));
    });
  const initialNames = await visibleNames();
  const initialViewport = await scroller(pane).boundingBox();
  expect(initialNames).toEqual([...initialNames].sort());
  await pane.getByRole("button", { name: "Options", exact: true }).click();
  await expect(page.getByRole("menuitem", { name: /^Sort by/ })).toHaveCount(1);
  await expect(page.getByRole("menuitem", { name: /Switch to|Show folders first/ })).toHaveCount(0);
  await page.keyboard.press("Escape");

  // Delay a real server page, so the assertion covers the pending sort as well as
  // the final order. Old rows stay usable until the new viewport is ready.
  let release!: () => void;
  let pending = false;
  const responseReady = new Promise<void>((resolve) => (release = resolve));
  await page.route(
    "**/yatm.v1.FilesService/ListIdenticalRows",
    async (route) => {
      const response = await route.fetch();
      pending = true;
      await responseReady;
      await route.fulfill({ response });
    },
    { times: 1 },
  );
  await pane.getByRole("button", { name: "Options", exact: true }).click();
  await page.getByRole("menuitem", { name: "Sort by size", exact: true }).click();
  try {
    await expect.poll(() => pending).toBe(true);
    await expect(pane.getByRole("progressbar", { name: "Sorting files" })).toBeVisible();
    await expect.poll(visibleNames).toEqual(initialNames);
    expect(await scroller(pane).boundingBox()).toEqual(initialViewport);
    expect(await scroller(pane).evaluate((element) => element.scrollTop)).toBe(0);
    await expect(pane.locator("[data-chonky-sparse-placeholder]")).toHaveCount(0);
  } finally {
    release();
  }
  await expect(pane.getByRole("progressbar", { name: "Sorting files" })).toHaveCount(0);
  await loaded(pane);
  await expectSmallestGroup(pane);
  await expectOptions(pane, "size", "asc");
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();

  for (const fraction of [0.9, 0.35, 0.7, 0]) {
    const previous = await rows(pane).first().getAttribute("data-chonky-file-id");
    await scroller(pane).evaluate((element, fraction) => element.scrollTo({ top: element.scrollHeight * fraction }), fraction);
    await expect(rows(pane).first()).not.toHaveAttribute("data-chonky-file-id", previous!);
    await loaded(pane);
    expect(await rows(pane).count()).toBeLessThan(100);
    if (fraction === 0.9) await expect(rows(pane).first()).toContainText("Large group/");
  }
  await expectSmallestGroup(pane);
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();

  // Collapse a different group: collapsing the active group intentionally clears
  // Chonky's selection. This interval must follow its group across global orders.
  const collapsedId = await pane.locator("[data-chonky-group-id]").first().getAttribute("data-chonky-group-id");
  await groups.first().click();
  const collapsedGroup = pane.locator(`[data-chonky-group-id="${collapsedId}"] button[aria-expanded]`);
  await expect(collapsedGroup).toHaveAttribute("aria-expanded", "false");
  await sort(pane, "size");
  await expect(rows(pane).first()).toContainText("Large group/");
  await expectOptions(pane, "size", "desc");
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();
  await sort(pane, "size");
  await expect(collapsedGroup).toHaveAttribute("aria-expanded", "false");
  await collapsedGroup.click();
  await expectSmallestGroup(pane);
  await sort(pane, "size");
  await expect(rows(pane).first()).toContainText("Large group/");
  await expect(pane.getByText("1 selected", { exact: true })).toBeVisible();
  // The switch commits together with its asynchronously replaced rows.
  const hidden = page.getByRole("switch", { name: "Show hidden files" });
  await hidden.click();
  await expect(hidden).toBeChecked();
  await loaded(pane);
  await expect(pane.getByRole("listitem", { name: /^\.member-/ }).first()).toBeVisible();
  const names = await rowNames(pane);
  expect(names).toEqual([...names].sort());
  await expect(page.getByText(/Could not load some rows|needs a new Find/)).toHaveCount(0);
  expect(finds).toBe(1);
});

test("Identical remembers only its size direction across page changes and reload", async ({ page }) => {
  const ordinary = page.getByRole("region", { name: "Left file pane" });
  await loaded(ordinary);
  await sort(ordinary, "size");
  await expectOptions(ordinary, "size", "desc", true);
  let pane = await findShared(page);
  await expectOptions(pane, "size", "desc");
  await sort(pane, "size");
  await expectOptions(pane, "size", "asc");
  await expect(rows(pane).first()).toContainText("Many groups/");

  await page.getByRole("tab", { name: "Library", exact: true }).click();
  await loaded(ordinary);
  await expectOptions(ordinary, "size", "desc", true);
  await page.getByRole("tab", { name: "Tools", exact: true }).click();
  pane = page.getByRole("region", { name: "Identical file groups" });
  await page.getByRole("button", { name: "Find", exact: true }).click();
  await loaded(pane);
  await expectOptions(pane, "size", "asc");
  // Find after a full page reload uses the persisted list preference too.
  pane = await findShared(page);
  await expectOptions(pane, "size", "asc");
  await expectSmallestGroup(pane);
});

test("Identical shows one loading indicator and quiet gaps during a delayed scroll", async ({ page }) => {
  const pane = await findShared(page);
  let release!: () => void;
  let pending = false;
  const ready = new Promise<void>((resolve) => (release = resolve));
  await page.route("**/yatm.v1.FilesService/ListIdenticalRows", async (route) => {
    const response = await route.fetch();
    pending = true;
    await ready;
    await route.fulfill({ response });
  });
  try {
    await scroller(pane).evaluate((node) => node.scrollTo({ top: node.scrollHeight * 0.8 }));
    await expect.poll(() => pending).toBe(true);
    await expect(pane.getByRole("progressbar", { name: "Loading files" })).toHaveCount(1);
    expect(await pane.locator("[data-chonky-sparse-placeholder]").count()).toBeGreaterThan(0);
    await expect(pane.getByText(/^Loading[.…]*$/)).toHaveCount(0);
    const position = await scroller(pane).evaluate((node) => node.scrollTop);
    const screenshot = test.info().outputPath("identical-pending.png");
    await pane.screenshot({ path: screenshot });
    await test.info().attach("identical-pending", { path: screenshot, contentType: "image/png" });
    release();
    await loaded(pane);
    await expect(pane.getByRole("progressbar")).toHaveCount(0);
    expect(await scroller(pane).evaluate((node) => node.scrollTop)).toBe(position);
  } finally {
    release();
  }
});

test("ordinary ordering and menu preferences stay with each list across paths, pages and reload", async ({ page }) => {
  await page.getByRole("button", { name: "Dual pane", exact: true }).click();
  const left = page.getByRole("region", { name: "Left file pane" });
  const right = page.getByRole("region", { name: "Right file pane" });
  const folders = ["Large group", "Many groups", "notes"];
  const files = ["a.txt", "b.txt", "checklist-backup.txt", "checklist-copy.txt", "checklist-final.txt", "checklist.txt"];
  for (const pane of [left, right]) {
    await chooseSource(pane, "Shared files");
    await expect.poll(() => rowNames(pane)).toEqual([...folders, ...files]);
    await expectOptions(pane, "name", "asc", true);
  }

  await sort(left, "size");
  await expect.poll(() => rowNames(left)).toEqual([...folders, "a.txt", ...files.slice(2), "b.txt"]);
  await expectOptions(left, "size", "desc", true);
  await left.getByRole("button", { name: "Options", exact: true }).click();
  await page.getByRole("menuitem", { name: "Show folders first", exact: true }).click();
  await sort(left, "size");
  const leftOrder = ["b.txt", ...files.slice(2), "a.txt", ...folders];
  await expect.poll(() => rowNames(left)).toEqual(leftOrder);
  await expectOptions(left, "size", "asc", false);
  await sort(right, "name");
  const rightOrder = [...folders].reverse().concat([...files].reverse());
  await expect.poll(() => rowNames(right)).toEqual(rightOrder);
  await expectOptions(right, "name", "desc", true);

  // A folder gets the list's preference, never a new preference keyed by its path.
  await left.getByTitle("notes", { exact: true }).first().dblclick();
  await expect.poll(() => rowNames(left)).toEqual(["team-copy.txt", "team-final.txt", "team.txt"]);
  await expectOptions(left, "size", "asc", false);
  await left.getByRole("button", { name: "Go to Shared files root" }).click();
  await expect.poll(() => rowNames(left)).toEqual(leftOrder);

  await page.getByRole("tab", { name: "Tools", exact: true }).click();
  await expect(page.getByRole("tab", { name: "Identical files", exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "Library", exact: true }).click();
  for (const reload of [false, true]) {
    if (reload) await page.reload();
    await expect.poll(() => rowNames(left)).toEqual(leftOrder);
    await expectOptions(left, "size", "asc", false);
    await expect.poll(() => rowNames(right)).toEqual(rightOrder);
    await expectOptions(right, "name", "desc", true);
  }
});

test("Settings reuses committed groups when switching tabs", async ({ page }) => {
  let reads = 0;
  page.on("request", (request) => {
    if (request.url().endsWith("/yatm.v1.SettingsService/Get")) reads++;
  });
  await page.goto("/settings/jobs");
  await page.getByRole("button", { name: /^Advanced/ }).click();
  await expect(page.getByRole("spinbutton", { name: "Manifest batch (items)" })).toBeEnabled();
  for (const name of ["Library", "Preview"]) {
    await page.getByRole("navigation", { name: "Settings views" }).getByRole("tab", { name, exact: true }).click();
    await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeVisible();
    await expect(page.getByRole("progressbar", { name: /Loading .* settings/ })).toHaveCount(0);
  }
  const initialReads = reads;
  for (const name of ["Jobs", "Library", "Preview", "Jobs"]) {
    await page.getByRole("navigation", { name: "Settings views" }).getByRole("tab", { name, exact: true }).click();
    await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeVisible();
    await expect(page.getByRole("progressbar", { name: /Loading .* settings/ })).toHaveCount(0);
  }
  expect(reads).toBe(initialReads);
});

test("a cold Job Settings route keeps one loading indicator through code and data loading", async ({ browser }) => {
  // A fresh context is essential: the usual beforeEach has already loaded app code.
  const context = await browser.newContext({ baseURL: process.env.YATM_BROWSER_URL });
  const page = await context.newPage();
  let releaseCode!: () => void;
  let releaseData!: () => void;
  await page.route(/(?:settings-jobs[^/]*\.js|\/src\/pages\/settings-jobs\.tsx)(?:\?.*)?$/, async (route) => {
    const response = await route.fetch();
    await new Promise<void>((resolve) => (releaseCode = resolve));
    await route.fulfill({ response });
  });
  await page.route("**/yatm.v1.SettingsService/Get", async (route) => {
    const response = await route.fetch();
    await new Promise<void>((resolve) => (releaseData = resolve));
    await route.fulfill({ response });
  });
  try {
    await page.goto("/settings/jobs", { waitUntil: "domcontentloaded" });
    await expect.poll(() => Boolean(releaseCode)).toBe(true);
    const heading = page.getByRole("heading", { name: "Jobs", exact: true });
    await expect(heading).toBeVisible();
    const before = await heading.boundingBox();
    await expect(page.getByRole("progressbar")).toHaveCount(1);
    await expect(page.getByRole("progressbar", { name: "Loading Job settings" })).toBeVisible();
    releaseCode();
    await expect.poll(() => Boolean(releaseData)).toBe(true);
    await expect(page.getByRole("progressbar")).toHaveCount(1);
    await expect(page.getByRole("progressbar", { name: "Loading Job settings" })).toBeVisible();
    expect(await heading.boundingBox()).toEqual(before);
    releaseData();
    await page.getByRole("button", { name: /^Advanced/ }).click();
    await expect(page.getByRole("spinbutton", { name: "Manifest batch (items)" })).toBeEnabled();
    await expect(page.getByRole("progressbar")).toHaveCount(0);
  } finally {
    releaseCode?.();
    releaseData?.();
    await context.close();
  }
});

test("opening a file-source menu reuses configured Locations without a new read", async ({ page }) => {
  const pane = page.getByRole("region", { name: "Left file pane" });
  await loaded(pane);
  let reads = 0;
  page.on("request", (request) => {
    if (request.url().endsWith("/yatm.v1.LocationService/List")) reads++;
  });
  await pane.getByRole("button", { name: "Choose file source" }).click();
  await expect(page.getByRole("menuitem", { name: "Shared files", exact: true })).toBeVisible();
  expect(reads).toBe(0);
  for (let opening = 0; opening < 2; opening++) {
    await page.keyboard.press("Escape");
    await pane.getByRole("button", { name: "Choose file source" }).click();
    await expect(page.getByRole("menuitem", { name: "Shared files", exact: true })).toBeVisible();
    await expect(page.getByText("Loading Locations…", { exact: true })).toHaveCount(0);
  }
  expect(reads).toBe(0);
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
