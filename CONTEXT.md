# YATM Domain

YATM organizes ongoing files independently of their originals and archival copies. This glossary defines the domain; the [Library contract](docs/architecture/library.md) owns implemented behavior. The [documentation index](docs/README.md#pre-stable-compatibility) owns the pre-stable compatibility policy.

## Language

These are the domain's words, each with the alternatives to avoid. The list is closed: a new
concept, or a new name for one already here, needs explicit developer authorization.
A `_Chinese_` line gives the fixed Simplified Chinese rendering the user interface uses for that word; it never replaces the English term in code or documentation.

**Library**:
The catalog of logical Files, annotations, originals, saved versions and Media inventory. Membership does not require a saved version or archived copy; logical organization is independent of an original's presence or physical directory structure.
_Avoid_: source filesystem, backup directory
_Chinese_: 资料库

**File**:
An independently organized ongoing item, or a directory in the logical tree. Its content can change while its identity, logical path, tags and note remain.
_Avoid_: immutable content, physical path
_Chinese_: 文件

**FileLocation**:
A File's sole original association, with its Location, relative path, last observed physical facts and optional content signature. Retaining the association does not prove that the original still exists or is unchanged.
_Avoid_: archived copy, retained historical bytes
_Chinese_: 原件关联

**FileVersion**:
One content state of a File known to have been saved on archive Media, with its own restore metadata. Several physical copies of that content belong to the same version, regardless of which File initiated their archival.
_Avoid_: backup Job, physical copy, mutable current content, edit event, directory snapshot
_Chinese_: 文件版本

**Signature**:
Opaque bytes identifying content. Exact equality finds duplicate content and physical copies, without merging Files or histories. Unset bytes are NULL and cannot establish content equality.
_Avoid_: File ID, pathname, tracking UUID, ACP cache entry
_Chinese_: 签名

**Position**:
A physical archive path and recorded content on Media. It is independent inventory, queried by signature rather than owned by a File or FileVersion. Derived directory rows support physical browsing.
_Avoid_: logical File, deletion unit
_Chinese_: 介质位置

**Location**:
A registered directory in an Executor's access namespace for live filesystem browsing, physical file management and authorized Restore output. A preferred Restore destination is a chooser preference, not an access permission. It is configured in Settings; its metadata lives in the Library.
_Avoid_: Media, temporary Source selection, device identity
_Chinese_: 存储位置

**Tracking key**:
Private evidence for continuity from a scoped native filesystem identity. Shared identifiers do not prove shared File identity.
_Avoid_: content signature, File ID
_Chinese_: 追踪密钥

**Media**:
Archive storage identified as Tape or Volume. Removability and accessibility do not define archive status.
_Avoid_: original Location, Job
_Chinese_: 介质

**Tape**:
An archive medium identified by its barcode and immutable storage profile, using LTFS or tar.
_Avoid_: drive, mount point
_Chinese_: 磁带

**Volume**:
An already-mounted archive filesystem registered with an immutable YATM marker. It may remain mounted permanently.
_Avoid_: any visible directory, original Location
_Chinese_: 卷

**Device settings**:
User-owned settings for one configured Tape device: write defaults, read order, capacity polling, read parameters (at most one set per recorded density code) and read planning budget. They never grant device access, and each Media operation freezes them when admitted.
_Avoid_: device preferences, drive profile, cartridge settings, device registry
_Chinese_: 设备设置

**Position map**:
Spatial facts about a Tape's recorded layout, with their source. It is derived from the current observations of one read Session and is never stored.
_Avoid_: cartridge position table, index, physical guarantee
_Chinese_: 位置映射

**Read order**:
The Device setting that orders a Tape read. Logical keeps recorded order; Auto adopts a planned order only when its inputs apply and it is predicted to save at least one positioning base time over Logical, otherwise it keeps Logical.
_Avoid_: route (outside planner internals), read plan, speedup guarantee
_Chinese_: 读取顺序

**Streaming distance**:
How far logically ahead a Tape drive reaches by continuing to read instead of positioning. A target within it costs the skipped data at reading speed plus one approach reversal for each wrap turn crossed; it is a read parameter.
_Avoid_: read-ahead buffer, drive buffer, drive cache, prefetch window
_Chinese_: 流式距离

**Read-speed locate**:
A move beyond the streaming distance to a target ahead in the same wrap and half that the drive covers at reading speed: the shared positioning base time plus the skipped records at the read rate.
_Avoid_: short seek, reading through a gap
_Chinese_: 读速定位

**Prepared read**:
The record of a Job's most recently prepared Tape read on a device: requested and prepared order, parameter and position sources, applicability and coverage. It describes preparation, not the loaded cartridge or the order finally executed.
_Avoid_: read optimization, execution plan, ETA, current cartridge report
_Chinese_: 预备读取

**Calibration**:
A Job that measures a Tape drive's positioning times on one cartridge and stores them as the device's read parameters for that recorded density.
_Avoid_: drive benchmark, read learning, automatic tuning
_Chinese_: 校准

**Analyze**:
A Scan configured to observe Location ranges and optionally publish original associations. Basic collection uses known-only content facts. Ordinary browsing does not require analysis.
_Avoid_: archive copying, filesystem snapshot
_Chinese_: 分析

**File operation**:
A request-bound organization operation using shared planning and guarded Library/filesystem primitives. Physical moves update original associations without changing Library organization. Same-name directories merge; files never overwrite.
_Avoid_: Job, cross-Location transfer, filesystem transaction
_Chinese_: 文件操作

**Archive**:
Copying selected content to Media, verifying physical publication, and recording the File's saved content version. It is also the word for the Job kind that performs it, on every surface.
_Avoid_: backup, indexing an original in place
_Chinese_: 归档

**Target Media count**:
An Archive's optional number N: it copies only content held by fewer than N distinct Media, counting usable copies from any Job. Only known, confirmed content can be skipped; unknown content is always copied.
_Avoid_: backup count, min copies, min media
_Chinese_: 目标介质数量

**Restore**:
Copying an explicitly selected FileVersion from an available matching Position and verifying the output.
_Avoid_: direct original read, restoring unspecified current content
_Chinese_: 恢复

**Restore result**:
The recorded outcome of restoring one selected version, including its actual output and any resulting original association. Retaining damaged recovery output does not prove that the selected content was restored.
_Avoid_: new backup, copy ownership, pending execution log
_Chinese_: 恢复结果

**Verify**:
Scan's actual-content examination of recorded archive copies against frozen expected facts. Its historical observations describe copy health without changing expected content identity.
_Avoid_: inventory Scan, automatic repair, replacement checksum
_Chinese_: 校验（界面动作 Check integrity：检查完整性）

**Scan**:
A configurable background pipeline over Location/Library selections or Media, with shared enumeration, signature policy, optional comparison/Preview and selected result publication. Inventory publication and recorded-copy verification are distinct result policies, not separate Jobs.
_Avoid_: backup copying, automatic Library identity creation, filesystem repair
_Chinese_: 扫描

**Preview**:
Content-addressed derivative assets. Current-original and saved-version views are distinct even when assets are shared.
_Avoid_: backup copy
_Chinese_: 预览

**Job**:
A durable Archive, Restore, Scan or Calibration operation with one lifecycle state. Preview and integrity checking are Scan configurations. Ordinary Library and Location file actions do not create Jobs.
_Avoid_: backup, background task
_Chinese_: 任务

**Job state**:
The one durable lifecycle word a Job reports. `PREPARING` means its manifest is still being built, `READY` means the manifest is complete and the remaining work waits for the operator to choose Media, `COMPLETED` means every workflow item settled, and `FAILED` means preparation, Scan or Calibration execution ended with an error or an operator cancellation. These failures are terminal; create a new Job. Archive and Restore Media failures return to the state before that Media operation, retaining its reason and published results so the operator can select Media again. Execution is not a state: an active operation reports a live phase, including `QUEUED` while waiting for its Media resource. An idle Job reports no phase even when its runner is cached.
_Avoid_: running state, outcome, status plus result, retry
_Chinese_: 任务状态

**Executor**:
The owner of Job execution, accessible paths, cancellation and attempt-scoped Media allocation.
_Avoid_: Library organization
_Chinese_: 执行器

**Unforged**:
The logical Library area for newly admitted items awaiting organization.
_Chinese_: 未整理

**Trash**:
Library's logical holding area, or a Location's owned `.trash` directory. Delete retains bytes and saved Library history; deleting a Location entry detaches its original association. Move can return entries to ordinary directories.
_Chinese_: 回收站

## Invariants and Navigation

A File has at most one original. Different Files may have equal signatures and query the same Positions without sharing organization. Library moves never rename physical originals or archive files. Known archived coverage of an observed content state establishes a saved version without another physical backup; [Library](docs/architecture/library.md#archive-inventory) owns publication and evidence rules.

Matching is the deterministic continuity policy path → available signature → native identity; it does not prove a move occurred. Retained metadata does not retain bytes. [Library](docs/architecture/library.md) owns identities and cleanup; [Jobs](docs/architecture/jobs.md) owns execution and recovery; [Media/I/O](docs/architecture/media-io.md) owns physical safety.
