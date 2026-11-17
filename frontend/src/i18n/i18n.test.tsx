import { i18n } from "@lingui/core";
import { act, render as renderPlain, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChonkyActions, FileBrowser, FileList, FileToolbar } from "@samuelncui/chonky";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Job, JobKind, JobPhase, JobStatus, LibrarySettings, SettingsValue } from "@/entity";
import { jobLabel, jobStatusLabel } from "@/components/job-progress";
import { contentTime } from "@/components/content-status";
import { renderPage } from "@/state/test-render";
import { chonkyI18n } from "@/tools";
import { createAppTheme } from "@/theme";
import { activateLocale, currentLocale, detectLocale, I18nProvider, localeStorageKey, useLocale } from "@/i18n";

const { get, update, list } = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), list: vi.fn() }));
vi.mock("@/api", () => ({ fileBase: "/files", settingsCli: { get, update }, locationCli: { list } }));
import { SettingsBrowser } from "@/pages/settings";

const language = (value: string) => vi.spyOn(navigator, "language", "get").mockReturnValue(value);

beforeEach(() => {
  window.localStorage.clear();
  list.mockReset().mockReturnValue({ response: Promise.resolve({ locations: [], hasMore: false }) });
  get.mockReset().mockReturnValue({
    response: Promise.resolve({ value: SettingsValue.create({ value: { oneofKind: "library", library: LibrarySettings.create({ confirmRemove: true }) } }) }),
  });
});

afterEach(async () => {
  vi.restoreAllMocks();
  await activateLocale("en");
});

describe("locale choice", () => {
  it("defaults to English and follows a Chinese browser language", () => {
    language("en-US");
    expect(detectLocale()).toBe("en");
    language("zh-TW");
    expect(detectLocale()).toBe("zh-CN");
    language("fr-FR");
    expect(detectLocale()).toBe("en");
  });

  it("prefers the stored choice and ignores an unknown one", () => {
    language("zh-CN");
    window.localStorage.setItem(localeStorageKey, "en");
    expect(detectLocale()).toBe("en");
    window.localStorage.setItem(localeStorageKey, "klingon");
    expect(detectLocale()).toBe("zh-CN");
  });

  it("switches, persists and syncs the document language", async () => {
    const Probe = () => {
      const { locale, setLocale } = useLocale();
      return <button onClick={() => void setLocale("zh-CN")}>{`locale ${locale} ${jobLabel(JobKind.ARCHIVE)}`}</button>;
    };
    renderPlain(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByRole("button")).toHaveTextContent("locale en Archive");
    await userEvent.click(screen.getByRole("button"));
    expect(await screen.findByRole("button", { name: "locale zh-CN 归档" })).toBeVisible();
    expect(window.localStorage.getItem(localeStorageKey)).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(currentLocale()).toBe("zh-CN");
  });
});

describe("Simplified Chinese", () => {
  beforeEach(() => activateLocale("zh-CN"));

  it("translates Job words from the glossary through explicit enum maps", () => {
    expect(jobLabel(JobKind.RESTORE)).toBe("恢复");
    expect(jobLabel(JobKind.CALIBRATION)).toBe("校准");
    expect(jobStatusLabel(Job.create({ phase: JobPhase.COPYING_TO_MEDIA }))).toBe("正在复制到介质");
    expect(jobStatusLabel(Job.create({ status: JobStatus.COMPLETED }))).toBe("已完成");
  });

  it("gives Chonky the active locale and translated labels", () => {
    expect(chonkyI18n.locale).toBe("zh-CN");
    expect(chonkyI18n.messages).toMatchObject({
      "chonky.actions.scan_files.button.name": "扫描",
      "chonky.actions.verify_media.button.name": "检查完整性",
      "chonky.actionGroups.Actions": "操作",
      "chonky.fileList.nothingToShow": "暂无内容",
      "chonky.toolbar.visibleFileCount": "{fileCount, plural, =0 {# 项} one {# 项} other {# 项}}",
    });
  });

  it("renders Chonky's own text and action names in Chinese", async () => {
    const actions = [ChonkyActions.SortFilesByName];
    renderPlain(
      <FileBrowser files={[]} fileActions={actions} i18n={chonkyI18n} disableDragAndDrop defaultFileViewActionId={ChonkyActions.EnableListView.id}>
        <FileToolbar />
        <FileList />
      </FileBrowser>,
    );
    expect(await screen.findByText("暂无内容")).toBeVisible();
    expect(screen.getByText("0 项")).toBeVisible();
  });

  it("localizes MUI, dates and the page", async () => {
    expect(createAppTheme("zh-CN").components?.MuiTablePagination?.defaultProps?.getItemAriaLabel?.("next")).toBe("下一页");
    expect(contentTime(1_700_000_000_000_000_000n)).toMatch(/2023年11月/);
    renderPage(<SettingsBrowser />);
    expect(await screen.findByText("文件可见性")).toBeVisible();
    expect(screen.getByRole("button", { name: "导入" })).toBeVisible();
  });
});

describe("language setting", () => {
  it("shows the active language and switches the page", async () => {
    renderPage(<SettingsBrowser />);
    const select = await screen.findByRole("combobox", { name: "Interface language" });
    expect(select).toHaveTextContent("English");
    await userEvent.click(select);
    await userEvent.click(screen.getByRole("option", { name: "简体中文" }));
    await waitFor(() => expect(i18n.locale).toBe("zh-CN"));
    expect(await screen.findByText("文件可见性")).toBeVisible();
    expect(window.localStorage.getItem(localeStorageKey)).toBe("zh-CN");
    await act(async () => {});
  });
});
