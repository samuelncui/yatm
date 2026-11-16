import { useCallback } from "react";
import { SettingsGroup, type LibrarySettings } from "@/entity";
import { useAppStore } from "@/state/react";
import { changeCommittedSettings, useCommittedSettings } from "./settings-editor";

/**
 * The advisory confirmations an operator may stop seeing. Each is one Library setting that is true
 * when the confirmation is skipped; Settings lists them and can show them all again. Confirmations
 * that guard a loss, and one-off consents, are not listed and always appear.
 */
export const skippableConfirmations = [
  { field: "skipAddEntriesConfirmation", label: "Confirm adding selected inventory entries to Library" },
  { field: "skipAddMediaConfirmation", label: "Confirm adding a Media\u2019s whole inventory to Library" },
] as const satisfies readonly { field: keyof LibrarySettings; label: string }[];
export type SkippableConfirmation = (typeof skippableConfirmations)[number]["field"];

/** Whether one advisory confirmation is skipped, and the action that stores the operator's choice. */
export function useSkippableConfirmation(field: SkippableConfirmation) {
  const store = useAppStore();
  const { settings } = useCommittedSettings(SettingsGroup.LIBRARY);
  const setSkipped = useCallback(
    async (skipped: boolean) => {
      await changeCommittedSettings(store, SettingsGroup.LIBRARY, (current) => ({ ...current, [field]: skipped }));
    },
    [store, field],
  );
  // Until the settings are known the confirmation is shown.
  return { skipped: settings?.[field] ?? false, setSkipped };
}
