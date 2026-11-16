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
- [ ] Stop printing the v0.1.x Tape adapter notice ("Keep legacy captures through migration…") on v1-to-v1 upgrades.
- [ ] Make the release CI independent of live ffmpeg.org downloads. The pinned FFmpeg archive download for the Preview fixture and builds failed three times on 2026-11-02 with connection resets.
- [ ] Test the MySQL Catalog option and its migration path. The configuration still offers an untested MySQL dialect, and the v0.1.x import keeps MySQL branches; cover a fresh MySQL installation, the v0.1.x import into MySQL, and the Catalog operations SQLite tests cover today, or remove the option.
- [ ] Keep Media backends out of the Catalog. Backends in `internal/executor` look up Media records, decide FORMAT/APPEND eligibility, decode stored keys, recheck frozen read expectations and write position maps through the Library. Jobs should resolve the Media record and profile once per attempt, pass it to the backend and persist what the backend returns, leaving backends to operate the device.
