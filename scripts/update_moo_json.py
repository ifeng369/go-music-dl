#!/usr/bin/env python3
"""更新 Moo 应用源的 moo.json。

Moo 是飞牛 fnOS 上的第三方应用中心，仓库根目录的 moo.json 就是它的应用源索引
（协议见 https://github.com/Blue-Mink/moo/blob/main/docs/Moo应用源协议.md）。

用法示例：
    python3 scripts/update_moo_json.py --repo . --dist dist --version 1.2.3 --tag v1.2.3

脚本把某个版本在 dist/ 下的 x86 / arm 安装包补进 moo.json，包括下载地址、
文件大小和 sha256；其余展示信息沿用仓库里已有的配置。若传入的是旧版
fnpack.json 的目录，也能直接迁移出 moo.json（只保留版本数据）。
"""

import argparse
import hashlib
import json
import os
import sys
from datetime import datetime, timezone

SCHEMA_VERSION = "moo"
APP_NAME = "musicdl"
ARCH_FILES = {
    "x86": "musicdl_{version}_x86.fpk",
    "arm": "musicdl_{version}_arm.fpk",
}
MIN_FNOS = "1.1.8"
CATEGORIES = ["影音娱乐"]
LICENSE = "AGPL-3.0"
# 详情页徽章：原作者（上游仓库）与发布者（本仓库）分开，Moo 的语义见协议 §4.4。
UPSTREAM_AUTHOR = "guohuiyuan"
UPSTREAM_URL = "https://github.com/guohuiyuan/go-music-dl"
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


def now_iso():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def load_source(path, repository):
    """读取现有 moo.json；旧 fnpack.json 也能读进来做一次性迁移。"""
    source = {}
    if os.path.exists(path):
        with open(path, "r", encoding="utf-8") as handle:
            source = json.load(handle)
    if not isinstance(source, dict):
        source = {}
    if source.get("schema_version") != SCHEMA_VERSION:
        # 旧格式（如 fnpack.json 的 "2"）只保留 apps 里的条目，按 moo 重新组织。
        source = {"schema_version": SCHEMA_VERSION, "apps": source.get("apps") or {}}

    owner = repository.split("/")[0]
    source["schema_version"] = SCHEMA_VERSION
    info = source.setdefault("source_info", {})
    info.setdefault("name", "go-music-dl")
    info.setdefault("author", owner)
    info.setdefault("distributor", owner)
    info.setdefault("homepage", f"https://github.com/{repository}")
    info.setdefault("description", "Music-DL 的飞牛 fnOS 应用源，提供 x86 / arm 安装包。")
    info["updated_at"] = now_iso()
    return source


def normalize_app(app, repository):
    owner = repository.split("/")[0]
    app.setdefault("display_name", "Music-DL")
    app.setdefault("app_type", "fpk")
    app.setdefault("platform", ["x86", "arm"])
    app.setdefault("categories", CATEGORIES)
    app.setdefault("desc", DEFAULT_DESC)
    app.setdefault("author", UPSTREAM_AUTHOR)
    app.setdefault("author_url", UPSTREAM_URL)
    app.setdefault("distributor", owner)
    app.setdefault("homepage", f"https://github.com/{repository}")
    app.setdefault("bug_report_url", f"https://github.com/{repository}/issues")
    app.setdefault("license", LICENSE)
    app.setdefault("icon_url", f"https://raw.githubusercontent.com/{repository}/main/desktop/icon.png")
    app.setdefault("readme_url", f"https://raw.githubusercontent.com/{repository}/main/README.md")
    app.setdefault("install_type", "package")
    app.setdefault("service_port", 18081)
    app.setdefault("min_fnos", MIN_FNOS)
    app["releases"] = app.get("releases") or {}

    # fnpack.json 迁移过来的字段在 moo 里没有对应语义，清理掉避免歧义。
    for legacy in ("maintainer", "maintainer_url", "run_as", "is_docker"):
        app.pop(legacy, None)
    return app


def normalize_release(release):
    release.pop("os_min_version", None)
    for pkg in (release.get("packages") or {}).values():
        size = pkg.get("size")
        if isinstance(size, int) and size > 0:
            pkg["size_mb"] = round(size / (1024 * 1024), 2)
    return release


def main():
    parser = argparse.ArgumentParser(description="更新 Moo 应用源 moo.json")
    parser.add_argument("--repo", default=".", help="包含 moo.json 的目录")
    parser.add_argument("--dist", default="dist", help="存放 fpk 的目录")
    parser.add_argument("--version", required=True, help="版本号，例如 1.2.3")
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

    moo_path = os.path.join(args.repo, "moo.json")
    source = load_source(moo_path, repository)
    apps = source.setdefault("apps", {})
    app = normalize_app(apps.setdefault(APP_NAME, {}), repository)

    releases = app["releases"]
    release = normalize_release(releases.setdefault(version, {}))
    release["changelog"] = args.changelog.strip() or DEFAULT_CHANGELOG
    release["updated_at"] = now_iso()

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
            "size_mb": round(os.path.getsize(path) / (1024 * 1024), 2),
        }
        print(f"{arch}: {filename} size={packages[arch]['size']} sha256={packages[arch]['sha256']}")

    # 只保留最近的 100 个版本，避免索引无限增长。
    ordered = sorted(releases.keys(), key=version_key, reverse=True)[:100]
    app["releases"] = {key: releases[key] for key in ordered}

    with open(moo_path, "w", encoding="utf-8", newline="\n") as handle:
        json.dump(source, handle, ensure_ascii=False, indent=2)
        handle.write("\n")

    print(f"已更新 {moo_path}（版本 {version}，tag {tag}）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
