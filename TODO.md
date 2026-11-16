# TODO

Confirmed pending work. Keep entries concise, link a source Issue when available, and remove completed
items. Detailed designs belong in an Issue or pull request; this list does not define
implemented behavior or authorize API/data changes.

- [ ] Add Archive selection guidance ([#20](https://github.com/samuelncui/yatm/issues/20)). Explain **Add to list** and dragging into the selection.
- [ ] Research indexing CD/DVD/Blu-ray archives ([#16](https://github.com/samuelncui/yatm/issues/16)). Define read-only inventory and offline browsing requirements, and assess what existing Location/Scan behavior can provide.
- [ ] Test the MySQL Catalog option and its migration path. The configuration still offers an untested MySQL dialect, and the v0.1.x import keeps MySQL branches; cover a fresh MySQL installation, the v0.1.x import into MySQL, and the Catalog operations SQLite tests cover today, or remove the option.
