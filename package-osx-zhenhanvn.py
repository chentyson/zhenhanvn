#!/usr/bin/env python3
"""Package zhenhanvn as a macOS .app bundle zip.

Cross-platform replacement for package-osx-zhenhanvn.sh. It can run on Windows
(the macOS binaries are produced with `dotnet publish -r osx-*`), macOS or Linux.

Why a Python script: zip archives created by Windows tools store files with
MS-DOS attributes only. The resulting .app then has no Unix execute permission,
so the app and its cores cannot be launched on macOS. This script writes
create_system=3 (Unix) and the correct 0755/0644 mode for every entry.

Usage:
  python package-osx-zhenhanvn.py --arch macos-arm64 \
      --publish <publish-dir> --version 7.24.9.8 \
      [--output zhenhanvn-macos-arm64.zip] [--cache-dir <dir>]
"""

import argparse
import os
import shutil
import struct
import sys
import tempfile
import urllib.request
import zipfile

CORE_URL = "https://github.com/2dust/v2rayN-core-bin/raw/refs/heads/master/v2rayN-{arch}.zip"
APP_NAME = "zhenhanvn"
BUNDLE_ID = "com.boosoo.zhenhanvn"
# Highest minimum implied by the bundled natives: libe_sqlite3.dylib needs 13.5.
MIN_MACOS = "13.6"

# Files that must be executable inside the bundle.
def _is_executable(rel_path: str) -> bool:
    rel = rel_path.replace("\\", "/")
    if rel == "Contents/MacOS/" + APP_NAME:
        return True
    if rel.startswith("Contents/MacOS/bin/"):
        base = rel.rsplit("/", 1)[-1]
        # Core binaries (xray, mihomo, sing-box) have no extension.
        return "." not in base
    return False


def _mode(rel_path: str) -> int:
    return 0o100755 if _is_executable(rel_path) else 0o100644


def _download(url: str, dest: str) -> None:
    print("Downloading %s" % url)
    tmp = dest + ".part"
    req = urllib.request.Request(url, headers={"User-Agent": "zhenhanvn-packager"})
    with urllib.request.urlopen(req) as resp, open(tmp, "wb") as out:
        shutil.copyfileobj(resp, out)
    os.replace(tmp, dest)


def _fetch_core(arch: str, cache_dir: str) -> str:
    name = "v2rayN-%s.zip" % arch
    dest = os.path.join(cache_dir, name)
    if os.path.isfile(dest) and os.path.getsize(dest) > 0:
        print("Using cached core archive: %s" % dest)
        return dest
    _download(CORE_URL.format(arch=arch), dest)
    return dest


def _macho_cputype(path: str):
    """Return the Mach-O cputype of a thin 64-bit binary, or None."""
    with open(path, "rb") as f:
        data = f.read(32)
    if len(data) < 32:
        return None
    magic = struct.unpack("<I", data[0:4])[0]
    if magic == 0xFEEDFACF:
        return struct.unpack("<I", data[4:8])[0]
    if magic == 0xFEEDFACE:
        return struct.unpack("<I", data[4:8])[0]
    # Fat binary (0xCAFEBABE big endian): take the first slice.
    if struct.unpack(">I", data[0:4])[0] == 0xCAFEBABE:
        offset = struct.unpack(">I", data[8 + 8:8 + 12])[0]
        with open(path, "rb") as f:
            f.seek(offset)
            slice_header = f.read(8)
        if len(slice_header) >= 8:
            return struct.unpack("<I", slice_header[4:8])[0]
    return None


def _verify_arch(binary: str, arch: str) -> None:
    expected = 0x0100000C if arch == "macos-arm64" else 0x01000007
    expected_name = "arm64" if arch == "macos-arm64" else "x86_64"
    cputype = _macho_cputype(binary)
    if cputype is None:
        print("WARNING: cannot determine Mach-O architecture of %s" % binary)
        return
    if cputype != expected:
        raise SystemExit(
            "ERROR: %s is not a %s binary (cputype=0x%08X)" % (binary, expected_name, cputype)
        )
    print("Verified Mach-O architecture: %s" % expected_name)


