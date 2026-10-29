import { Dispatch, SetStateAction, useCallback, useEffect, useMemo, useState } from "react";

import { settingsCli } from "@/api";
import { type DeviceSettings, type JobSettings, type LibrarySettings, type PreviewSettings, SettingsGroup, SettingsValue } from "@/entity";
import { useAppSelector, useAppStore } from "@/state/react";
import type { AppStore } from "@/state/store";
import { makeSelectSettings, settingsActions } from "@/state/settings";
import { errorMessage } from "@/tools";

type SettingsMessage<T> = {
  clone(value: T): T;
  equals(left: T | undefined, right: T | undefined): boolean;
};

type SettingsByGroup = {
  [SettingsGroup.LIBRARY]: LibrarySettings;
  [SettingsGroup.PREVIEW]: PreviewSettings;
  [SettingsGroup.JOB]: JobSettings;
  [SettingsGroup.DEVICE]: DeviceSettings;
};

const groupValue = <G extends keyof SettingsByGroup>(settings: SettingsValue, group: G): SettingsByGroup[G] | undefined => {
  const value = settings.value;
  if (group === SettingsGroup.LIBRARY && value?.oneofKind === "library") return value.library as SettingsByGroup[G];
  if (group === SettingsGroup.PREVIEW && value?.oneofKind === "preview") return value.preview as SettingsByGroup[G];
  if (group === SettingsGroup.JOB && value?.oneofKind === "job") return value.job as SettingsByGroup[G];
  if (group === SettingsGroup.DEVICE && value?.oneofKind === "device") return value.device as SettingsByGroup[G];
  return undefined;
};

const settingsValue = <G extends keyof SettingsByGroup>(group: G, value: SettingsByGroup[G]): SettingsValue => {
  switch (group) {
    case SettingsGroup.LIBRARY:
      return { value: { oneofKind: "library", library: value as LibrarySettings } };
    case SettingsGroup.PREVIEW:
      return { value: { oneofKind: "preview", preview: value as PreviewSettings } };
    case SettingsGroup.JOB:
      return { value: { oneofKind: "job", job: value as JobSettings } };
    case SettingsGroup.DEVICE:
      return { value: { oneofKind: "device", device: value as DeviceSettings } };
    default:
      throw new Error("Unsupported Settings group");
  }
};

export const getSettingsGroup = async <G extends keyof SettingsByGroup>(group: G): Promise<SettingsByGroup[G]> => {
  const response = await settingsCli.get({ group }).response;
  const value = response.value && groupValue(response.value, group);
  if (!value) throw new Error("Settings reply does not match the requested group");
  return value;
};

const updateSettingsGroup = async <G extends keyof SettingsByGroup>(group: G, value: SettingsByGroup[G]): Promise<SettingsByGroup[G]> => {
  const response = await settingsCli.update({ value: settingsValue(group, value) }).response;
  const updated = response.value && groupValue(response.value, group);
  if (!updated) throw new Error("Settings reply does not match the updated group");
  return updated;
};

const readCommittedSettings = <G extends keyof SettingsByGroup>(store: AppStore, group: G): SettingsByGroup[G] | undefined => {
  const value = store.getState().settings[group]?.value;
  return value ? groupValue(SettingsValue.fromJsonString(value), group) : undefined;
};

const pending = new WeakMap<AppStore, Map<SettingsGroup, Promise<unknown>>>();
export async function loadCommittedSettings<G extends keyof SettingsByGroup>(store: AppStore, group: G, reload = false): Promise<SettingsByGroup[G]> {
  const read = () => readCommittedSettings(store, group);
  const current = read();
  if (!reload && current) return current;
  const requests = pending.get(store) ?? new Map<SettingsGroup, Promise<unknown>>();
  pending.set(store, requests);
  const existing = requests.get(group);
  if (!reload && existing) return existing as Promise<SettingsByGroup[G]>;
  const error = store.getState().settings[group]?.error;
  if (!reload && error) throw new Error(error);
  store.dispatch(settingsActions.requested(group));
  const request = store.getState().settings[group]!.request;
  const followCurrent = (): Promise<SettingsByGroup[G]> => {
    const next = requests.get(group);
    return next && next !== load ? (next as Promise<SettingsByGroup[G]>) : loadCommittedSettings(store, group);
  };
  const load: Promise<SettingsByGroup[G]> = getSettingsGroup(group)
    .then((value) => {
      if (store.getState().settings[group]?.request !== request) return followCurrent();
      store.dispatch(settingsActions.received({ group, request, value: SettingsValue.toJsonString(settingsValue(group, value)) }));
      return read() ?? value;
    })
    .catch((failure: unknown) => {
      if (store.getState().settings[group]?.request !== request) return followCurrent();
      store.dispatch(settingsActions.failed({ group, request, error: errorMessage(failure, "Could not load settings") }));
      throw failure;
    })
    .finally(() => {
      if (requests.get(group) === load) requests.delete(group);
    });
  requests.set(group, load);
  return load;
}

