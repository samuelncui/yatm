# Shared Chonky presentation

The pinned `@samuelncui/chonky` 0.3.2 patch adds an optional `FileData.status`
(`label`, `color`, optional supplemental `marker`). Shared list/grid entries render an accessible dot with a
short, left-side hover/focus tooltip, without changing selection, navigation, or drag behavior.
List rows reserve the same trailing status column for files and directories. Other
consumers have no indicator unless they provide status data.

`GroupedFileList` adds controlled, single-group expansion around the ordinary
FileList, independently of its list/grid view. Headers are not Files; expanding
another group clears selection. The parent FileBrowser receives the active
group's loaded members, so toolbar, context menu, selection and Inspector actions
keep their normal identities. Optional `FileData.details` lines show contextual
paths in list rows. Querying, pagination, signatures and backup labels belong to
YATM, not the shared component.

Lists containing details use Virtuoso's measured row heights. Content determines
each row's height above the configured minimum, including after details change;
there is no line-count height estimate. Ordinary lists without details retain
fixed-height virtualization. [Row tests](../src/components/file-row-height.test.tsx)
cover this sizing contract; real-browser review checks text bounds and scrolling.

`FileNavbar.rootContent` replaces only the first root breadcrumb with an optional
React control. YATM uses it for each pane's Library/Location selector. Ancestor
navigation measures available width and folds middle ancestors into a keyboard-accessible menu while keeping the
root and current directory on one compact line. Root navigation and the source
menu are separate controls. The full toolbar remains ordinary Chonky behavior; the selector
does not intercept row events or implement physical file operations. The Copy path
action copies an explicit `FileNavbar.path` or the source-qualified breadcrumb path.
Breadcrumb buttons use two-pixel padding and a six-pixel separator slot; folding
uses the same separator width. The list's inline name filter requests the `filter`
icon, distinct from full query search; YATM supplies its funnel through the normal
icon provider.

Keyboard actions are scoped to the focused browser frame. Clicking a row focuses
its frame without stealing focus from inputs, links or buttons; another pane's
selection cannot also receive Copy, Cut, Paste or Delete. Native form editing is
unaffected. YATM supplies filesystem operations through ordinary typed Chonky
actions, sharing the current source's cut/paste clipboard between panes.

Drag/drop connectors keep stable DOM refs while selection and hover state change.
Only the source browser renders the shared drag preview; target browsers render
their drop feedback without duplicating it. [Drag tests](../src/components/file-drag.test.tsx)
exercise native HTML5 events across panes and folder/background targets, checking
that connectors stay attached and each drop dispatches one move.

`FileBrowser.footer` (also available on `FullFileBrowser`) places optional React
content inside the same browser frame, after the independently scrolling browser
body. The footer has a top divider and is bounded to half the available height;
composed forms can scroll their options while keeping submission actions visible.
Footer inputs retain native context menus and text selection. Without a footer,
the existing browser layout is unchanged. Backup and Restore waitlists use this
slot for a single creation-dialog entry action; configuration and estimates belong
inside that application dialog.

`FileList.emptyPlaceholder` accepts optional React content for a list without
rows. Directory-read failures use a concise error and Retry rather than a false
empty-directory message; their toolbars omit zero counters and write actions.
Ordinary empty directories retain the default placeholder. Error details remain
available without repeating full paths in the primary message.

[chonky-source.patch](chonky-source.patch) contains the reviewable TypeScript
changes against the public
[Chonky v0.3.2 source](https://github.com/samuelncui/Chonky/tree/v0.3.2).
Apply it with `patch -p1` from the package directory containing `src`, run the package's build, copy
the resulting `dist` into a `pnpm patch` edit directory, and regenerate the
installation patch with `pnpm patch-commit`. Do not hand-edit generated bundles.
The lockfile pins the installation patch; no local checkout or unpublished
package is needed to build YATM.

[Status tests](../src/components/file-status.test.tsx) and
[grouping tests](../src/components/duplicate-groups.test.tsx) exercise installed
shared rows, tooltips, expansion and selection. Remove these patches when a
published Chonky version provides the same contract, after rerunning the checks.
The [waitlist tests](../src/components/selection-waitlist.test.tsx) exercise the
installed footer's frame ownership, input behavior and separation from file menus.
