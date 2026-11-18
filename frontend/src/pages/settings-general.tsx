import { i18n } from "@lingui/core";
import { t } from "@lingui/core/macro";
import { useState } from "react";
import { Button, Checkbox, MenuItem, Select, Stack } from "@mui/material";
import { ActionDialog } from "@/components/action-dialog";
import { skippableConfirmations } from "@/components/confirmations";
import { useSettingsEditor } from "@/components/settings-editor";
import { SettingsField } from "@/components/settings-field";
import { SettingsActions, SettingsEditorFeedback, SettingsSection } from "@/components/settings-page";
import { GeneralSettings, SettingsGroup } from "@/entity";
import { localeNames, locales, useLocale, type Locale } from "@/i18n";

/** Preferences that are not about one area: the interface language and which confirmations appear. */
export const GeneralSettingsBrowser = () => {
  const editor = useSettingsEditor(SettingsGroup.GENERAL, GeneralSettings);
  const { locale, setLocale } = useLocale();
  const [pendingLocale, setPendingLocale] = useState<Locale>();
  const { cancel, dirty, draft, error, invalid, loading, reload, save, saved, saving, setDraft } = editor;
  return (
    <>
      <SettingsEditorFeedback
        loading={loading}
        saving={saving}
        error={error}
        invalid={invalid}
        saved={saved}
        dirty={dirty}
        hasDraft={!!draft}
        onReload={reload}
      />
      {pendingLocale && (
        <ActionDialog
          title={t`Discard unsaved changes?`}
          confirmLabel={t`Discard`}
          cancelLabel={t`Keep editing`}
          danger
          onConfirm={() => setLocale(pendingLocale)}
          onClose={() => setPendingLocale(undefined)}
        >
          {t`Switching the language reloads the interface and discards your unsaved settings.`}
        </ActionDialog>
      )}
      <Stack
        component="form"
        noValidate
        useFlexGap
        spacing={2}
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <SettingsSection title={t`Language`} description={t`Applies to this browser only.`}>
          <SettingsField
            label={t`Interface language`}
            control={
              <Select<Locale>
                size="small"
                value={locale}
                disabled={saving}
                onChange={(event) => (dirty ? setPendingLocale(event.target.value) : void setLocale(event.target.value))}
                inputProps={{ "aria-label": t`Interface language` }}
              >
                {locales.map((value) => (
                  <MenuItem key={value} value={value}>
                    {localeNames[value]}
                  </MenuItem>
                ))}
              </Select>
            }
          />
        </SettingsSection>
        {draft && (
          <SettingsSection
            title={t`Confirmations`}
            description={t`Checked confirmations appear before their action. A confirmation turned off with “Don’t show this again” is unchecked here.`}
          >
            <SettingsField
              label={t`Confirm Delete`}
              description={!draft.confirmRemove ? t`Delete moves selected items to Trash immediately.` : undefined}
              control={
                <Checkbox
                  checked={draft.confirmRemove}
                  disabled={loading || saving}
                  onChange={(_, confirmRemove) => setDraft((value) => (value ? { ...value, confirmRemove } : value))}
                />
              }
            />
            {skippableConfirmations.map(({ field, label }) => (
              <SettingsField
                key={field}
                label={i18n._(label)}
                control={
                  <Checkbox
                    checked={!draft[field]}
                    disabled={loading || saving}
                    onChange={(_, shown) => setDraft((value) => (value ? { ...value, [field]: !shown } : value))}
                  />
                }
              />
            ))}
            <div>
              <Button
                type="button"
                disabled={loading || saving || (draft.confirmRemove && skippableConfirmations.every(({ field }) => !draft[field]))}
                onClick={() =>
                  setDraft((value) =>
                    value ? { ...value, confirmRemove: true, ...Object.fromEntries(skippableConfirmations.map(({ field }) => [field, false])) } : value,
                  )
                }
              >
                {t`Show all confirmations`}
              </Button>
            </div>
          </SettingsSection>
        )}
        <SettingsActions dirty={dirty} loading={loading} saving={saving} onCancel={cancel} />
      </Stack>
    </>
  );
};
