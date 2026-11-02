#!/usr/bin/env python3
"""更新 FnDepot 外部应用源的 fnpack.json。

用法示例：
    python3 scripts/update_fnpack.py --repo . --dist dist --version 1.2.1 --tag v1.2.1

脚本只负责把某个版本在 dist/ 下的 x86 / arm 安装包补进 fnpack.json，
包括下载地址、文件大小和 sha256；其余展示信息沿用仓库里已有的配置。
"""

import argparse
import hashlib
import json
import os
import sys
from datetime import datetime, timezone

APP_NAME = "musicdl"
ARCH_FILES = {
    "x86": "musicdl_{version}_x86.fpk",
    "arm": "musicdl_{version}_arm.fpk",
}
OS_MIN_VERSION = "1.1.8"
DEFAULT_DESC = (
    "Music-DL 是一个多平台音乐工具：支持网易云、QQ、酷狗、酷我、咪咕等平台的"
    "搜索与下载，并提供 Web 界面、本地曲库管理、音质替换、一键音质提升和收藏歌单。"
)
DEFAULT_CHANGELOG = "常规更新与稳定性改进。"


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def version_key(version):
    parts = []
    for piece in version.replace("-", ".").split("."):
        digits = "".join(ch for ch in piece if ch.isdigit())
        parts.append(int(digits) if digits else 0)
    return tuple(parts)


def load_source(path):
    if os.path.exists(path):
        with open(path, "r", encoding="utf-8") as handle:
            return json.load(handle)

    return {
        "schema_version": "2",
        "source_info": {
            "name": "go-music-dl",
            "author": "ifeng369",
            "description": "Music-DL 的飞牛 fnOS 应用源",
        },
        "apps": {},
    }


def main():
    parser = argparse.ArgumentParser(description="更新 FnDepot fnpack.json")
    parser.add_argument("--repo", default=".", help="包含 fnpack.json 的目录")
    parser.add_argument("--dist", default="dist", help="存放 fpk 的目录")
    parser.add_argument("--version", required=True, help="版本号，例如 1.2.1")
    parser.add_argument("--tag", default="", help="Release tag，默认 v<version>")
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY", "ifeng369/go-music-dl"))
    parser.add_argument("--changelog", default="", help="版本更新说明")
    args = parser.parse_args()

    version = args.version.strip().lstrip("v")
    if not version:
        print("版本号不能为空", file=sys.stderr)
        return 1
    tag = args.tag.strip() or f"v{version}"
    repository = args.repository.strip().strip("/")

    fnpack_path = os.path.join(args.repo, "fnpack.json")
    source = load_source(fnpack_path)
    apps = source.setdefault("apps", {})
    app = apps.setdefault(APP_NAME, {})

    app.setdefault("display_name", "Music-DL")
    app.setdefault("desc", DEFAULT_DESC)
    app.setdefault("platform", ["x86", "arm"])
    app.setdefault("categories", ["影音娱乐"])
    app.setdefault("icon_url", f"https://raw.githubusercontent.com/{repository}/main/desktop/icon.png")
    app.setdefault("readme_url", f"https://raw.githubusercontent.com/{repository}/main/README.md")
    app.setdefault("bug_report_url", f"https://github.com/{repository}/issues")
    app.setdefault("maintainer", repository.split("/")[0])
    app.setdefault("maintainer_url", f"https://github.com/{repository.split('/')[0]}")
    app.setdefault("run_as", "package")
    app.setdefault("install_type", "")
    app.setdefault("is_docker", False)
    app.setdefault("service_port", "18081")

    releases = app.setdefault("releases", {})
    release = releases.setdefault(version, {})
    release["changelog"] = args.changelog.strip() or DEFAULT_CHANGELOG
    release["updated_at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    release["os_min_version"] = OS_MIN_VERSION

    packages = release.setdefault("packages", {})
    for arch, pattern in ARCH_FILES.items():
        filename = pattern.format(version=version)
        path = os.path.join(args.dist, filename)
        if not os.path.exists(path):
            print(f"缺少 {arch} 安装包：{path}", file=sys.stderr)
            return 1
        packages[arch] = {
            "download_url": f"https://github.com/{repository}/releases/download/{tag}/{filename}",
            "sha256": sha256_file(path),
            "size": os.path.getsize(path),
        }
        print(f"{arch}: {filename} size={packages[arch]['size']} sha256={packages[arch]['sha256']}")

    # 只保留最近的 100 个版本，避免超过 FnDepot 的单应用版本上限。
    ordered = sorted(releases.keys(), key=version_key, reverse=True)[:100]
    app["releases"] = {key: releases[key] for key in ordered}

    with open(fnpack_path, "w", encoding="utf-8", newline="\n") as handle:
        json.dump(source, handle, ensure_ascii=False, indent=2)
        handle.write("\n")

    print(f"已更新 {fnpack_path}（版本 {version}，tag {tag}）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