export function useCommittedSettings<G extends keyof SettingsByGroup>(group: G, active = true) {
  const store = useAppStore();
  const selector = useMemo(() => makeSelectSettings(group), [group]);
  const value = useAppSelector(selector);
  const status = useAppSelector((state) => state.settings[group]);
  useEffect(() => {
    if (active) void loadCommittedSettings(store, group).catch(() => {});
  }, [store, group, active]);
  const reload = useCallback(() => loadCommittedSettings(store, group, true), [store, group]);
  return { settings: value && groupValue(value, group), loading: active && (!status || status.loading), error: status?.error ?? "", reload };
}

/** Shared load, draft, save, cancel and reload behavior for every typed Settings group. */
export const useSettingsEditor = <G extends keyof SettingsByGroup>(group: G, message: SettingsMessage<SettingsByGroup[G]>, label: string) => {
  type T = SettingsByGroup[G];
  const store = useAppStore();
  const selector = useMemo(() => makeSelectSettings(group), [group]);
  const committed = useAppSelector(selector);
  const [settings, setSettings] = useState<T | undefined>(() => {
    const value = readCommittedSettings(store, group);
    return value && message.clone(value);
  });
  const [draft, setDraft] = useState<T | undefined>(() => settings && message.clone(settings));
  const [attempt, setAttempt] = useState(0);
  const [loading, setLoading] = useState(!settings && !store.getState().settings[group]?.error);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(store.getState().settings[group]?.error ?? "");
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    const value = committed && groupValue(committed, group);
    if (!value || message.equals(value, settings)) return;
    // Saves can finish after navigation. Adopt them without replacing private edits.
    setDraft((current) => (!current || message.equals(current, settings) ? message.clone(value) : current));
    setSettings(message.clone(value));
  }, [committed, group, message, settings]);

  useEffect(() => {
    let active = true;
    setLoading(attempt > 0 || (!readCommittedSettings(store, group) && !store.getState().settings[group]?.error));
    void loadCommittedSettings(store, group, attempt > 0)
      .then((value) => {
        if (!active) return;
        setSettings(message.clone(value));
        setDraft((current) => (attempt > 0 || !current ? message.clone(value) : current));
        setError("");
      })
      .catch((failure) => {
        if (active) setError(errorMessage(failure, `Could not load ${label} settings`));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [attempt, group, label, message, store]);

  const dirty = !!draft && !message.equals(draft, settings);
  const reload = () => {
    setSaved(false);
    setAttempt((value) => value + 1);
  };
  const cancel = () => {
    if (settings) setDraft(message.clone(settings));
    setError("");
    setSaved(false);
  };
  const save = async (): Promise<boolean> => {
    if (!draft || saving || loading) return false;
    setSaving(true);
    setSaved(false);
    try {
      const value = await updateSettingsGroup(group, draft);
      store.dispatch(settingsActions.saved({ group, value: SettingsValue.toJsonString(settingsValue(group, value)) }));
      setSettings(message.clone(value));
      setDraft(message.clone(value));
      setError("");
      setSaved(true);
      return true;
    } catch (failure) {
      setError(errorMessage(failure, `Could not save ${label} settings`));
      return false;
    } finally {
      setSaving(false);
    }
  };

  return {
    cancel,
    dirty,
    draft,
    error,
    loading,
    reload,
    save,
    saved,
    saving,
    settings,
    setDraft: setDraft as Dispatch<SetStateAction<T | undefined>>,
    setError,
  };
};
