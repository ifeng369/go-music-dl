#!/bin/bash
set -e

# Music-DL FPK 打包脚本（fnOS / 飞牛）
#
# 用法：
#   ./build-fpk.sh                # 使用 manifest 中的版本，默认 x86，自动在 Docker 中构建
#   ./build-fpk.sh 1.0.1          # 指定版本
#   ./build-fpk.sh 1.0.1 arm      # 指定版本 + 架构（x86 / arm）
#
# 说明：
#   - 产物为 dist/musicdl_<版本>_<架构>.fpk
#   - 在 Windows / macOS 上运行时会自动调用 Docker（需要已安装 Docker 并启动）
#   - 在 Linux 上可直接运行，也可指定 MUSIC_DL_BUILD_IN_DOCKER=1 强制用 Docker
#   - 脚本内使用 Docker 时会挂载仓库与 Go 模块缓存，重复构建更快

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PKG_DIR="$SCRIPT_DIR/fnos"

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'
info() { echo -e "${GREEN}[INFO]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# === Docker 自举（宿主机不是 Linux、没有本地 Go、或显式要求时） ===
NEED_DOCKER=0
if [ "$(uname -s)" != "Linux" ]; then
    NEED_DOCKER=1
fi
if ! command -v go >/dev/null 2>&1; then
    NEED_DOCKER=1
fi
if [ "${MUSIC_DL_BUILD_IN_DOCKER:-0}" = "1" ]; then
    NEED_DOCKER=1
fi

if [ "$NEED_DOCKER" = "1" ] && [ "${MUSIC_DL_BUILD_IN_DOCKER:-0}" != "1" ]; then
    command -v docker >/dev/null 2>&1 || error "本机没有 Go，且未找到 Docker，无法打包 FPK"
    info "在 Docker 容器中执行打包..."
    exec docker run --rm \
        -v "$SCRIPT_DIR":/src \
        -v music-dl-gomod:/go/pkg/mod \
        -w /src \
        -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
        -e GOSUMDB="${GOSUMDB:-sum.golang.org}" \
        -e MUSIC_DL_BUILD_IN_DOCKER=0 \
        golang:1.25 \
        bash /src/build-fpk.sh "$@"
fi

VERSION="${1:-}"
ARCH="${2:-x86}"

APPNAME=$(grep "^appname" "$PKG_DIR/manifest" | awk -F'=' '{print $2}' | tr -d ' \r')
[ -z "$APPNAME" ] && error "无法从 manifest 读取 appname"

if [ -z "$VERSION" ]; then
    VERSION=$(grep -o 'AppVersion *= *"[^"]*"' "$SCRIPT_DIR/core/version.go" 2>/dev/null | head -n1 | sed 's/.*"\(.*\)"/\1/')
fi
if [ -z "$VERSION" ]; then
    VERSION=$(grep "^version" "$PKG_DIR/manifest" | awk -F'=' '{print $2}' | tr -d ' \r')
fi
[ -z "$VERSION" ] && error "无法确定版本号"

case "$ARCH" in
    x86|amd64|x86_64) GOARCH="amd64"; ARCH="x86" ;;
    arm|arm64|aarch64) GOARCH="arm64"; ARCH="arm" ;;
    *) error "不支持的架构: $ARCH (支持: x86, arm)" ;;
esac

info "========================================"
info "  Music-DL FPK 打包"
info "========================================"
info "版本: $VERSION | 架构: $ARCH ($GOARCH)"

command -v go >/dev/null 2>&1 || error "需要 Go 环境"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

# === 1. 编译静态二进制 ===
info "编译 music-dl..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
    go build -trimpath -ldflags="-s -w" -o "$WORK_DIR/music-dl" ./cmd/music-dl
chmod 755 "$WORK_DIR/music-dl"
info "二进制: $(du -h "$WORK_DIR/music-dl" | cut -f1)"

# === 2. app.tgz（应用本体） ===
info "构建 app.tgz..."
APP_ROOT="$WORK_DIR/app_root"
mkdir -p "$APP_ROOT/bin" "$APP_ROOT/ui"
cp "$WORK_DIR/music-dl" "$APP_ROOT/bin/"
chmod 755 "$APP_ROOT/bin/music-dl"
cp -a "$PKG_DIR/ui/." "$APP_ROOT/ui/"
find "$APP_ROOT/ui" -type d -exec chmod 755 {} +
find "$APP_ROOT/ui" -type f -exec chmod 644 {} +
(cd "$APP_ROOT" && tar czf "$WORK_DIR/app.tgz" bin ui)

# === 3. 组装 FPK 目录 ===
FPK_DIR="$WORK_DIR/package"
mkdir -p "$FPK_DIR/cmd"

cp "$WORK_DIR/app.tgz" "$FPK_DIR/"

# fnOS 框架脚本 + 应用自己的 service-setup
for f in "$PKG_DIR"/cmd/*; do
    name=$(basename "$f")
    case "$name" in
        service-setup) continue ;;
    esac
    cp "$f" "$FPK_DIR/cmd/$name"
done
cp "$PKG_DIR/cmd/service-setup" "$FPK_DIR/cmd/service-setup"
chmod 755 "$FPK_DIR/cmd/"*

cp -a "$PKG_DIR/config" "$FPK_DIR/"
cp -a "$PKG_DIR/wizard" "$FPK_DIR/"
cp "$PKG_DIR/musicdl.sc" "$FPK_DIR/"
cp "$PKG_DIR/ICON.PNG" "$FPK_DIR/"
cp "$PKG_DIR/ICON_256.PNG" "$FPK_DIR/"
cp -a "$PKG_DIR/ui" "$FPK_DIR/"
cp "$FPK_DIR/ICON_256.PNG" "$FPK_DIR/ui/images/256.png"

chmod 644 "$FPK_DIR/musicdl.sc" "$FPK_DIR/ICON.PNG" "$FPK_DIR/ICON_256.PNG"
find "$FPK_DIR/config" "$FPK_DIR/wizard" "$FPK_DIR/ui" -type d -exec chmod 755 {} +
find "$FPK_DIR/config" "$FPK_DIR/wizard" "$FPK_DIR/ui" -type f -exec chmod 644 {} +
chmod 755 "$FPK_DIR"/cmd/*

# === 4. manifest 与校验和 ===
cp "$PKG_DIR/manifest" "$FPK_DIR/manifest"
CHECKSUM=$(md5sum "$WORK_DIR/app.tgz" | cut -d' ' -f1)
sed -i "s/^version .*=.*/version         = ${VERSION}/" "$FPK_DIR/manifest"
sed -i "s/^checksum .*=.*/checksum        = ${CHECKSUM}/" "$FPK_DIR/manifest"
sed -i "s/^platform .*=.*/platform        = ${ARCH}/" "$FPK_DIR/manifest"
chmod 644 "$FPK_DIR/manifest"

# === 5. 打出 FPK（tar.gz） ===
FPK_NAME="${APPNAME}_${VERSION}_${ARCH}.fpk"
mkdir -p "$SCRIPT_DIR/dist"
(cd "$FPK_DIR" && tar czf "$SCRIPT_DIR/dist/$FPK_NAME" ./*)

info "完成: dist/$FPK_NAME ($(du -h "$SCRIPT_DIR/dist/$FPK_NAME" | cut -f1))"
info "校验和: ${CHECKSUM}"
