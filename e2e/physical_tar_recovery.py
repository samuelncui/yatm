"""Independent Linux st / mt-st 1.8 reader for the three-file tar baseline, and its fixture oracle.

Inputs are an explicit device/record policy and the nine-file fixture oracle. An optional fourth
Tape file from the tar-fill stage is streamed through GNU tar and hashed per member against its
published size/SHA-256 list, without retaining the payload.
No YATM program, Catalog, Job bundle or saved physical locator is read.
Current encrypted Media also require an explicit adapter and temporary saved-key file.
The `fixture generate|check` actions create and check that oracle without Tape access.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import stat
import subprocess
import sys


# The production 24-bit transfer ceiling, rounded down to a whole tar block.
MAX_RECORD_BYTES = (0xffffff // 512) * 512
# GNU tar --to-command child: hash one member's actual stdin bytes and report its declared size.
MEMBER_HASH = """import hashlib, json, os, sys
digest, received = hashlib.sha256(), 0
for chunk in iter(lambda: sys.stdin.buffer.read(1 << 20), b""):
    digest.update(chunk)
    received += len(chunk)
print(json.dumps({"name": os.environ["TAR_FILENAME"], "size": int(os.environ["TAR_SIZE"]),
                  "received": received, "sha256": digest.hexdigest()}), flush=True)
