# YATM Domain

YATM organizes ongoing files independently of their online originals and archival copies. This glossary defines the domain; the [Library contract](docs/architecture/library.md) owns implemented behavior. The [documentation index](docs/README.md) owns the published Alpha compatibility policy and the exception for unpublished Draft data.

## Language

**Library**:
The catalog of logical Files, annotations, originals, saved versions and Media inventory. Membership does not require a backup; logical organization is independent of an original's presence or physical directory structure.
_Avoid_: source filesystem, backup directory

**File**:
An independently organized ongoing item, or a directory in the logical tree. Its content can change while its identity, logical path, tags and note remain.
_Avoid_: immutable content, physical path

**FileLocation**:
A File's sole original association, with its Location, relative path, last observed physical facts and optional content signature. Retaining the association does not prove that the original still exists or is unchanged.
_Avoid_: archived copy, retained historical bytes

**FileVersion**:
One content state of a File known to have been saved on archive Media, with its own restore metadata. Several physical copies of that content belong to the same version, regardless of which File initiated their archival.
_Avoid_: backup Job, physical copy, mutable current content, edit event, directory snapshot

**Signature**:
Opaque bytes identifying content. Exact equality finds duplicate content and physical copies, without merging Files or histories. Unset bytes are NULL and cannot establish content equality.
_Avoid_: File ID, pathname, tracking UUID, ACP cache entry

**Position**:
A physical archive path and recorded content on Media. It is independent inventory, queried by signature rather than owned by a File or FileVersion. Derived directory rows support physical browsing.
_Avoid_: logical File, deletion unit

**Location**:
A registered directory in an Executor's access namespace for live filesystem browsing, physical file management and authorized Restore output. A preferred Restore destination is a chooser preference, not an access permission. It is configured in Settings; its metadata lives in the Library.
_Avoid_: Media, temporary Source selection, device identity

**Tracking key**:
Private evidence for continuity, such as a scoped native identity or optional YATM UUID. Copied identifiers do not prove shared identity.
_Avoid_: content signature, File ID

**Media**:
Archive storage identified as Tape or Volume. Removability and accessibility do not define archive status.
_Avoid_: original Location, Job

**Tape**:
An LTFS archive medium identified by its barcode and immutable storage profile.
_Avoid_: drive, mount point

**Volume**:
An already-mounted archive filesystem registered with an immutable YATM marker. It may remain mounted permanently.
_Avoid_: any visible directory, original Location

**Source**:
A frozen legacy Job selection containing a filesystem base and selected paths; new workflows use registered Library/Location selections.
_Avoid_: registered Location, Media

**Analyze**:
A Scan configured to observe Location ranges and optionally publish original associations. Basic collection uses known-only content facts. Ordinary browsing does not require analysis.
_Avoid_: archive copying, filesystem snapshot

**File operation**:
A request-bound organization operation using shared planning and guarded Library/filesystem primitives. Physical moves update original associations without changing Library organization. Same-name directories merge; files never overwrite.
_Avoid_: Job, cross-Location transfer, filesystem transaction

**Archive**:
Copying selected content to Media, verifying physical publication, and recording the File's saved content version.
_Avoid_: indexing an original in place

**Restore**:
Copying an explicitly selected FileVersion from an available matching Position and verifying the output.
_Avoid_: direct original read, restoring unspecified current content

**Restore result**:
The recorded outcome of restoring one selected version, including its actual output and any resulting original association. Retaining damaged recovery output does not prove that the selected content was restored.
_Avoid_: new backup, copy ownership, pending execution log

**Verify**:
Scan's actual-content examination of recorded archive copies against frozen expected facts. Its historical observations describe copy health without changing expected content identity.
_Avoid_: inventory Scan, automatic repair, replacement checksum

**Scan**:
A configurable background pipeline over Location/Library selections or Media, with shared enumeration, signature policy, optional comparison/Preview and selected result publication. Inventory publication and recorded-copy verification are distinct result policies, not separate Jobs.
_Avoid_: backup copying, automatic Library identity creation, filesystem repair

**Preview**:
Content-addressed derivative assets. Current-original and saved-version views are distinct even when assets are shared.
_Avoid_: backup copy

**Job**:
A durable Archive, Restore or Scan operation with retryable execution state. Preview and integrity checking are Scan configurations. Ordinary Library and Location file actions do not create Jobs.

**Executor**:
The owner of Job execution, accessible paths, cancellation and attempt-scoped Media allocation.
_Avoid_: Library organization

**Unforged**:
The logical Library area for newly admitted items awaiting organization.

**Trash**:
A logical move retaining File identities and annotations, not physical deletion.

## Invariants and Navigation

A File has at most one online original. Different Files may have equal signatures and query the same Positions without sharing organization. Library moves never rename physical originals or archive files. Known archived coverage of an observed content state establishes a saved version without another physical backup; [Library](docs/architecture/library.md#archive-inventory) owns publication and evidence rules.

Matching is the deterministic continuity policy path → available signature → native identity → UUID; it does not prove a move occurred. Retained metadata does not retain bytes. [Library](docs/architecture/library.md) owns identities and cleanup; [Jobs](docs/architecture/jobs.md) owns execution and recovery; [Media/I/O](docs/architecture/media-io.md) owns physical safety.
