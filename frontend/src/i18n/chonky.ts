import { i18n, type MessageDescriptor } from "@lingui/core";
import { msg } from "@lingui/core/macro";
import { currentLocale, type Locale } from "./locale";

// Chonky looks every label up by id in react-intl: chonky.actions.<action id>.button.name for an
// action, .button.tooltip for its tooltip, chonky.actionGroups.<group> for a toolbar group. Each
// action's English text stays in its definition (src/actions.tsx and the other defineFileAction
// calls); the table below carries the same text so it is extracted and translated. Add a row when
// an action is added. A row's text must equal the action's button name.
const names: Record<string, MessageDescriptor> = {
  // Chonky built-ins.
  open_parent_folder: msg({ message: "Go up a directory", context: "file action" }),
  open_selection: msg({ message: "Open selection", context: "file action" }),
  select_all_files: msg({ message: "Select all files", context: "file action" }),
  clear_selection: msg({ message: "Clear selection", context: "file action" }),
  enable_list_view: msg({ message: "Switch to List", context: "file action" }),
  enable_grid_view: msg({ message: "Switch to Grid", context: "file action" }),
  sort_files_by_name: msg({ message: "Sort by name", context: "file action" }),
  sort_files_by_size: msg({ message: "Sort by size", context: "file action" }),
  sort_files_by_date: msg({ message: "Sort by date", context: "file action" }),
  toggle_hidden_files: msg({ message: "Show hidden files", context: "file action" }),
  toggle_show_folders_first: msg({ message: "Show folders first", context: "file action" }),
  create_folder: msg({ message: "Create folder", context: "file action" }),
  download_files: msg({ message: "Download files", context: "file action" }),
  // Every page that uses delete_files names it "Delete"; Chonky resolves one translation per id, so
  // a differently named variant has its own id.
  delete_files: msg({ message: "Delete", context: "file action" }),
  // src/actions.tsx.
  rename_file: msg({ message: "Rename File", context: "file action" }),
  cut_files: msg({ message: "Cut", context: "file action" }),
  paste_files: msg({ message: "Paste", context: "file action" }),
  admit_location_files: msg({ message: "Add to Library", context: "file action" }),
  import_archive_positions: msg({ message: "Add to Library", context: "file action" }),
  scan_files: msg({ message: "Scan", context: "file action" }),
  scan_media: msg({ message: "Scan", context: "file action" }),
  get_data_usage: msg({ message: "Data Usage", context: "file action" }),
  cancel_data_usage: msg({ message: "Data Usage", context: "file action" }),
  refresh_list: msg({ message: "Refresh", context: "file action" }),
  edit_file_metadata: msg({ message: "Edit tags & note", context: "file action" }),
  archive_library_selection: msg({ message: "Archive selection", context: "file action" }),
  add_to_archive_list: msg({ message: "Add to Archive list", context: "file action" }),
  restore_library_selection: msg({ message: "Add to Restore list", context: "file action" }),
  locate_in_other_pane: msg({ message: "Locate in Other Pane", context: "file action" }),
  view_file_details: msg({ message: "View Details", context: "file action" }),
  trim_library: msg({ message: "Clean up Library", context: "file action" }),
  initialize_volume: msg({ message: "Add Volume", context: "file action" }),
  inspect_media: msg({ message: "Properties", context: "file action" }),
  load_more: msg({ message: "Load More", context: "file action" }),
  verify_media: msg({ message: "Check integrity", context: "file action" }),
  media_jobs: msg({ message: "Related Jobs", context: "file action" }),
  remove_media_from_library: msg({ message: "Remove from Library", context: "file action" }),
  // Other defineFileAction calls.
  "choose-location-file": msg({ message: "Choose file", context: "file action" }),
  "add-file-selection": msg({ message: "Add selected", context: "file action" }),
  add_job_selection: msg({ message: "Add to list", context: "file action" }),
  remove_job_selection: msg({ message: "Remove from list", context: "file action" }),
  change_selected_version: msg({ message: "Change version", context: "file action" }),
  clear_job_selection: msg({ message: "Clear list", context: "file action" }),
  "identical-keep": msg({ message: "Keep this", context: "file action" }),
  "identical-merge": msg({ message: "Merge into", context: "file action" }),
};

