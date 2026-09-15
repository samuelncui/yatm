import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearchParams, useLocation } from "react-router";
import { Alert, Button, Card, Checkbox, CircularProgress, FormControlLabel, MenuItem, Stack, TextField, Typography } from "@mui/material";
import { cli, locationCli, scanJobCli } from "@/api";
import {
  FileSelection,
  Location,
  Media,
  MediaKind,
  MediaAccess,
  OnlineBinding,
  PreviewPolicy,
  ScanJobSpec,
  ScanResultPolicy,
  ScanSignaturePolicy,
} from "@/entity";
import { LocationSearchSelect, MediaSearchSelect } from "@/components/catalog-search-select";
import { PreviewPolicySelect } from "@/components/preview-policy-select";
import { errorMessage } from "@/tools";
import { loadScanPreferences, resultForSource, saveScanPreferences } from "./scan-preferences";

export const ScanBrowser = () => {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const route = useLocation();
  const requestedLocation = params.get("location") ?? "";
  const requestedMedia = params.get("media") ?? "";
  const selections = Array.isArray(route.state?.selections) ? route.state.selections.filter(FileSelection.is) : [];
  const [initial] = useState(() => {
    const saved = loadScanPreferences();
    const kind = selections.length ? "library" : requestedMedia ? "media" : requestedLocation ? "location" : (saved?.kind ?? "location");
    const explicit = !!(selections.length || requestedMedia || requestedLocation);
    return {
      saved,
      kind,
      explicit,
      targetID: explicit ? (kind === "media" ? requestedMedia : requestedLocation) : (saved?.[kind === "media" ? "media" : "location"]?.id ?? ""),
    };
  });
  const [kind, setKind] = useState(initial.kind);
  const [selectedLocation, setSelectedLocation] = useState<Location | null>(null);
  const [selectedMedia, setSelectedMedia] = useState<Media | null>(null);
  const [loadingTarget, setLoadingTarget] = useState(false);
  const [signaturePolicy, setSignaturePolicy] = useState(initial.saved?.signaturePolicy ?? ScanSignaturePolicy.FILL_MISSING);
  const [resultPolicy, setResultPolicy] = useState(
    initial.kind === "media" && params.get("result") === "verify" ? ScanResultPolicy.VERIFY_COPIES : resultForSource(initial.kind, initial.saved?.resultPolicy),
  );
  const [compare, setCompare] = useState(initial.saved?.compare ?? true);
  const [previewPolicy, setPreviewPolicy] = useState(
    params.get("previews") === "1" ? PreviewPolicy.PREVIEW_MISSING_ONLY : (initial.saved?.previewPolicy ?? PreviewPolicy.PREVIEW_NONE),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  useEffect(() => {
    if (initial.kind === "library" || !initial.targetID) return;
    if (!/^[1-9]\d*$/.test(initial.targetID)) {
      setError("The selected source is invalid. Choose a source.");
      return;
    }
    const controller = new AbortController();
    setLoadingTarget(true);
    const load = async () => {
      if (initial.kind === "location") {
        const location = (await locationCli.get({ id: BigInt(initial.targetID), revision: 0n }, { abort: controller.signal }).response).location;
        if (controller.signal.aborted) return;
        if (!location || String(location.id) !== initial.targetID || location.binding !== OnlineBinding.CONFIRMED)
          throw new Error("The selected Location is unavailable or needs confirmation. Choose a Location.");
        if (
          !initial.explicit &&
          (location.bindingToken !== initial.saved?.location?.bindingToken || !location.bindingToken || location.rootPath !== initial.saved?.location?.rootPath)
        )
          throw new Error("The last Location has changed. Select it again.");
        setSelectedLocation(location);
        return;
      }
      const reply = await cli.mediaList({ param: { oneofKind: "mget", mget: { ids: [BigInt(initial.targetID)] } } }, { abort: controller.signal }).response;
      if (controller.signal.aborted) return;
      const media = reply.media.find((item) => String(item.id) === initial.targetID);
      if (!media || ![MediaKind.TAPE, MediaKind.VOLUME].includes(media.kind))
        throw new Error("The selected Media is no longer available. Choose another Media.");
      if (!initial.explicit && (media.identity !== initial.saved?.media?.identity || media.kind !== initial.saved?.media?.kind))
        throw new Error("The last Media has changed. Select it again.");
      setSelectedMedia(media);
    };
    void load()
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error, "Could not load selected source"));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingTarget(false);
      });
    return () => controller.abort();
  }, [initial]);
  const canPreview =
    kind !== "media" ||
    (!!selectedMedia &&
      selectedMedia.kind !== MediaKind.TAPE &&
      [MediaAccess.RANDOM, MediaAccess.CONCURRENT_RANDOM].includes(selectedMedia.capabilities?.read ?? MediaAccess.UNSPECIFIED));
  const verifying = resultPolicy === ScanResultPolicy.VERIFY_COPIES;
  const paths =
    String(selectedLocation?.id) === requestedLocation && Array.isArray(route.state?.paths)
      ? route.state.paths.filter((path: unknown): path is string => typeof path === "string")
      : [];
  const ready = kind === "library" ? selections.length > 0 : kind === "location" ? selectedLocation?.binding === OnlineBinding.CONFIRMED : !!selectedMedia;
  const submit = async () => {
    if (submitting.current || !ready) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    try {
      const spec = ScanJobSpec.create({
        locationId: kind === "location" ? selectedLocation?.id : 0n,
        mediaId: kind === "media" ? selectedMedia?.id : 0n,
        selections: kind === "library" ? selections : [],
        paths: kind === "location" ? paths : [],
        signaturePolicy: verifying ? ScanSignaturePolicy.FORCE_READ : signaturePolicy,
        resultPolicy,
        compareLibrary: compare,
        previewPolicy: canPreview ? previewPolicy : PreviewPolicy.PREVIEW_NONE,
      });
      const reply = await scanJobCli.create({ priority: 1n, spec }).response;
      if (!reply.job) throw new Error("Job was not returned");
      saveScanPreferences({
        kind: kind === "media" ? "media" : "location",
        location: selectedLocation
          ? { id: String(selectedLocation.id), rootPath: selectedLocation.rootPath, bindingToken: selectedLocation.bindingToken }
          : undefined,
        media: selectedMedia ? { id: String(selectedMedia.id), identity: selectedMedia.identity, kind: selectedMedia.kind } : undefined,
        signaturePolicy: spec.signaturePolicy,
        resultPolicy,
        compare,
        previewPolicy: spec.previewPolicy,
      });
      navigate(`/jobs/${reply.job.id}`);
    } catch (error) {
      setError(errorMessage(error, "Could not create scan"));
    } finally {
      setBusy(false);
      submitting.current = false;
    }
  };
  return (
    <div className="job-create-page">
      <div className="short-form-page">
        <Card className="job-create-card">
          <TextField
            select
            label="Source"
            value={kind}
            disabled={busy || loadingTarget}
            onChange={(event) => {
              const next = event.target.value;
              setKind(next);
              setResultPolicy(resultForSource(next, resultPolicy));
              setError("");
            }}
          >
            <MenuItem value="location">Location</MenuItem>
            <MenuItem value="media">Media</MenuItem>
            {selections.length > 0 && <MenuItem value="library">Selected files</MenuItem>}
          </TextField>
          {kind === "location" && (
            <>
              <LocationSearchSelect
                value={selectedLocation}
                disabled={busy || loadingTarget}
                onChange={(location) => {
                  setSelectedLocation(location);
                  setError("");
                }}
              />
              {paths.length > 0 && (
                <ul>
                  {paths.map((path: string) => (
                    <li key={path}>{path || "/"}</li>
                  ))}
                </ul>
              )}
            </>
          )}
          {kind === "media" && (
            <MediaSearchSelect
              value={selectedMedia}
              disabled={busy || loadingTarget}
              onChange={(media) => {
                setSelectedMedia(media);
                setError("");
              }}
            />
          )}
          {loadingTarget && (
            <Stack direction="row" role="status" sx={{ gap: 1, alignItems: "center" }}>
              <CircularProgress size={16} />
              <Typography variant="body2">Loading source…</Typography>
            </Stack>
          )}
          <TextField
            select
            label="Signatures"
            value={verifying ? ScanSignaturePolicy.FORCE_READ : signaturePolicy}
            disabled={busy || verifying}
            onChange={(event) => setSignaturePolicy(Number(event.target.value))}
          >
            <MenuItem value={ScanSignaturePolicy.KNOWN_ONLY}>Reuse known signatures only</MenuItem>
            <MenuItem value={ScanSignaturePolicy.FILL_MISSING}>Read new and changed files</MenuItem>
            <MenuItem value={ScanSignaturePolicy.FORCE_READ}>Read every file</MenuItem>
          </TextField>
          <TextField select label="Results" disabled={busy} value={resultPolicy} onChange={(event) => setResultPolicy(Number(event.target.value))}>
            <MenuItem value={ScanResultPolicy.REPORT_ONLY}>View results only</MenuItem>
            {kind === "media" ? (
              [
                <MenuItem key="inventory" value={ScanResultPolicy.PUBLISH_INVENTORY}>
                  Update inventory
                </MenuItem>,
                <MenuItem key="verify" value={ScanResultPolicy.VERIFY_COPIES}>
                  Check recorded copies
                </MenuItem>,
              ]
            ) : (
              <MenuItem value={ScanResultPolicy.PUBLISH_ORIGINALS}>Update Library originals</MenuItem>
            )}
          </TextField>
          <FormControlLabel
            control={<Checkbox disabled={busy} checked={compare} onChange={(_, value) => setCompare(value)} />}
            label="Find matching Library content"
          />
          <PreviewPolicySelect
            value={canPreview ? previewPolicy : PreviewPolicy.PREVIEW_NONE}
            disabled={busy || !canPreview}
            onChange={setPreviewPolicy}
            helperText={
              kind === "media" && selectedMedia && !canPreview
                ? selectedMedia.kind === MediaKind.TAPE
                  ? "Not supported on Tape"
                  : "Previews require random-readable Media"
                : undefined
            }
          />
          {error && <Alert severity="error">{error}</Alert>}
          <div className="job-create-actions">
            <Button variant="contained" disabled={busy || !ready} onClick={() => void submit()}>
              {busy ? "Creating…" : "Start scan"}
            </Button>
          </div>
        </Card>
      </div>
    </div>
  );
};
