#!/usr/bin/env python3
"""Build GitHub Wiki pages from the repository's canonical documentation."""

from __future__ import annotations

import argparse
import os
from pathlib import Path, PurePosixPath
import re
import sys
from urllib.parse import quote

PAGE_MAP = {
    "docs/wiki/Home.md": "Home.md",
    "docs/wiki/_Sidebar.md": "_Sidebar.md",
    "docs/wiki/_Footer.md": "_Footer.md",
    "docs/zh/getting-started.md": "Getting-Started.md",
    "docs/zh/architecture.md": "Architecture.md",
    "docs/zh/media-sources.md": "Media-and-Streams.md",
    "docs/zh/recording-plans.md": "Recording-and-Playback.md",
    "docs/zh/recording-flow.md": "Recording-Internals.md",
    "docs/zh/configuration.md": "Configuration.md",
    "docs/zh/api-reference.md": "API.md",
    "docs/zh/security.md": "Security.md",
    "docs/zh/deployment.md": "Deployment-and-Operations.md",
    "docs/zh/troubleshooting.md": "Troubleshooting.md",
    "docs/zh/development.md": "Development.md",
    "docs/zh/database-migration.md": "Database-Migration.md",
    "docs/zh/camera-guide.md": "Camera-Guide.md",
    "docs/zh/onvif-guide.md": "ONVIF.md",
    "docs/zh/gb28181-guide.md": "GB28181.md",
    "docs/zh/voip.md": "VoIP.md",
    "docs/zh/xiaomi-setup.md": "Xiaomi.md",
    "docs/zh/iptv.md": "IPTV.md",
    "docs/zh/emby.md": "Emby.md",
    "docs/zh/mediamtx-guide.md": "MediaMTX.md",
    "docs/zh/home-assistant.md": "Home-Assistant.md",
    "docs/zh/mqtt-integration.md": "MQTT.md",
    "docs/zh/events.md": "Events.md",
    "docs/zh/webdav-integration.md": "WebDAV.md",
    "docs/zh/ftp-integration.md": "FTP.md",
    "docs/zh/dlna.md": "DLNA.md",
    "docs/zh/transcoding-deployment.md": "Transcoding.md",
}

LINK_RE = re.compile(
    r"(?P<open>!?\[[^\]]*\]\()(?P<target>[^)\s]+)(?P<title>\s+[^)]*)?\)"
)
FENCE_RE = re.compile(r"^\s*(```|~~~)")


def repository_path(root: Path, path: Path) -> str:
    return PurePosixPath(path.relative_to(root).as_posix()).as_posix()


def rewrite_target(
    target: str,
    *,
    source: Path,
    image: bool,
    root: Path,
    repository: str,
    ref: str,
    page_by_source: dict[str, str],
) -> str:
    if not target or target.startswith(("#", "/", "//")):
        return target
    if re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", target):
        return target

    path_part, separator, fragment = target.partition("#")
    if not path_part:
        return target
    resolved = (source.parent / path_part).resolve()
    try:
        relative = repository_path(root, resolved)
    except ValueError:
        raise ValueError(f"{repository_path(root, source)} links outside repository: {target}")

    mapped = page_by_source.get(relative)
    if mapped:
        target_page = mapped.removesuffix(".md")
        return target_page + (separator + fragment if separator else "")

    encoded_path = quote(relative, safe="/")
    suffix = separator + fragment if separator else ""
    if image:
        return f"https://raw.githubusercontent.com/{repository}/{quote(ref, safe='')}/{encoded_path}{suffix}"
    kind = "tree" if path_part.endswith("/") or resolved.is_dir() else "blob"
    return f"https://github.com/{repository}/{kind}/{quote(ref, safe='')}/{encoded_path}{suffix}"


def rewrite_markdown(
    text: str,
    *,
    source: Path,
    root: Path,
    repository: str,
    ref: str,
    page_by_source: dict[str, str],
) -> str:
    output: list[str] = []
    fenced = False
    for line in text.splitlines(keepends=True):
        if FENCE_RE.match(line):
            fenced = not fenced
            output.append(line)
            continue
        if fenced:
            output.append(line)
            continue

        def replace(match: re.Match[str]) -> str:
            original_target = match.group("target")
            target = rewrite_target(
                original_target,
                source=source,
                image=match.group("open").startswith("!"),
                root=root,
                repository=repository,
                ref=ref,
                page_by_source=page_by_source,
            )
            return match.group("open") + target + (match.group("title") or "") + ")"

        output.append(LINK_RE.sub(replace, line))
    return "".join(output)


def build(root: Path, output: Path, repository: str, ref: str) -> list[Path]:
    root = root.resolve()
    output = output.resolve()
    page_by_source = dict(PAGE_MAP)
    written: list[Path] = []

    for source_name, target_name in PAGE_MAP.items():
        source = root / source_name
        if not source.is_file():
            raise FileNotFoundError(f"missing Wiki source: {source_name}")
        target = output / target_name
        target.parent.mkdir(parents=True, exist_ok=True)
        text = source.read_text(encoding="utf-8")
        if not source_name.startswith("docs/wiki/"):
            text = rewrite_markdown(
                text,
                source=source,
                root=root,
                repository=repository,
                ref=ref,
                page_by_source=page_by_source,
            )
            source_url = f"https://github.com/{repository}/blob/{quote(ref, safe='')}/{source_name}"
            text = text.rstrip() + f"\n\n---\n> 本页由主仓库 [`{source_name}`]({source_url}) 同步；请在源文件中修改。\n"
        target.write_text(text, encoding="utf-8")
        written.append(target)
    return written


def main() -> int:
    default_root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=default_root, help="repository root")
    parser.add_argument("--output", type=Path, required=True, help="generated Wiki directory")
    parser.add_argument(
        "--repository",
        default=os.environ.get("GITHUB_REPOSITORY", "lalmax-pro/lalmax-nvr"),
        help="GitHub owner/repository used for source and image links",
    )
    parser.add_argument("--ref", default="main", help="Git ref used for repository links")
    args = parser.parse_args()

    try:
        written = build(args.root, args.output, args.repository, args.ref)
    except (OSError, ValueError) as exc:
        print(f"wiki build failed: {exc}", file=sys.stderr)
        return 1
    print(f"generated {len(written)} Wiki pages in {args.output.resolve()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
