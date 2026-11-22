# TODO

Confirmed pending work. Keep entries concise, link a source Issue when available, and remove completed
items. Detailed designs belong in an Issue or pull request; this list does not define
implemented behavior or authorize API/data changes.

- [ ] Add Archive selection guidance ([#20](https://github.com/samuelncui/yatm/issues/20)). Explain **Add to list** and dragging into the selection.
- [ ] Read-only optical disc support for v1.0.0-alpha.5 ([#16](https://github.com/samuelncui/yatm/issues/16); [research](docs/research/optical-discs.md)). Decided: an optical drive is a configured Device, and Device settings extend from Tape devices to Tape or Disc devices without a new drive term; a disc is a third Media kind, **Disc** (Chinese 光盘), whose glossary entry is authorized (avoid Volume, disk and optical media; CD/DVD/BD are formats). A Disc is identified by an operator-assigned label like a Tape barcode, is never written and is not an Archive target. Automated checks use loop-mounted `xorriso` images, a QEMU virtual CD drive and device-mapper error/delay targets; a USB reader provides physical acceptance. The design proposal awaits approval.
- [ ] Per-file compression decided by extension for v1.0.0-alpha.5. Decided: archived files are compressed by default except a case-insensitive exclusion list of already-compressed formats, and each file is compressed independently rather than in a shared stream. Tar on Tape must stay recoverable with standard tools. The design proposal awaits approval.
