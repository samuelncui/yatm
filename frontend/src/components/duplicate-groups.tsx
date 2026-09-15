import { useCallback, useEffect, useEffectEvent, useRef, useState } from "react";
import { Alert, Button, LinearProgress } from "@mui/material";
import RefreshRoundedIcon from "@mui/icons-material/RefreshRounded";
import { GroupedFileList, type FileArray } from "@samuelncui/chonky";
import { convertFiles, fileCatalogCli } from "@/api";
import { ContentObservation, FileScope, FileSelection, type DuplicateGroup, type DuplicateMember } from "@/entity";
import { contentHex } from "@/components/content-status";
import { errorMessage, formatFilesize } from "@/tools";
import "./duplicate-groups.less";

const groupFingerprint = (group: DuplicateGroup) =>
  [contentHex(group.signature), group.name, group.originalCount, group.locationCount, group.archivedCopies, group.matchingCount, group.size].join("\0");

export const duplicateFiles = (members: DuplicateMember[]) =>
  members.flatMap((member) => {
    if (!member.file || !member.original) return [];
    const file = convertFiles([member.file])[0];
    const labels = {
      [ContentObservation.UNCHECKED]: "Not checked",
      [ContentObservation.CONFIRMED]: "",
      [ContentObservation.CHANGED]: "Changed · Scan again",
      [ContentObservation.UNAVAILABLE]: "Unavailable",
    };
    const observation = labels[member.observation];
    return [
      {
        ...file,
        originSelection: FileSelection.create({ target: { oneofKind: "library", library: { fileId: member.file.id } }, scope: FileScope.ALL }),
        draggable: false,
        droppable: false,
        analysisLocationID: String(member.original.locationId),
        analysisPath: member.original.path,
        status: observation ? { color: "#87909e", label: observation } : file.status,
        details: [
          `Library${member.libraryPath}`,
          `${member.locationName} / ${member.original.path}${member.matchesFilter ? "" : " · Outside filter"}`,
          ...(observation ? [observation] : []),
        ],
      },
    ];
  });

