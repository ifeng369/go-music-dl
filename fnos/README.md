# 飞牛 fnOS 打包说明

本目录是 fnOS（飞牛 OS）的 FPK 应用打包配置，打包脚本在仓库根目录：`build-fpk.sh`（Linux/macOS/WSL）与 `build-fpk.ps1`（Windows）。

## 打包

```bash
./build-fpk.sh                 # 版本取 core/version.go 里的 AppVersion，架构 x86
./build-fpk.sh 1.1.3 arm       # 指定版本 + 架构：arm 对应 aarch64
```

```powershell
pwsh -NoProfile -File .\build-fpk.ps1                    # x86
pwsh -NoProfile -File .\build-fpk.ps1 -Version 1.1.3 -Arch arm
```

- 本机没有 Go、或不在 Linux 上时，脚本会自动通过 Docker（`golang:1.25`）编译；可用 `MUSIC_DL_BUILD_IN_DOCKER=0` 关闭自动自举。
- 模块代理默认 `https://goproxy.cn,direct`，可用环境变量 `GOPROXY` 覆盖。
- 产物：`dist/musicdl_<版本>_<x86|arm>.fpk`。

## 安装与使用

1. 飞牛 NAS → 应用中心 → 手动安装，选择生成的 `.fpk`。
2. 安装向导里设置端口，默认 **18081**。
3. 打开「Music-DL」，或直接访问 `http://NAS地址:18081/music`。
4. 下载目录默认是共享文件夹 `musicdl/downloads`，安装时自动创建并软链到应用数据目录；首次进入 Web 页面需完成初始化（设置管理员账号）。

## 目录结构

| 路径 | 作用 |
| --- | --- |
| `manifest` | 应用元信息，`checksum` 为 `app.tgz` 的 md5，由构建脚本回填 |
| `musicdl.sc` | 端口声明，端口变化时由安装脚本同步 |
| `config/privilege` | 以 `musicdl` 用户运行 |
| `config/resource` | 端口配置 + 数据共享目录 |
| `ui/config` | 应用中心入口，指向 `/music` |
| `wizard/` | 安装 / 设置 / 卸载向导 |
| `cmd/` | fnOS 生命周期脚本；`service-setup` 负责端口、启动命令、下载目录软链 |
| `ICON.PNG`、`ICON_256.PNG`、`ui/images/` | 应用图标 |

## 运行时说明

- 服务以应用数据目录 `TRIM_PKGVAR` 为工作目录，`data/settings.db`、Cookie、下载缓存都在其中，升级时保留。
- 实际端口记录在 `data/.port`，修改端口后会同步更新 `musicdl.sc`、`ui/config` 与 `manifest` 的 `service_port`（应用中心入口），重启应用生效。
- 日志：`<应用数据目录>/musicdl.log`。

## 排查：端口不生效 / 启动即停

- **应用中心打开的端口仍是旧端口**：入口端口取自已安装的 `/var/apps/musicdl/manifest` 的 `service_port`，安装脚本会同步；若应用中心仍显示旧端口，重启一次应用或重新保存设置即可刷新缓存。
- **安装时设置的端口没生效**：端口优先级为 安装向导 `wizard_port` → `data/.port` → 默认 18081；安装参数会记录在 `/var/apps/musicdl/etc/installer-variables`，可在这里核对。
- **服务启动一下就停止**：先看应用数据目录下的 `musicdl.log`。常见原因：
  - 数据目录属主不是 `musicdl`（脚本已在安装/启动时 `chown`，如手动改过目录需重新 `chown -R musicdl:musicdl`）；
  - 端口被其它程序占用（`ss -lntp | grep <端口>`）；
  - 播放器升级占用了正在播放的文件（替换会失败并提示，不影响服务运行）。