const tooltips: Record<string, MessageDescriptor> = {
  create_folder: msg({ message: "Create a folder", context: "file action" }),
  get_data_usage: msg({ message: "Data Usage", context: "file action" }),
  cancel_data_usage: msg({ message: "Cancel Data Usage", context: "file action" }),
  add_to_archive_list: msg({ message: "Add to Archive list", context: "file action" }),
  delete_files: msg({ message: "Delete", context: "file action" }),
  refresh_list: msg({ message: "Refresh", context: "file action" }),
};

const groups: Record<string, MessageDescriptor> = {
  Actions: msg({ message: "Actions", context: "file action group" }),
  Options: msg({ message: "Options", context: "file action group" }),
};

const byId = (prefix: string, table: Record<string, MessageDescriptor>, suffix = "") =>
  Object.entries(table).map(([key, descriptor]) => [`${prefix}${key}${suffix}`, i18n._(descriptor)] as const);

// Chonky's own messages are ICU strings that it formats with the count. A translation supplies the
// text for the zero, one and other cases, written with # for the count.
const count = (zero: string, one: string, other: string) => `{fileCount, plural, =0 {${zero}} one {${one}} other {${other}}}`;
const chonkyOwn = () => {
  return {
    "chonky.toolbar.sparseRowCount": `{rowCount, plural, one {${i18n._(msg({ message: "# row", context: "file browser" }))}} other {${i18n._(msg({ message: "# rows", context: "file browser" }))}}}`,
    "chonky.toolbar.unknownFileCount": i18n._(msg({ message: "Unknown total", context: "file browser" })),
    "chonky.toolbar.visibleFileCount": count(
      i18n._(msg({ message: "# items", context: "file browser" })),
      i18n._(msg({ message: "# item", context: "file browser" })),
      i18n._(msg({ message: "# items", context: "file browser" })),
    ),
    "chonky.toolbar.selectedFileCount": count(
      "",
      i18n._(msg({ message: "# selected", context: "file browser" })),
      i18n._(msg({ message: "# selected", context: "file browser" })),
    ),
    "chonky.toolbar.hiddenFileCount": count(
      "",
      i18n._(msg({ message: "# hidden", context: "file browser" })),
      i18n._(msg({ message: "# hidden", context: "file browser" })),
    ),
    "chonky.toolbar.searchPlaceholder": i18n._(msg({ message: "Filter", context: "file browser" })),
    "chonky.fileList.nothingToShow": i18n._(msg({ message: "Nothing to show", context: "file browser" })),
    "chonky.fileList.loadingFiles": i18n._(msg({ message: "Loading files", context: "file browser" })),
    // Chonky formats {name}, {label} and {count}; the translation keeps them as placeholders.
    "chonky.fileList.groupActions": i18n._({ ...msg({ message: "{name} actions", context: "file browser" }), values: { name: "{name}" } }),
    "chonky.fileList.groupFileCount": i18n._({ ...msg({ message: "{label} files", context: "file browser" }), values: { label: "{label}" } }),
    "chonky.navbar.copyPath": i18n._(msg({ message: "Copy path", context: "file browser" })),
    "chonky.navbar.pathCopied": i18n._(msg({ message: "Path copied", context: "file browser" })),
    "chonky.navbar.copyPathFailed": i18n._(msg({ message: "Could not copy path", context: "file browser" })),
    "chonky.navbar.showParentFolders": i18n._(msg({ message: "Show parent folders", context: "file browser" })),
    "chonky.navbar.loadingAncestor": i18n._(msg({ message: "Loading…", context: "file browser" })),
    "chonky.navbar.loadingFolder": i18n._(msg({ message: "Loading…", context: "file browser" })),
    // Chonky inserts its bold "Alt + Right Click" shortcut at {shortcut}.
    "chonky.contextMenu.browserMenuShortcut": i18n._({
      ...msg({ message: "Browser menu: {shortcut}", context: "file browser" }),
      values: { shortcut: "{shortcut}" },
    }),
  };
};

const built = new Map<Locale, Record<string, string>>();

/**
 * Chonky's react-intl messages for the active locale. English returns none, so Chonky keeps the
 * default text of each action definition, including pages that rename a shared action.
 */
export const chonkyMessages = (): Record<string, string> => {
  const locale = currentLocale();
  if (locale === "en") return {};
  let messages = built.get(locale);
  if (!messages) {
    messages = Object.fromEntries([
      ...byId("chonky.actions.", names, ".button.name"),
      ...byId("chonky.actions.", tooltips, ".button.tooltip"),
      ...byId("chonky.actionGroups.", groups),
      ...Object.entries(chonkyOwn()),
    ]);
    built.set(locale, messages);
  }
  return messages;
};
