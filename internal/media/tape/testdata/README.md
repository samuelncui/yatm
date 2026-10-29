# Cartridge memory decoder fixtures

## Sanitization

The hardware captures below keep the exact protected table (bytes 36–63), unprotected table
(256–351) and v4 directory including all 80 entry checksums (1968–4543). In each v2 EOD page,
bytes 0–39 retain the original framing and numeric fields; only bytes 40–91 containing
identity/vendor information are cleared, and bytes 92–95 are recomputed with the ECMA-319 §13.2
Reed-Solomon remainder. All other image bytes, including unused pages and CM identity, are zero.
The original page table values and addresses are unchanged; unused page bodies are intentionally
not a fixture for their own decoders. `TestCartridgeDirectoryFixtureHashes` freezes the sanitized
SHA-256 values.

Each `*-endpoints.json` file contains the numeric endpoints independently returned by READ END OF
WRAP POSITION for the same observation, using the exclusive convention, and no cartridge or device
identity. Images and endpoints are fixed test input, not data generated from the production CM
decoder. They are same-cartridge consistency fixtures, not an independent hardware qualification set.

## Fixtures

`cm/hp-lto5-directory-v4.bin` is an 8,160-byte sanitized CM image captured from one HP LTO-5
configuration. Its v2 EOD pages start at 4544 and 4640, and its endpoints file has 23 entries.

`cm/hp-lto5-single-partition-v4.bin` is a later single-partition FORMAT on the assigned HP LTO-5. The
independent MAM partition list was `00020001`, while the CM still contains a structurally valid
version-2 EOD page `0x10e` with state zero. The active directory has 35 reported wraps, ending at
logical object 1,285,289. It proves that EOD page presence is not the active partition count; the
decoder receives that count from the device owner.

`cm/single-partition-v4.bin` is fully synthetic content in the same sanitized table framing. It has
only EOD page 0x104, three valid wraps 0–2, record counts 5/3/89 and filemark counts 1/1/1, giving
exclusive endpoints 6, 10 and 100. Its completed EOD totals are 97 records and 3 filemarks. All
other directory entries carry the invalid dataset marker; no observed hardware count is claimed.

Synthetic tests separately relocate tables/pages, vary partition starts and counters, and inject
malformed framing. Their checksum reference constructs the generator from roots α^126 through
α^129 over GF(256) with polynomial 0x11d and performs polynomial long division, independently of
the production shift-register implementation.