def _short_version(version: str) -> str:
    parts = version.split(".")
    return ".".join(parts[:3])


def _info_plist(version: str) -> str:
    return """<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>en</string>
  <key>CFBundleLocalizations</key>
  <array>
    <string>zh-Hans</string>
    <string>zh-Hant</string>
    <string>en</string>
    <string>fa</string>
    <string>fr</string>
    <string>ru</string>
    <string>hu</string>
  </array>
  <key>CFBundleDisplayName</key>
  <string>%(app)s</string>
  <key>CFBundleExecutable</key>
  <string>%(app)s</string>
  <key>CFBundleIconFile</key>
  <string>AppIcon</string>
  <key>CFBundleIconName</key>
  <string>AppIcon</string>
  <key>CFBundleIdentifier</key>
  <string>%(bundle_id)s</string>
  <key>CFBundleName</key>
  <string>%(app)s</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>%(short_version)s</string>
  <key>CFBundleVersion</key>
  <string>%(version)s</string>
  <key>CSResourcesFileMapped</key>
  <true/>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>LSMinimumSystemVersion</key>
  <string>%(min_macos)s</string>
</dict>
</plist>
""" % {
        "app": APP_NAME,
        "bundle_id": BUNDLE_ID,
        "short_version": _short_version(version),
        "version": version,
        "min_macos": MIN_MACOS,
    }


def build_app(publish_dir: str, arch: str, version: str, core_zip: str, work_dir: str) -> str:
    app_dir = os.path.join(work_dir, "%s.app" % APP_NAME)
    macos_dir = os.path.join(app_dir, "Contents", "MacOS")
    resources_dir = os.path.join(app_dir, "Contents", "Resources")
    os.makedirs(macos_dir)
    os.makedirs(resources_dir)

    # 1) Application binaries (dotnet publish output).
    copied = 0
    for name in sorted(os.listdir(publish_dir)):
        src = os.path.join(publish_dir, name)
        if not os.path.isfile(src):
            continue
        shutil.copy2(src, os.path.join(macos_dir, name))
        copied += 1
    print("Copied %d application files" % copied)

    executable = os.path.join(macos_dir, APP_NAME)
    if not os.path.isfile(executable):
        raise SystemExit("ERROR: %s not found in %s" % (APP_NAME, publish_dir))
    _verify_arch(executable, arch)

    # 2) Proxy cores shipped with the upstream release (bin/ only).
    core_root = "v2rayN-%s" % arch
    bin_copied = 0
    with zipfile.ZipFile(core_zip) as zf:
        for info in zf.infolist():
            if info.is_dir():
                continue
            rel = info.filename
            prefix = core_root + "/"
            if not rel.startswith(prefix):
                continue
            rel = rel[len(prefix):]
            if not rel.startswith("bin/"):
                continue
            if rel.startswith(("bin/mihomo/", "bin/sing_box/", "bin/srss/")):
                continue
            dest = os.path.join(macos_dir, rel.replace("/", os.sep))
            os.makedirs(os.path.dirname(dest), exist_ok=True)
            with zf.open(info) as src, open(dest, "wb") as out:
                shutil.copyfileobj(src, out)
            bin_copied += 1
    print("Copied %d core files" % bin_copied)

    # 3) Info.plist, icon and the portable-config marker.
    with open(os.path.join(app_dir, "Contents", "Info.plist"), "w", encoding="utf-8", newline="\n") as f:
        f.write(_info_plist(version))

    icon = os.path.join(publish_dir, "v2rayN.icns")
    if os.path.isfile(icon):
        shutil.copy2(icon, os.path.join(resources_dir, "AppIcon.icns"))
    else:
        print("WARNING: v2rayN.icns not found in publish output")

    with open(os.path.join(macos_dir, "NotStoreConfigHere.txt"), "w", encoding="utf-8", newline="\n") as f:
        f.write("When this file exists, app will not store configs under this folder\n")

    return app_dir


