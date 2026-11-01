"""The installable archive must carry local branding without arbitrary images."""

import json
import zipfile

from scripts import package_home_assistant as packager


def test_package_brand_allowlist_and_reproducibility(tmp_path, monkeypatch):
    source = tmp_path / "integration"
    (source / "brand").mkdir(parents=True)
    (source / "manifest.json").write_text(
        json.dumps({"domain": "lalmax_nvr", "version": "0.6.2"})
    )
    (source / "__init__.py").write_text('"""Integration."""\n')
    icon = b"\x89PNG\r\n\x1a\npublic-brand"
    (source / "brand/icon.png").write_bytes(icon)
    (source / "brand/private-preview.png").write_bytes(b"private")
    (source / "camera-preview.png").write_bytes(b"private")
    monkeypatch.setattr(packager, "SOURCE", source)
    first, second = tmp_path / "first.zip", tmp_path / "second.zip"
    packager.package(first)
    packager.package(second)
    assert first.read_bytes() == second.read_bytes()
    with zipfile.ZipFile(first) as archive:
        assert set(archive.namelist()) == {
            "__init__.py",
            "manifest.json",
            "brand/icon.png",
        }
        assert archive.read("brand/icon.png") == icon