"""


def require(value, reason):
    if not value:
        raise RuntimeError(reason)


def metadata(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode), f"not a regular file: {path}")
    return {"size": info.st_size, "mode": stat.S_IMODE(info.st_mode), "mtime_ns": str(info.st_mtime_ns),
            "xattrs": {name: base64.b64encode(os.getxattr(path, name)).decode()
                       for name in ("user.yatm_empty", "user.yatm_binary")}}


def facts(path):
    return {"size": path.lstat().st_size, "sha256": file_sha256(path), **metadata(path)}


def baseline_layout(record_bytes):
    """Five tar-block edges, three record edges and one long UTF-8 path."""
    require(type(record_bytes) is int and 0 < record_bytes <= MAX_RECORD_BYTES and record_bytes % 512 == 0,
            "generate requires explicit 512-aligned record bytes no greater than 0xfffe00")
    return [("format/empty", 0), ("format/one", 1), ("format/before-block", 511),
            ("format/one-block", 512), ("format/after-block", 513),
            ("append/before-record", record_bytes - 1), ("append/one-record", record_bytes),
            ("append/after-record", record_bytes + 1),
            ("append/" + "long-segment-" * 9 + "/UTF-8-雪 payload.bin", 1048613)]


def generate(root, manifest, record_bytes):
    names = baseline_layout(record_bytes)
    require(not root.exists() and not manifest.exists(), "fixture and manifest must both be new")
    root.mkdir(parents=True, mode=0o700)
    result = {}
    for index, (name, size) in enumerate(names):
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(hashlib.shake_256(name.encode()).digest(size))
        os.chmod(path, (0o600, 0o640, 0o750)[index % 3])
        os.setxattr(path, "user.yatm_empty", b"")
        os.setxattr(path, "user.yatm_binary", b"\x00\xff\n\x01" + name.encode())
        stamp = 1700000000123456789 + index * 10000003
        os.utime(path, ns=(stamp, stamp))
        result[name] = facts(path)
        require(result[name]["mtime_ns"] == str(stamp), "filesystem rounded nanosecond mtime")
    manifest.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n")
    os.chmod(manifest, 0o600)
    return result


def check(manifest, root, mapping=None, exact=False):
    expected = json.loads(manifest.read_text())
    mapping = mapping if mapping is not None else {name: name for name in expected}
    require(mapping and set(mapping) <= set(expected), "unknown/empty fixture selection")
    paths = set()
    for name, relative in mapping.items():
        path = root / relative
        require(not Path(relative).is_absolute() and ".." not in Path(relative).parts,
                "oracle path escaped destination")
        require(facts(path) == expected[name], f"content or metadata mismatch: {name}: {path}")
        paths.add(str(path.relative_to(root)))
    if exact:
        actual = {str(path.relative_to(root)) for path in root.rglob("*") if not path.is_dir() or path.is_symlink()}
        require(actual == paths, f"unexpected/missing outputs: {actual ^ paths}")
    return {"checked": len(mapping), "exact_regular_paths": exact}


def check_extracted(manifest, root, group):
    # Source-relative suffixes are fixture facts, not Library paths or tar locators.
    expected = {name for name in json.loads(manifest.read_text()) if name.startswith(group + "/")}
    mapping = {}
    for path in root.rglob("*"):
        if path.is_dir() and not path.is_symlink():
            continue
        relative = str(path.relative_to(root))
        names = [name for name in expected if relative == name or relative.endswith("/" + name)]
        require(len(names) == 1 and names[0] not in mapping, "unexpected or duplicate extracted fixture member")
        mapping[names[0]] = relative
    require(set(mapping) == expected, "public reader omitted fixture members")
    return check(manifest, root, mapping, exact=True)


def check_fill(records, members):
    """Each published member is complete exactly once; other members form only an unpublished tail."""
    names = [str(record.get("name", "")).removeprefix("./") for record in records]
    published = [index for index, name in enumerate(names) if name in members]
    require(sorted(names[index] for index in published) == sorted(members),
            "public reader did not hash every published fill member exactly once")
    for index in published:
        record, expected = records[index], members[names[index]]
        require(record.get("size") == record.get("received") == expected["size"]
                and record.get("sha256") == expected["sha256"], f"published fill member differs: {names[index]}")
    tail = [index for index, name in enumerate(names) if name not in members]
    require(not published or all(index > max(published) for index in tail),
            "unpublished members precede published fill content")
    return {"published_checked": len(published), "unpublished_tail": [{**records[index], "name": names[index]}
                                                                         for index in tail]}


def check_fill_members(value):
    require(isinstance(value, dict) and value and all(
        isinstance(name, str) and name and isinstance(row, dict) and type(row.get("size")) is int and row["size"] >= 0
        and isinstance(row.get("sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", row["sha256"])
        for name, row in value.items()), "fill members must map member paths to size/sha256")
    return value


def file_sha256(path):
    """Hash retained recovery bytes with bounded memory, independently of archive size."""
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_status(text):
    require(len(text.splitlines()) == 6 and re.search(r"^Soft error count since last status=\d+$", text, re.M),
            "Unknown mt-st status fields; physical boundary remains unknown")
    positions = re.findall(r"^File number=(-?\d+), block number=(-?\d+), partition=(\d+)\.$", text, re.M)
    blocks = re.findall(r"^Tape block size (\d+) bytes\. Density code .+$", text, re.M)
    flags = re.findall(r"^General status bits on \([0-9a-fA-F]+\):\n[ \t]+([A-Z_ ]+)\s*$", text, re.M)
    require(text.startswith("SCSI 2 tape drive:\n") and len(positions) == len(blocks) == len(flags) == 1,
            "Unknown mt-st status syntax; physical boundary remains unknown")
    flags = set(flags[0].split())
    require(flags and flags <= {"EOF", "EOD", "EOT", "DR_OPEN", "ONLINE", "IM_REP_EN", "BOT", "WR_PROT"},
            "Unknown mt-st status flags; physical boundary remains unknown")
    return {"position": tuple(map(int, positions[0])), "block_bytes": int(blocks[0]), "flags": flags}


def check_boundary(text, number, copied_bytes, end=False):
    status = parse_status(text)
    flags = status["flags"]
    if end:
        require(copied_bytes == 0 and "EOD" in flags and not flags & {"EOF", "EOT", "DR_OPEN"}
                and status["position"] == (number, -1, 0), "Normal recorded EOD was not independently established")
        return "DRIVER_REPORTED_EOD"
    require(copied_bytes > 0 and "EOF" in flags and not flags & {"EOD", "EOT", "DR_OPEN"}
            and status["position"] == (number + 1, 0, 0), "Expected physical FILEMARK was not established")
    return "FILEMARK"


def device_aliases(device):
    resolved = Path(device).resolve(strict=True)
    require(re.fullmatch(r"nst\d+[alm]?", resolved.name) and stat.S_ISCHR(resolved.stat().st_mode),
            "Public recovery requires an explicit non-rewinding Linux st character device")
    selected = Path("/sys/class/scsi_tape") / resolved.name / "device"
    target = selected.resolve(strict=True)
    aliases = {str(resolved), device}
    for entry in Path("/sys/class/scsi_tape").iterdir():
        if (entry / "device").resolve() == target and (Path("/dev") / entry.name).exists():
            aliases.add("/dev/" + entry.name)
    generic = list((target / "scsi_generic").iterdir())
    require(len(generic) == 1 and (Path("/dev") / generic[0].name).exists(), "Cannot establish the matching SG alias")
    aliases.add("/dev/" + generic[0].name)
    require(any(re.fullmatch(r"/dev/st\d+[alm]?", path) for path in aliases), "Rewinding alias is unknown")
    return str(resolved), sorted(aliases)


class PublicReader:
    def __init__(self, device, mode, record_bytes, fixture, output):
        require(mode in ("fixed", "variable") and 0 < record_bytes <= MAX_RECORD_BYTES and record_bytes % 512 == 0,
                "Supply the explicit tar record policy")
        self.device, self.mode, self.record_bytes = device, mode, record_bytes
        self.fixture, self.output = Path(fixture), Path(output)
        self.commands = []
        self.unsettled = False
        self.encryption = None
        self.fill_members = None

    def capture(self, name, argv, *, env=None, redact=()):
        require(not self.unsettled, "Command completion is unknown; retain the stopped owner")
        command = {"name": name, "argv": list(map(str, argv)), "unsettled": True}
        self.commands.append(command)
        receipt = self.output / "commands.json"
        receipt.write_text(json.dumps(self.commands, indent=2) + "\n")
        self.unsettled = True
        # The host-local owner waits for device completion; a watchdog must not kill an in-flight read or motion.
        process = None
        try:
            process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **({"env": env} if env is not None else {}))
            stdout, stderr = process.communicate()
        except BaseException as error:
            command["error"] = type(error).__name__ + ": " + str(error)
            if process is not None:
                # Drain without killing the child; the interrupted attempt remains unsettled.
                try:
                    process.communicate()
                except BaseException as wait_error:
                    command["wait_error"] = type(wait_error).__name__ + ": " + str(wait_error)
            receipt.write_text(json.dumps(self.commands, indent=2) + "\n")
            raise
        self.unsettled = False
        command.update(exit=process.returncode, unsettled=False)
        receipt.write_text(json.dumps(self.commands, indent=2) + "\n")
        for secret in redact:
            stdout, stderr = stdout.replace(secret, b"[redacted Tape key]"), stderr.replace(secret, b"[redacted Tape key]")
        result = subprocess.CompletedProcess(argv, process.returncode, stdout, stderr)
        (self.output / (name + ".out")).write_bytes(result.stdout)
        (self.output / (name + ".err")).write_bytes(result.stderr)
        return result

    def run(self, name, argv, expected=0):
        result = self.capture(name, argv)
        require(result.returncode == expected and not result.stderr,
                "Public reader command failed or emitted a diagnostic: " + name)
        return result

    def stream_fill(self, number):
        """Pipe one Tape file through tar member hashing; dd always reads to the filemark."""
        require(not self.unsettled, "Command completion is unknown; retain the stopped owner")
        copy = self.output / ("file-" + str(number))
        copy.mkdir(mode=0o700)
        helper = copy / "hash-member.py"
        helper.write_text(MEMBER_HASH)
        read_argv = ["dd", "if=" + self.device, "bs=" + str(self.record_bytes), "status=none"]
        hash_argv = ["tar", "-x", "-f", "-", "--to-command=python3 -I " + shlex.quote(str(helper))]
        commands = [{"name": f"read-{number}", "argv": read_argv, "unsettled": True},
                    {"name": f"hash-{number}", "argv": hash_argv, "unsettled": True}]
        self.commands.extend(commands)
        receipt = self.output / "commands.json"
        receipt.write_text(json.dumps(self.commands, indent=2) + "\n")
        self.unsettled = True
        digest, total, closed = hashlib.sha256(), 0, False
        (self.output / f"read-{number}.out").write_bytes(b"")
        with (self.output / f"read-{number}.err").open("wb") as read_error, \
                (self.output / f"hash-{number}.out").open("wb") as hashes, \
                (self.output / f"hash-{number}.err").open("wb") as hash_error:
            reader = subprocess.Popen(read_argv, stdout=subprocess.PIPE, stderr=read_error)
            extractor = subprocess.Popen(hash_argv, stdin=subprocess.PIPE, stdout=hashes, stderr=hash_error, cwd=copy)
            for chunk in iter(lambda: reader.stdout.read(1 << 20), b""):
                total += len(chunk)
                digest.update(chunk)
                if not closed:
                    try:
                        extractor.stdin.write(chunk)
                    except BrokenPipeError:
                        closed = True
            try:
                extractor.stdin.close()
            except BrokenPipeError:
                closed = True
            read_exit, hash_exit = reader.wait(), extractor.wait()
        self.unsettled = False
        commands[0].update(exit=read_exit, unsettled=False, bytes=total)
        commands[1].update(exit=hash_exit, unsettled=False, input_closed_early=closed)
        receipt.write_text(json.dumps(self.commands, indent=2) + "\n")
        require(read_exit == 0 and not (self.output / f"read-{number}.err").read_bytes(),
                f"Public reader command failed or emitted a diagnostic: read-{number}")
        records = [json.loads(line) for line in (self.output / f"hash-{number}.out").read_text().splitlines() if line.strip()]
        # An EW-stopped archive has no tar terminator and can end inside an unpublished member.
        return {"bytes": total, "sha256": digest.hexdigest(), "tar_exit": hash_exit, "input_closed_early": closed,
                "tar_stderr": (self.output / f"hash-{number}.err").read_text(errors="replace"),
                **check_fill(records, self.fill_members)}

    def copy_file(self, name, archive):
        self.run(name, ["dd", "if=" + self.device, "of=" + str(archive), "bs=" + str(self.record_bytes),
                        "conv=excl", "status=none"])

    def status(self, name):
        result = self.run(name, ["mt", "-f", self.device, "status"])
        text = result.stdout.decode()
        parse_status(text)
        return text

    def configure_encryption(self):
        if self.encryption is None:
            return
        options = self.encryption
        key_file = Path(options["key_file"])
        info = key_file.lstat()
        require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600,
                "Public recovery requires an owner-only regular temporary key file")
        key = key_file.read_bytes()
        require(re.fullmatch(rb"[0-9a-fA-F]{64}", key), "Temporary Tape key must contain 256-bit hexadecimal data")
        environment = {**os.environ, "DEVICE": self.device, "KEY_FILE": str(key_file),
                       "TAPE_BARCODE": options["barcode"], "TAPE_NAME": options["tape_name"],
                       "TAPE_DIR": str(self.output.resolve())}
        command = ["/bin/sh", "-c", 'cd "$1" && shift && exec "$@"', "yatm-public-encryption",
                   options["script_directory"], options["encrypt_script"]]
        configured = self.capture("configure-encryption", command, env=environment, redact=(key, b"v1:" + key))
        require(configured.returncode == 0, "Configured encryption adapter failed before public recovery")

    def prepare(self):
        require(self.fixture.is_file() and not self.fixture.is_symlink(), "Fixture oracle must be a regular file")
        self.output.mkdir(mode=0o700, parents=False, exist_ok=False)
        self.device, aliases = device_aliases(self.device)
        versions = {}
        for name, argv in (("mt", ["mt", "--version"]), ("dd", ["dd", "--version"]),
                           ("tar", ["tar", "--version"]), ("kernel", ["uname", "-a"])):
            observed = self.run("version-" + name, argv)
            versions[name] = observed.stdout.decode()
        require(re.match(r"mt-st v\. 1\.8(?:\n|$)", versions["mt"])
                and versions["dd"].startswith("dd (coreutils) ")
                and versions["tar"].startswith("tar (GNU tar) ") and versions["kernel"].startswith("Linux "),
                "Public boundary oracle supports the observed Linux st / mt-st 1.8 syntax and GNU tools only")
        users = self.run("device-users", ["fuser", *aliases], expected=1)
        require(not users.stdout.strip() and not users.stderr.strip(), "Device ownership is uncertain")
        mounts = json.loads(self.run("mounts", ["findmnt", "--list", "--json", "--output", "SOURCE"]).stdout)
        require("filesystems" in mounts and not any(row.get("source") in aliases for row in mounts["filesystems"]),
                "A Tape alias is mounted or mount state is unknown")
        self.configure_encryption()
        original = parse_status(self.status("original-status"))["block_bytes"]
        return versions, aliases, original

    def read(self):
        versions, aliases, original = self.prepare()
        selected = 0 if self.mode == "variable" else self.record_bytes
        changed = original != selected
        result = {"scope": "Normal three-file baseline and optional EW-stopped fill file; no reload, seek, empty physical file or fault qualification.",
                  "versions": versions, "aliases": aliases, "original_block_bytes": original, "files": []}
        try:
            if changed:
                self.run("set-record-policy", ["mt", "-f", self.device, "setblk", str(selected)])
            require(parse_status(self.status("selected-status"))["block_bytes"] == selected,
                    "Driver did not retain selected record policy")
            for number in range(3):
                self.run(f"rewind-{number}", ["mt", "-f", self.device, "rewind"])
                if number:
                    self.run(f"space-{number}", ["mt", "-f", self.device, "fsf", str(number)])
                copy = self.output / ("file-" + str(number))
                copy.mkdir(mode=0o700)
                archive = copy / "archive.tar"
                self.copy_file(f"read-{number}", archive)
                status = self.status(f"post-close-{number}")
                require(parse_status(status)["block_bytes"] == selected, "Driver record policy changed during public read")
                boundary = check_boundary(status, number, archive.stat().st_size)
                self.run(f"list-{number}", ["tar", "--warning=missing-zero-blocks", "-tf", str(archive)])
                extracted = copy / "extracted"
                extracted.mkdir(mode=0o700)
                self.run(f"extract-{number}", ["tar", "--warning=missing-zero-blocks", "--same-permissions",
                                              "--no-same-owner", "--xattrs", "-xf", str(archive), "-C", str(extracted)])
                if number == 0:
                    paths = {str(path.relative_to(extracted)) for path in extracted.rglob("*")
                             if not path.is_dir() or path.is_symlink()}
                    require(paths == {"YATM/identity.json", "YATM/README.txt"}
                            and all((extracted / path).is_file() and not (extracted / path).is_symlink() for path in paths),
                            "Label file contents differ")
                    result["identity"] = json.loads((extracted / "YATM/identity.json").read_text())
                else:
                    check_extracted(self.fixture, extracted, ("format", "append")[number - 1])
                result["files"].append({"number": number, "boundary": boundary, "bytes": archive.stat().st_size,
                                        "sha256": file_sha256(archive)})
            if self.fill_members is not None:
                # The fill file follows the third FILEMARK without another seek.
                fill = self.stream_fill(3)
                status = self.status("post-close-3")
                require(parse_status(status)["block_bytes"] == selected, "Driver record policy changed during fill read")
                result["fill"] = fill
                result["files"].append({"number": 3, "boundary": check_boundary(status, 3, fill["bytes"]),
                                        "bytes": fill["bytes"], "sha256": fill["sha256"]})
            # This read follows the last independently observed FILEMARK, without another seek.
            tail = self.output / "tail.bin"
            self.copy_file("read-tail", tail)
            status = self.status("post-tail")
            require(parse_status(status)["block_bytes"] == selected, "Driver record policy changed during tail read")
            result["end"] = check_boundary(status, len(result["files"]), tail.stat().st_size, end=True)
        finally:
            if not self.unsettled:
                if changed:
                    self.run("restore-record-policy", ["mt", "-f", self.device, "setblk", str(original)])
                require(parse_status(self.status("restored-status"))["block_bytes"] == original,
                        "Original driver record setting was not restored; retain the stopped owner")
                result["record_setting_restored"] = True
        (self.output / "review.json").write_text(json.dumps(result, indent=2) + "\n")
        return result


def fixture_main(argv):
    parser = argparse.ArgumentParser(description="Generate or check the nine-file fixture oracle; no Tape access.")
    parser.add_argument("action", choices=("generate", "check"))
    parser.add_argument("root", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--record-bytes", type=int, help="Required for generate; explicit selected record size")
    parser.add_argument("--mapping", type=Path, help="fixture-relative -> output-relative JSON map")
    parser.add_argument("--exact", action="store_true")
    args = parser.parse_args(argv)
    if args.action == "generate":
        require(args.record_bytes is not None, "generate requires --record-bytes")
        print(json.dumps({"generated": len(generate(args.root, args.manifest, args.record_bytes))}))
        return
    mapping = json.loads(args.mapping.read_text()) if args.mapping else None
    print(json.dumps(check(args.manifest, args.root, mapping, args.exact)))


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if argv[:1] == ["fixture"]:
        return fixture_main(argv[1:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--device", required=True)
    parser.add_argument("--record-mode", choices=("fixed", "variable"), required=True)
    parser.add_argument("--record-bytes", type=int, required=True)
    parser.add_argument("--fixture", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--encrypt-script", type=Path, help="Operator-selected compatible encryption adapter")
    parser.add_argument("--key-file", type=Path, help="Caller-owned temporary 0600 key file from the saved Media profile")
    parser.add_argument("--barcode", help="Saved Media barcode supplied to the encryption adapter")
    parser.add_argument("--tape-name", help="Saved Media name supplied to the encryption adapter")
    parser.add_argument("--script-directory", type=Path, help="Original service working directory for local adapter helpers")
    parser.add_argument("--fill-members", type=Path,
                        help="Published fill members (Tape file 3) as a JSON map of member path to size/sha256")
    args = parser.parse_args(argv)
    encryption = (args.encrypt_script, args.key_file, args.barcode, args.tape_name, args.script_directory)
    require(not any(value is not None for value in encryption) or all(value is not None for value in encryption),
            "Encrypted recovery requires adapter, temporary key file, barcode, Tape name and service directory together")
    def interrupted(signum, frame):
        raise RuntimeError("Public reader interrupted; retain the stopped owner and partial evidence")
    signal.signal(signal.SIGTERM, interrupted)
    reader = PublicReader(args.device, args.record_mode, args.record_bytes, args.fixture, args.out)
    if args.fill_members is not None:
        reader.fill_members = check_fill_members(json.loads(args.fill_members.read_text()))
    if args.encrypt_script is not None:
        reader.encryption = {"encrypt_script": str(args.encrypt_script.resolve()), "key_file": str(args.key_file.resolve()),
                             "barcode": args.barcode, "tape_name": args.tape_name,
                             "script_directory": str(args.script_directory.resolve())}
    print(json.dumps(reader.read()))


if __name__ == "__main__":
    main()
