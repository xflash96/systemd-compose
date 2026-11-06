#!/usr/bin/env python3
# scripts/wheel.py OUTDIR TARBALL... — a wheel for PyPI from each release
# tarball, holding the tarball's own binary, so pip, pipx and uv install the
# bytes the GitHub release has. The binary goes in the wheel's scripts (pip
# puts it in bin/ as is, with no Python in front of it), the man page in its
# data (share/man/man1), and LICENSE and third_party/ in its licences. The
# binary is static, so one wheel per arch carries both the glibc and the
# musl tags. File times and modes are fixed, so a wheel is the same
# wherever it is built. The release workflow runs it on the release's
# tarballs (.github/workflows/ci.yml, pypi).
#
#   scripts/wheel.py wheels/ dist/systemd-compose_0.1.2_linux_*.tar.gz
import base64
import hashlib
import os
import re
import sys
import tarfile
import zipfile

NAME = "systemd-compose"
DIST = NAME.replace("-", "_")
REPO = "https://github.com/xflash96/systemd-compose"
ARCH = {"amd64": "x86_64", "arm64": "aarch64"}
SUMMARY = "Run a project's services as systemd user units, with docker compose's verbs."


def pep440(version):
    """0.1.2 stays; a pre-release 0.1.2-rc.1 becomes 0.1.2rc1."""
    m = re.fullmatch(r"(\d+\.\d+\.\d+)(?:-(alpha|beta|rc)\.(\d+))?", version)
    if not m:
        sys.exit(f"wheel.py: {version} is no X.Y.Z or X.Y.Z-(alpha|beta|rc).N")
    pre = {"alpha": "a", "beta": "b", "rc": "rc"}.get(m[2], "")
    return m[1] + (pre + m[3] if pre else "")


def absolute_links(readme, tag):
    """README's relative links point into the repository at the tag, since
    PyPI's page has no files beside it."""
    return re.sub(
        r"\]\((?!https?://|#)([^)\s]+)\)",
        lambda m: f"]({REPO}/blob/{tag}/{m[1]})",
        readme,
    )


def wheel(outdir, tarball):
    m = re.fullmatch(NAME + r"_(.+)_linux_(amd64|arm64)\.tar\.gz", os.path.basename(tarball))
    if not m:
        sys.exit(f"wheel.py: {tarball} is no {NAME}_VERSION_linux_ARCH.tar.gz")
    version, arch = m[1], ARCH[m[2]]
    v, tag = pep440(version), "v" + version
    top = os.path.basename(tarball)[: -len(".tar.gz")] + "/"
    with tarfile.open(tarball) as t:
        files = {
            i.name[len(top):]: t.extractfile(i).read()
            for i in t.getmembers()
            if i.isfile() and i.name.startswith(top)
        }
    licences = ["LICENSE"] + sorted(f for f in files if f.startswith("third_party/"))
    data, info = f"{DIST}-{v}.data", f"{DIST}-{v}.dist-info"

    metadata = "".join(
        [
            "Metadata-Version: 2.4\n",
            f"Name: {NAME}\n",
            f"Version: {v}\n",
            f"Summary: {SUMMARY}\n",
            "License-Expression: Apache-2.0\n",
            *(f"License-File: {f}\n" for f in licences),
            f"Project-URL: Homepage, {REPO}\n",
            f"Project-URL: Changelog, {REPO}/blob/{tag}/CHANGELOG.md\n",
            f"Project-URL: Issues, {REPO}/issues\n",
            "Classifier: Environment :: Console\n",
            "Classifier: Operating System :: POSIX :: Linux\n",
            "Classifier: Topic :: System :: Systems Administration\n",
            "Description-Content-Type: text/markdown\n",
            "\n",
            absolute_links(files["README.md"].decode(), tag),
        ]
    )
    tags = [f"manylinux_2_17_{arch}", f"manylinux2014_{arch}", f"musllinux_1_1_{arch}"]
    wheelinfo = "Wheel-Version: 1.0\nGenerator: systemd-compose scripts/wheel.py\nRoot-Is-Purelib: false\n" + "".join(
        f"Tag: py3-none-{t}\n" for t in tags
    )
    entries = [
        (f"{data}/scripts/{NAME}", files[NAME], 0o755),
        (f"{data}/data/share/man/man1/{NAME}.1", files[f"docs/{NAME}.1"], 0o644),
        *((f"{info}/licenses/{f}", files[f], 0o644) for f in licences),
        (f"{info}/METADATA", metadata.encode(), 0o644),
        (f"{info}/WHEEL", wheelinfo.encode(), 0o644),
    ]
    record = "".join(
        f"{path},sha256={base64.urlsafe_b64encode(hashlib.sha256(b).digest()).rstrip(b'=').decode()},{len(b)}\n"
        for path, b, _ in entries
    ) + f"{info}/RECORD,,\n"
    entries.append((f"{info}/RECORD", record.encode(), 0o644))

    out = os.path.join(outdir, f"{DIST}-{v}-py3-none-{'.'.join(tags)}.whl")
    with zipfile.ZipFile(out, "w") as z:
        for path, b, mode in entries:
            zi = zipfile.ZipInfo(path, date_time=(1980, 1, 1, 0, 0, 0))
            zi.external_attr = (0o100000 | mode) << 16
            zi.create_system = 3
            z.writestr(zi, b, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    print(out)


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit("usage: wheel.py OUTDIR TARBALL...")
    os.makedirs(sys.argv[1], exist_ok=True)
    for tb in sys.argv[2:]:
        wheel(sys.argv[1], tb)
