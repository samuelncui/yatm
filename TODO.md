# TODO

Confirmed pending work. Keep entries concise, link a source Issue when available, and remove completed
items. Detailed designs belong in an Issue or pull request; this list does not define
implemented behavior or authorize API/data changes.

- [ ] Add Archive selection guidance ([#20](https://github.com/samuelncui/yatm/issues/20)). Explain **Add to list** and dragging into the selection.
- [ ] Research selecting newly added files for Archive ([#12](https://github.com/samuelncui/yatm/issues/12)). Define how this relates to changed content and existing archived coverage. A path/name match alone cannot establish that current content is already archived. Preserve Recreate's review and explicit submission flow.
- [ ] Qualify the software position estimator, read model and cartridge-memory decoding beyond the studied HP LTO-5 configuration ([#14](https://github.com/samuelncui/yatm/issues/14)).
- [ ] Revisit LTFS passive read learning after resolving its measurement ownership. Deferred: preserve native ACP reads and keep LTFS observations separate from native tar measurements.
- [ ] Research indexing CD/DVD/Blu-ray archives ([#16](https://github.com/samuelncui/yatm/issues/16)). Define read-only inventory and offline browsing requirements, and assess what existing Location/Scan behavior can provide.
- [ ] Find the cause of the LTFS final Index failure in the 2026-11-05 NAS physical test. Unmount got `READ POSITION` timeouts while the configured `umount` (which skipped its device-release wait) polled `mt status`; confirm with the corrected wait, and decide whether the bundled `umount` should avoid opening the device until LTFS has exited.
- [ ] Tell operators in the user guides that Archive writes ACP's signature-cache xattr onto originals (a read-only source only logs a warning), and decide whether an operator can turn it off.
- [ ] Stop printing the v0.1.x Tape adapter notice ("Keep legacy captures through migration…") on v1-to-v1 upgrades.
- [ ] Make the release CI independent of live ffmpeg.org downloads. The pinned FFmpeg archive download for the Preview fixture and builds failed three times on 2026-11-02 with connection resets.
