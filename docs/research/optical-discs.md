# Optical Disc Archives

Status: Research reviewed on 2026-11-22 for [Issue #16](https://github.com/samuelncui/yatm/issues/16).
YATM behavior was read from the v1 development line at commit `f6ff4dd3`. External sources are
ECMA-119 4th edition (ISO 9660), ECMA-167 3rd edition and OSTA UDF 2.60, Linux `v6.12` kernel
documentation, util-linux libblkid, and the public tool documentation cited in place. No optical
drive or disc was tested. This study describes what is known and what remains open. It does not
authorize implementation, schema or API changes, and it does not define a product guarantee.

## Objective

Define what read-only inventory and offline browsing of CD, DVD and Blu-ray archives would need,
and assess how much of it existing YATM behavior already provides. The issue asks for "some form of
indexing of read only media" because discs may outlast magnetic Tape in cold storage. Burning discs,
audio CDs without a filesystem, and copy-protected video are outside this study.

## Vocabulary Note

The closed [glossary](../../CONTEXT.md) has no word for a read-only medium that YATM neither wrote
nor marked. **Volume** is the closest concept, but it is defined by an immutable YATM marker. This
study therefore uses "disc" and "image" (an `.iso` file) as plain descriptive words. They are not
proposed domain terms; any product term needs explicit developer authorization.

## User Needs

These needs come from the issue, the feature sets of the catalog tools below and the preservation
sources. They are requirements to evaluate, not confirmed product scope.

| Need | What the operator does | Required evidence |
| --- | --- | --- |
| Identify a disc | Insert an unlabelled or labelled disc and learn which catalog record it is | An identity read from the disc that survives remounting and another host |
| Browse offline | Open the directory tree of a disc that sits on a shelf | Recorded paths, sizes and times; optionally Preview assets |
| Find the holding disc | Search by name or content and learn which disc to fetch | Search over recorded inventory and signatures |
| Count copies | Know whether live content already exists on one or more discs | Signature equality between originals and disc content |
| Verify readability | Periodically reread a disc and record which files still read correctly | Actual reads against recorded hashes, with per-file findings |
| Restore | Copy a selected saved content state from a disc back to a Location | A recorded copy on an identified disc |
| Group sets | Know that discs 1 to N belong together | A grouping fact on Media, not on Files |

## Sourced Findings

### Filesystems

- **ISO 9660** ([ECMA-119](https://ecma-international.org/publications-and-standards/standards/ecma-119/))
  stores a Primary Volume Descriptor with a 32-character Volume Identifier, a Volume Set
  Identifier, Publisher, Data Preparer and Application Identifiers, creation and modification dates,
  and Volume Set Size and Volume Sequence Number fields intended for multi-volume sets. Interchange
  levels limit names (level 1: 8.3 names) and file extents; level 3 allows a file to span several
  extents, which large files need. Recorded times have one-second resolution with a time-zone offset.
- **Joliet** adds a Supplementary Volume Descriptor with UCS-2 names; **Rock Ridge** adds POSIX
  names, permissions, symlinks and link counts through System Use entries. A disc may carry several
  name trees for the same data.
- **UDF** ([ECMA-167](https://ecma-international.org/publications-and-standards/standards/ecma-167/),
  [OSTA UDF 2.60](http://www.osta.org/specs/pdf/udf260.pdf)) is used by DVD-Video, Blu-ray and many
  burning tools, often as a "bridge" disc that also carries ISO 9660 structures.
- **Linux** mounts these through `isofs` and `udf`, always read-only for pressed and closed discs.
  The [isofs documentation](https://github.com/torvalds/linux/blob/v6.12/Documentation/filesystems/isofs.rst)
  provides `norock` and `nojoliet` to disregard either extension, and `session=` and `sbsector=` to
  select a session of a multisession disc; the default shows the last session. The
  [UDF documentation](https://github.com/torvalds/linux/blob/v6.12/Documentation/filesystems/udf.rst)
  covers UDF options. Drive state, disc status and eject are exposed through the
  [CD-ROM ioctls](https://github.com/torvalds/linux/blob/v6.12/Documentation/userspace-api/ioctl/cdrom.rst).
- **Identifiers seen by userspace.** libblkid reports `TYPE` (`iso9660` or `udf`), `LABEL` from
  the Volume Identifier, the ISO 9660 identifier strings as separate tags, and an ISO 9660 `UUID`
  built from the volume modification date, falling back to the creation date, with no UUID when the
  dates are unset ([libblkid manual](https://man7.org/linux/man-pages/man3/libblkid.3.html),
  [util-linux iso9660 probe](https://github.com/util-linux/util-linux/blob/v2.40/libblkid/src/superblocks/iso9660.c)).
  That UUID is a timestamp, not a random value.
- **Image files** carry the same filesystem; a loop mount presents them like a physical disc.

### Longevity and damage

- The NIST/Library of Congress study estimated most tested recordable DVD products above 30 years
  but with large variation between products, CDs tending to outlast DVDs, and stressed that
  accelerated ageing is a broad estimate and that readability also depends on surviving drives
  ([NIST SP 500-263](https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication500-263.pdf),
  [Library of Congress project page](https://loc.gov/preservation/scientists/projects/cd-r_dvd-r_rw_longevity.html)).
- dvdisaster documents that drive firmware retries an unreadable sector several times before
  returning a read error, recommends several passes with different drives, and recommends copying a
  failing disc to new media rather than relying on repair
  ([dvdisaster manual](https://manpages.ubuntu.com/manpages/plucky/man1/dvdisaster.1.html)).
  GNU ddrescue records unreadable areas in a map file so later passes retry only those areas
  ([ddrescue manual](https://www.gnu.org/software/ddrescue/manual/ddrescue_manual.html)).

### What other tools do

| Tool | Identity | Offline browsing and search | Integrity |
| --- | --- | --- | --- |
| [Virtual Volumes View](https://vvvapp.sourceforge.net/) | Operator-named volume in a portable catalog | Yes, plus a virtual folder view over physical volumes | No |
| [git-annex offline archive drives](https://git-annex.branchable.com/tips/offline_archive_drives/) | Repository UUID stored on the writable drive | Location tracking per key while the drive is offline | `fsck` rereads content; `numcopies` counts copies |
| [dvdisaster](https://manpages.ubuntu.com/manpages/plucky/man1/dvdisaster.1.html) | Image-level | No | Whole-image ECC and sector read statistics |
| [MusicBrainz Disc ID](https://musicbrainz.org/doc/Disc_ID_Calculation) | Hash of an audio CD's table of contents | Not applicable | Not applicable |

Catalog tools identify discs by an operator-assigned name; git-annex needs a writable medium for
its identity; image-level tools identify content, not a physical disc.

## YATM Fit Assessment

Each row compares a need with the current owner's behavior. "Provided" means current behavior
already does it for a disc presented in that shape; it is not a support claim.

### Disc registered as a Location

| Need | Current behavior | Assessment |
| --- | --- | --- |
| Browse while mounted | [Location](../architecture/library.md#registered-originals) browsing reads the live directory | Provided |
| Browse offline | Live Location browsing never falls back to an offline listing; admitted Files remain browsable in the Library | Partial: only the logical Library tree, not the disc's own tree |
| Find content | Scan with `PUBLISH_ORIGINALS` admits Files with signatures; Search supports `location:<id>` | Provided while each disc has its own Location |
| Unreadable files | A [Location Scan](../architecture/jobs.md#scan) records a per-file `UNREADABLE` finding and continues | Provided |
| Count copies / Restore | Archive coverage, Target Media count and Restore read Positions on Media, not originals | Missing: disc content would be originals, not archived copies |
| Disc swapped at one mount point | A complete observed range can remove original references; an empty mount point looks empty | Unsafe: a whole-Location Scan with the wrong or no disc publishes disappearance |

The glossary defines a Location as live originals and lists "device identity" under words to
avoid, so this shape contradicts the domain even where it works mechanically.

### Disc presented as a Volume

| Need | Current behavior | Assessment |
| --- | --- | --- |
| Identify | [Volume discovery](../architecture/media-io.md#volume) reads `.yatm.json` at a configured root or direct child | Works only for a disc that already contains a marker |
| Inventory | Volume inventory Scan enumerates the mounted tree and publishes Positions | Provided; the read Session writes nothing |
| Browse and find offline | Positions and derived directory rows are browsable from the catalog; Add to Library admits signed entries | Provided |
| Count copies / Restore | Positions count toward Media count and serve Restore candidates | Provided |
| Verify | `VERIFY_COPIES` rereads against recorded hashes and records [copy health](../architecture/library.md#copy-health) | Provided; per-file versus device-error classification of `EIO` on a disc is unverified |
| Preview | Preview accepts supported mounted Media | Provided for random-readable Volumes |
| Register an unmarked disc | Initialize creates the marker by exclusive file creation | Missing: impossible on a read-only filesystem |
| Signature cache | Cache refresh writes xattrs; failed cache writes are counted, never fatal | Tolerated; non-refreshing policy avoids the attempts |
| Archive to it | A registered Volume is Archive-eligible | Mismatch: writes would fail on a read-only mount |
| Profile | `VolumeType` is HDD or HM-SMR, both concurrent random read | Mismatch: an optical drive is one slow head; concurrent reads likely thrash |
| Serial number | Initialize reads the block device serial from sysfs | Wrong source: for `/dev/sr*` this is the drive's serial, not the disc's |
| Several burned copies of one image | One marker UUID; discovery rejects duplicate mounted identities | Mismatch: copies become one Media, merging copy count and health |

A disc burned from a directory that already holds a marker is therefore close to fully served by
current Volume behavior. Discs without a marker, which includes every pressed disc and every
existing personal collection, have no registration path.

### Image files

An `.iso` file in a Location is one ordinary File: it can be archived, signed and verified as a
whole, but its contents are not inventory. A loop-mounted image behaves like a disc under a Volume
root, with the same marker requirement. YATM does not mount filesystems, so loop setup stays an
operator action. An image avoids drive wear and can be verified quickly against a whole-image hash.

### Multi-disc sets

YATM has no grouping fact for Media; Tags belong to Files. ISO 9660 has Volume Set fields, but this
study found no evidence on how often burning tools fill them meaningfully. A file split across discs
by the producing tool is several ordinary files to YATM; reassembly is outside its model.

## Community Observations

- The issue author observes that optical media may outlast Tape in cold storage; the NIST/LoC
  results support long life for some products but show wide product variation.
- Catalog tools in this space are long-lived but slowly maintained; user reviews describe free
  catalogers as unmaintained, although Virtual Volumes View has since published release 1.5
  ([AlternativeTo reviews](https://alternativeto.net/software/virtual-volumes-view/about/)). Their common model is an operator-named volume plus a searchable
  tree, without integrity checking.
- git-annex users report that copy counting works best when every archive drive is a known remote,
  which mirrors YATM's need for each disc to be a known Media.

## Hypotheses

- A disc identity read only from filesystem facts is not unique: Volume Identifiers are often
  generic, timestamp UUIDs collide for copies of one image, and identical copies are by design
  indistinguishable by content. An operator-assigned identity, with recorded filesystem facts used as
  a mismatch check, would likely be required for physical discs.
- Recorded paths depend on which name tree was mounted (Rock Ridge, Joliet, plain ISO 9660 or UDF),
  so the same disc could produce different Positions on another host or with other mount options.
- isofs and udf probably supply no filesystem UUID through `FS_IOC_GETFSUUID`, so [tracking
  keys](../architecture/library.md#registered-originals) would yield no native key on a disc.
- A scratched disc can stall one read for minutes inside drive retries; a native read blocked in the
  kernel is not interruptible by Job cancellation.
- An open multisession disc changes content when a session is appended; only a closed disc is a
  stable inventory target.

## Open Questions

1. Should a disc be Media at all, given that a Volume is defined by its marker? Accepting unmarked
   read-only Media would change the glossary, the Media identity contract and its schema, and needs
   explicit authorization.
2. Which identity would be acceptable for an unmarked disc: an operator label, a whole-image hash, a
   set of filesystem descriptor fields, or a combination with a defined conflict rule?
3. Are physical copies of one burned image separate Media (independent health and copy count) or one?
4. Does the Volume Verify path classify `EIO` from a bad sector as that file's `Unreadable` finding or
   as a device error that stops the scope, and what does the operator see for a stalled read?
5. Which name tree should be authoritative when a disc carries several, and must it be frozen with
   the Media?
6. Is a dedicated read profile for optical drives needed, or is serial access through the existing
   capability table enough?
7. Is a Media grouping fact for sets a demonstrated requirement, or do names and Notes suffice?

## Evaluation Criteria

A future proposal should be judged against:

- no new domain term or schema change without explicit authorization, reusing Media, Position,
  Scan inventory and Verify where they already fit;
- an identity rule that cannot attach one disc's inventory to another disc, including when a mount
  point is empty or holds the wrong disc;
- no inventory replacement after an incomplete or failed read, as current Media inventory already
  guarantees;
- per-file findings for unreadable files without treating whole-drive unavailability as corruption;
- read-only behavior: no marker writes, cache writes or Archive eligibility on a read-only disc;
- automated checks using loop-mounted images of ISO 9660, Joliet, Rock Ridge and UDF, plus a
  documented physical acceptance for real drives and damaged discs.
