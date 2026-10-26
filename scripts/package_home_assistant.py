"""Build an installable HA ZIP, optionally export a standalone source repository.

Only ships public integration sources; no runtime config, tests or caches.
"""

import argparse
import json
import shutil
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "custom_components" / "lalmax_nvr"
BRAND_FILES = {
    f"brand/{name}.png"
    for name in (
        "icon",
        "icon@2x",
        "dark_icon",
        "dark_icon@2x",
        "logo",
        "logo@2x",
        "dark_logo",
        "dark_logo@2x",
    )
}


def package(output: Path, export: Path | None = None) -> None:
    manifest = json.loads((SOURCE / "manifest.json").read_text())
    if manifest["domain"] != "lalmax_nvr" or not manifest["version"]:
        raise ValueError("Invalid integration manifest")
    files = [
        path
        for path in sorted(SOURCE.rglob("*"))
        if path.is_file()
        and (
            path.suffix in {".py", ".json", ".yaml", ".yml", ".md"}
            or path.name == "LICENSE"
            or path.relative_to(SOURCE).as_posix() in BRAND_FILES
        )
        and "__pycache__" not in path.parts
    ]
    if any(path.is_symlink() for path in files):
        raise ValueError("Integration sources must not be symlinks")
    output.parent.mkdir(parents=True, exist_ok=True)
    # Stable metadata keeps archives reproducible across machines and runs.
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for path in files:
            info = zipfile.ZipInfo(
                path.relative_to(SOURCE).as_posix(), (2020, 1, 1, 0, 0, 0)
            )
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100644 << 16
            archive.writestr(info, path.read_bytes())
    if export is not None:
        export.mkdir(parents=True, exist_ok=False)
        for path in files:
            target = (
                export / "custom_components" / "lalmax_nvr" / path.relative_to(SOURCE)
            )
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        for name in ("LICENSE", "hacs.json", "info.md"):
            shutil.copyfile(ROOT / name, export / name)
        shutil.copyfile(ROOT / "docs/en/home-assistant.md", export / "README.md")
        shutil.copyfile(ROOT / "docs/zh/home-assistant.md", export / "README.zh.md")
    print(
        f"Packaged lalmax_nvr {manifest['version']}: {len(files)} source files → {output}"
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=ROOT / "dist/lalmax_nvr.zip")
    parser.add_argument(
        "--export-dir", type=Path, help="New directory for HA-only sources"
    )
    args = parser.parse_args()
    package(args.output, args.export_dir)
