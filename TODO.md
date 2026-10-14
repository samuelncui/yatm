# TODO

Confirmed pending work. Keep entries concise, link a source Issue when available, and remove completed
items. Detailed designs belong in an Issue or pull request; this list does not define
implemented behavior or authorize API/data changes.

- [ ] Add Archive selection guidance ([#20](https://github.com/samuelncui/yatm/issues/20)). Explain **Add to list** and dragging into the selection.
- [ ] Research selecting newly added files for Archive ([#12](https://github.com/samuelncui/yatm/issues/12)). Define how this relates to changed content and existing archived coverage. A path/name match alone cannot establish that current content is already archived. Preserve Recreate's review and explicit submission flow.
- [ ] Fix inconsistent loading behavior in Settings. Review initial loading, switching and saving so pending and error feedback stay coherent.
- [ ] Research a tar Tape backend for LTO-4 and older drives ([#14](https://github.com/samuelncui/yatm/issues/14)). Define Archive/Restore, inventory and Media identity requirements before choosing an implementation.
- [ ] Research indexing CD/DVD/Blu-ray archives ([#16](https://github.com/samuelncui/yatm/issues/16)). Define read-only inventory and offline browsing requirements, and assess what existing Location/Scan behavior can provide.