def _readme_text(version: str) -> str:
    return """zhenhanvn for macOS %s
=========================

Installation
------------
1. Unzip this archive.
2. Drag "zhenhanvn.app" into /Applications (or any folder you like).
3. The first launch must bypass Gatekeeper (the app is ad-hoc signed and not notarized):
   - Right-click (Control-click) zhenhanvn.app -> "Open" -> "Open", or
   - System Settings -> Privacy & Security -> "Open Anyway".
   - Terminal alternative: xattr -dr com.apple.quarantine /Applications/zhenhanvn.app

Requirements
------------
macOS 13.6 or later (Apple Silicon or Intel as indicated by the file name).

Config and logs are stored in ~/.local/share/v2rayN (not inside the app bundle).

Updating
--------
When a new version is downloaded by the app, choose "update now" and the app
replaces its own bundle, re-signs it ad-hoc and restarts automatically.
""" % version


def write_zip(app_dir: str, output: str, readme_path: str = "") -> None:
    """Write a zip archive with Unix permissions so the .app is runnable on macOS."""
    entries = []
    for root, dirs, files in os.walk(app_dir):
        dirs.sort()
        for name in sorted(files):
            full = os.path.join(root, name)
            rel = os.path.relpath(full, app_dir).replace(os.sep, "/")
            entries.append((os.path.join(os.path.dirname(rel), name).replace(os.sep, "/"), full))
    entries.sort(key=lambda item: item[0])

    if os.path.exists(output):
        os.remove(output)

    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        if readme_path and os.path.isfile(readme_path):
            info = zipfile.ZipInfo("README.txt", date_time=(2026, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = 0o100644 << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(readme_path, "rb") as src:
                zf.writestr(info, src.read())

        for rel_path, full in entries:
            zip_name = "%s.app/%s" % (APP_NAME, rel_path)
            info = zipfile.ZipInfo(zip_name, date_time=(2026, 1, 1, 0, 0, 0))
            info.create_system = 3  # Unix, so external_attr carries the mode
            info.external_attr = _mode(rel_path) << 16
            info.compress_type = zipfile.ZIP_DEFLATED
            with open(full, "rb") as src:
                zf.writestr(info, src.read())

    size_mb = os.path.getsize(output) / (1024 * 1024)
    print("Created %s (%.1f MB, %d entries)" % (output, size_mb, len(entries) + (1 if readme_path else 0)))


def main() -> int:
    parser = argparse.ArgumentParser(description="Package zhenhanvn as a macOS .app zip")
    parser.add_argument("--arch", required=True, choices=["macos-64", "macos-arm64"])
    parser.add_argument("--publish", required=True, help="dotnet publish output directory")
    parser.add_argument("--version", required=True, help="four-part version, e.g. 7.24.9.8")
    parser.add_argument("--output", default="", help="output zip (default: zhenhanvn-<arch>.zip)")
    parser.add_argument("--cache-dir", default="", help="where to cache the upstream core zip")
    args = parser.parse_args()

    publish_dir = os.path.abspath(args.publish)
    if not os.path.isdir(publish_dir):
        raise SystemExit("ERROR: publish directory not found: %s" % publish_dir)

    output = os.path.abspath(args.output or ("zhenhanvn-%s.zip" % args.arch))
    cache_dir = os.path.abspath(args.cache_dir or os.path.join(tempfile.gettempdir(), "zhenhanvn-core-cache"))
    os.makedirs(cache_dir, exist_ok=True)

    core_zip = _fetch_core(args.arch, cache_dir)

    work_dir = tempfile.mkdtemp(prefix="zhenhanvn-pkg-")
    try:
        app_dir = build_app(publish_dir, args.arch, args.version, core_zip, work_dir)
        readme_path = os.path.join(work_dir, "README.txt")
        with open(readme_path, "w", encoding="utf-8", newline="\n") as f:
            f.write(_readme_text(args.version))
        write_zip(app_dir, output, readme_path)
    finally:
        shutil.rmtree(work_dir, ignore_errors=True)

    print("OK: %s" % output)
    return 0


if __name__ == "__main__":
    sys.exit(main())
