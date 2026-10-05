"""Portable legacy-fixture boundaries; no SSH or real backup inputs."""

import io
import json
from pathlib import Path
import tarfile
import tempfile
from types import SimpleNamespace
import unittest

from package_acceptance import check_arguments
from package_legacy_cases import check_legacy_package, prepare_metadata, snapshot_records


class LegacyFixtureTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def archive(self, entries, name="metadata.tar.gz"):
        path = self.root / name
        with tarfile.open(path, "w:gz") as archive:
            for name, data in entries:
                member = tarfile.TarInfo(name)
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        return path

    def test_copies_only_metadata_without_configuration_or_executables(self):
        source = self.archive([("./tapes.db", b"synthetic database"), ("./tapes.db-wal", b"synthetic wal"),
                               ("captured_indices/TEST01.schema", b"<index/>"), ("job-logs/1.log", b"example"),
                               ("config.yaml", b"deployment-specific value"), ("scripts/mount", b"exit 1"),
                               ("library.json", b"unrelated export"), ("yatm-httpd", b"not a fixture")])
        before = source.read_bytes()
        destination = self.root / "copy"
        prepare_metadata(source, destination)
        self.assertEqual(sorted(p.relative_to(destination).as_posix() for p in destination.rglob("*") if p.is_file()),
                         ["captured_indices/TEST01.schema", "job-logs/1.log", "tapes.db", "tapes.db-wal"])
        self.assertEqual((destination / "tapes.db").read_bytes(), b"synthetic database")
        self.assertEqual(source.read_bytes(), before)

    def test_rejects_escape_and_duplicate_evidence(self):
        for index, entries in enumerate(([('../outside', b"x")], [("/outside", b"x")],
                                         [("tapes.db", b"a"), ("tapes.db", b"b")])):
            with self.subTest(entries=entries):
                source = self.archive(entries, f"case-{index}.tar.gz")
                with self.assertRaises((RuntimeError, FileExistsError)):
                    prepare_metadata(source, self.root / f"copy-{index}")
        self.assertFalse((self.root.parent / "outside").exists())

    def test_rejects_metadata_links(self):
        source = self.root / "link.tar.gz"
        with tarfile.open(source, "w:gz") as archive:
            member = tarfile.TarInfo("tapes.db")
            member.type, member.linkname = tarfile.SYMTYPE, "../../another-catalog"
            archive.addfile(member)
        with self.assertRaisesRegex(RuntimeError, "only files and directories"):
            prepare_metadata(source, self.root / "copy")

    def test_legacy_package_identity_must_match_filename(self):
        name = "yatm-linux-amd64-v0.1.8.tar.gz"
        source = self.archive([("./VERSION", b"v0.1.8\n")], name)
        self.assertEqual(check_legacy_package(source), "v0.1.8")
        source = self.archive([("./VERSION", b"v0.1.7\n")], name)
        with self.assertRaisesRegex(RuntimeError, "VERSION differ"):
            check_legacy_package(source)

    def test_requires_both_legacy_inputs_and_separate_acceptance(self):
        args = SimpleNamespace(host="isolated-test", test_parent="/srv/acceptance", out=self.root / "report",
                               archive=Path("yatm-linux-amd64-v1.0.0-alpha.2.tar.gz"),
                               version="v1.0.0-alpha.2", commit="1" * 40, ltfs=False, preview_archive=None,
                               legacy_package=Path("yatm-linux-amd64-v0.1.8.tar.gz"), legacy_fixture=None)
        with self.assertRaisesRegex(RuntimeError, "both a legacy"):
            check_arguments(args)
        args.legacy_fixture = self.root / "metadata.tar.gz"
        check_arguments(args)
        args.ltfs = True
        with self.assertRaisesRegex(RuntimeError, "separately"):
            check_arguments(args)

    def test_roundtrip_compares_all_metadata_except_regenerated_fields(self):
        rows = [{"type": "header", "format": "yatm-library-backup", "version": 1},
                {"type": "location", "data": {"id": 1, "revision": 10, "config": {"ignore": {"text": ""}}}},
                {"type": "position", "data": {"id": 2, "media_id": 1, "path": "folder/", "is_dir": True}},
                {"type": "position", "data": {"id": 3, "media_id": 1, "path": "folder/file", "mtime_ns": "123"}},
                {"type": "end"}]
        path = self.root / "export.jsonl"

        def snapshot(values):
            path.write_text("".join(json.dumps(row) + "\n" for row in values))
            return snapshot_records(path)

        expected = snapshot(rows)
        changed = json.loads(json.dumps(rows))
        changed[1]["data"]["revision"] = 20
        changed[2]["data"]["id"] = 4
        self.assertEqual(snapshot(list(reversed(changed))), expected)
        for index, field, value in ((1, "config", {"ignore": {"text": "*.tmp"}}),
                                    (1, "id", 5), (2, "path", "other/"),
                                    (3, "id", 6), (3, "mtime_ns", "124")):
            with self.subTest(index=index, field=field):
                changed = json.loads(json.dumps(rows))
                changed[index]["data"][field] = value
                self.assertNotEqual(snapshot(changed), expected)
        self.assertNotEqual(snapshot(rows[:-1]), expected)
        self.assertNotEqual(snapshot(rows + [rows[3]]), expected)
        rows[1]["data"]["revision"] = 0
        with self.assertRaisesRegex(RuntimeError, "identity is invalid"):
            snapshot(rows)


if __name__ == "__main__":
    unittest.main()