export const DuplicateGroups = ({
  query,
  refresh,
  onFiles,
}: {
  query: string;
  refresh: { sequence: number; background: boolean };
  onFiles: (files: FileArray) => void;
}) => {
  const [groups, setGroups] = useState<DuplicateGroup[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const [active, setActive] = useState("");
  const [members, setMembers] = useState<DuplicateMember[]>([]);
  const [memberCursor, setMemberCursor] = useState("");
  const [memberLoading, setMemberLoading] = useState(false);
  const [memberError, setMemberError] = useState("");
  const [copyError, setCopyError] = useState("");
  const groupRequest = useRef(0);
  const groupPending = useRef(false);
  const failedGroupCursor = useRef("");
  const memberRequest = useRef(0);
  const indexRevision = useRef("");
  const firstPage = useRef("");
  const lastRefresh = useRef(refresh.sequence);

  const clearMembers = useCallback(() => {
    memberRequest.current++;
    setMembers([]);
    setMemberCursor("");
    setMemberError("");
    setMemberLoading(false);
    onFiles([]);
  }, [onFiles]);

  const loadGroups = useCallback(
    async (cursor = "", background = false) => {
      if (background && (groupPending.current || !indexRevision.current)) return;
      const sequence = ++groupRequest.current;
      groupPending.current = true;
      if (!background) setLoading(true);
      if (!background && !cursor) {
        clearMembers();
        setActive("");
      }
      try {
        const reply = await fileCatalogCli.listDuplicateGroups({ query, cursor, limit: 20 }).response;
        if (sequence !== groupRequest.current) return;
        const fingerprint = reply.groups.map(groupFingerprint).join("\n");
        const changed = reply.indexRevision !== indexRevision.current;
        if ((background && (changed || fingerprint !== firstPage.current)) || (cursor && changed)) {
          setStale(true);
          clearMembers();
          return;
        }
        setError("");
        if (background) return;
        if (!cursor) {
          clearMembers();
          setActive("");
          setStale(false);
          indexRevision.current = reply.indexRevision;
          firstPage.current = fingerprint;
        }
        setGroups((current) => (cursor ? [...current, ...reply.groups] : reply.groups));
        setNextCursor(reply.nextCursor);
      } catch (error) {
        if (sequence === groupRequest.current) {
          failedGroupCursor.current = cursor;
          setError(errorMessage(error, "Could not load duplicate groups"));
        }
      } finally {
        if (sequence === groupRequest.current) {
          groupPending.current = false;
          setLoading(false);
        }
      }
    },
    [clearMembers, query],
  );

  useEffect(() => {
    const pending = groupRequest;
    const busy = groupPending;
    void loadGroups();
    return () => {
      pending.current++;
      busy.current = false;
    };
  }, [loadGroups]);

  const refreshGroups = useEffectEvent((background: boolean) => void loadGroups("", background));
  useEffect(() => {
    if (lastRefresh.current === refresh.sequence) return;
    lastRefresh.current = refresh.sequence;
    refreshGroups(refresh.background);
  }, [refresh.sequence, refresh.background]);

  useEffect(
    () => () => {
      memberRequest.current++;
    },
    [],
  );

  const loadMembers = async (group: DuplicateGroup, cursor = "") => {
    const sequence = ++memberRequest.current;
    setMemberLoading(true);
    setMemberError("");
    try {
      const reply = await fileCatalogCli.listDuplicateMembers({ signature: group.signature, query, cursor, limit: 50 }).response;
      if (sequence !== memberRequest.current) return;
      if (
        reply.indexRevision !== indexRevision.current ||
        !reply.group ||
        reply.group.originalCount < 2n ||
        groupFingerprint(reply.group) !== groupFingerprint(group)
      ) {
        setStale(true);
        clearMembers();
        return;
      }
      const values = cursor ? [...members, ...reply.members] : reply.members;
      setMembers(values);
      setMemberCursor(reply.nextCursor);
      onFiles(duplicateFiles(values));
    } catch (error) {
      if (sequence === memberRequest.current) setMemberError(errorMessage(error, "Could not load group files"));
    } finally {
      if (sequence === memberRequest.current) setMemberLoading(false);
    }
  };

  const activeGroup = groups.find((group) => contentHex(group.signature) === active);

  return (
    <div className="duplicate-results" aria-label="Duplicate content groups">
      <div className="duplicate-results-heading">
        <div>
          <strong>Duplicate content</strong>
          <span>
            {groups.length} {groups.length === 1 ? "group" : "groups"}
            {nextCursor ? " loaded" : ""}
          </span>
        </div>
        <Button size="small" startIcon={<RefreshRoundedIcon />} disabled={loading} onClick={() => void loadGroups()}>
          Refresh
        </Button>
      </div>
      {loading && <LinearProgress aria-label="Loading duplicate groups" />}
      {error && (
        <Alert severity="error">
          {error}
          <Button onClick={() => void loadGroups(failedGroupCursor.current)}>Retry groups</Button>
        </Alert>
      )}
      {stale && (
        <Alert severity="info">
          Results changed — refresh to continue.<Button onClick={() => void loadGroups()}>Refresh results</Button>
        </Alert>
      )}
      {!loading && !error && !groups.length && <p className="duplicate-empty">No matching duplicate content.</p>}
      <GroupedFileList
        groups={groups.map((group) => ({
          id: contentHex(group.signature),
          name: group.name,
          badge: `${group.originalCount} files`,
          description: [
            `${group.locationCount} ${group.locationCount === 1n ? "Location" : "Locations"}`,
            group.archivedCopies ? `${group.archivedCopies} archived ${group.archivedCopies === 1n ? "copy" : "copies"}` : "No archived copies",
            ...(group.size !== undefined ? [formatFilesize(group.size)] : []),
            ...(group.matchingCount !== group.originalCount ? [`${group.matchingCount} matching`] : []),
          ].join(" · "),
        }))}
        activeGroupId={active}
        disabled={stale || loading}
        onGroupChange={(id) => {
          clearMembers();
          setCopyError("");
          setActive(id ?? "");
          const group = groups.find((value) => contentHex(value.signature) === id);
          if (group) void loadMembers(group);
        }}
        beforeFiles={
          <>
            {memberLoading && <LinearProgress aria-label="Loading group files" />}
            {memberError && (
              <Alert severity="error">
                {memberError}
                <Button onClick={() => activeGroup && void loadMembers(activeGroup, memberCursor)}>Retry files</Button>
              </Alert>
            )}
          </>
        }
        afterFiles={
          <>
            {!stale && memberCursor && (
              <Button size="small" disabled={memberLoading} onClick={() => activeGroup && void loadMembers(activeGroup, memberCursor)}>
                Load more files
              </Button>
            )}
            <details className="duplicate-signature">
              <summary>Signature</summary>
              <code>{active}</code>
              <Button
                size="small"
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(active);
                    setCopyError("");
                  } catch {
                    setCopyError("Could not copy. Select the signature text to copy it.");
                  }
                }}
              >
                Copy signature
              </Button>
              {copyError && <p>{copyError}</p>}
            </details>
          </>
        }
      />
      {!stale && nextCursor && (
        <Button disabled={loading} onClick={() => void loadGroups(nextCursor)}>
          Load more groups
        </Button>
      )}
    </div>
  );
};
