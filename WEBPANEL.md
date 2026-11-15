# Web 控制面板 / Web panel

这是从 [FlClash-web](https://github.com/libzonda/FlClash-web) 移植的独立 Web 构建：浏览器运行 Flutter 界面，Go 进程提供核心 RPC、文件存储和网络请求转发。它不是桌面窗口的远程镜像，也不会自动连接已运行的桌面客户端。

The Web build runs independently of the native app. Its authenticated HTTP server serves the Flutter interface, Core WebSocket and server-side storage. Native desktop and Android builds retain their existing entry points.

## 构建

在本仓库根目录准备 Flutter 3.47.6、Go 1.27 或更新版本、Python 3 和 Bash，并初始化锁定版本的子模块：

```bash
git submodule update --init --recursive
bash tool/build_web.sh
```

若没有 GitHub SSH 访问权限，可仅对这次子模块初始化使用 HTTPS：

```bash
git -c url.https://github.com/.insteadOf=git@github.com: submodule update --init --recursive
```

构建脚本使用当前检出的源码，不获取“最新版本”、不切换分支。Web 兼容层只应用到隔离的构建副本，不覆盖原生 `lib/`、插件、`pubspec.yaml` 或生成文件。构建结果为 `dist/FlClash-web`，其中包含 Web 静态资源。不要对原生源码直接运行 `flutter build web`。

The build uses an isolated copy of the current checkout. It does not patch and restore native sources in place, and does not depend on a sibling `FlClash-web` repository. The resulting executable includes the Web assets.

当前适配针对本仓库的 `0.9.3` 基线，升级应用或插件时需要同步核对适配锚点；版本或锚点不匹配会拒绝构建，而不是悄悄漏掉补丁。`bash tool/build_web.sh --check` 仅检查前置依赖；`--safe-mode` 构建供本机界面验证使用的安全模式版本，不用于正常代理运行。

## Docker 与 Compose

`Dockerfile` 从当前源码构建 Web 界面和 Go 服务，不使用宿主机的 `dist/` 旧产物。构建阶段固定 Flutter 3.47.6 及其源码提交、Go 1.27.3；运行阶段只带可执行文件、CA 证书、时区数据和健康检查工具。本地构建需要 Docker BuildKit，构建上下文中须有已初始化的两个子模块：

```bash
docker build --build-arg VCS_REF="$(git rev-parse HEAD)" -t flclash-web:local .
python3 tool/test_container.py --image flclash-web:local
```

当前验证平台为 `linux/amd64`。默认 `compose.yaml` 只使用本地镜像（`pull_policy: never`），不会从镜像仓库拉取同名版本。可以直接在仓库根目录部署，也可以把 `compose.yaml`、`.env.example` 复制到独立部署目录，例如 `~/deploy/flclash`，然后在该目录首次初始化：

```bash
cp -n .env.example .env
mkdir -p data secrets
chmod 700 data secrets
(umask 077; python3 -c "import pathlib,secrets; pathlib.Path('secrets/web_password').open('x').write(secrets.token_urlsafe(32))")
docker compose up -d --wait --wait-timeout 60
```

密码文件使用排他创建，已存在时不会覆盖；重新部署时不要删除 `data` 或 `secrets`。`.env.example` 中的用户名为 `admin`，没有默认密码。需要查看首次生成的密码时，在部署目录执行 `cat secrets/web_password`，不要把它粘贴进日志或提交到 Git。

- 默认地址为 **`http://127.0.0.1:18080`**，可通过 `.env` 的 `FLCLASH_WEB_BIND`、`FLCLASH_WEB_PORT` 修改。
- 数据使用相对路径 **`./data:/data`**；密码使用独立的 **`./secrets/web_password`**，只读挂载到 `/run/secrets/web_password`。相对路径以 Compose 文件所在目录为准，不依赖源码目录。
- 容器以非 root 身份运行。`FLCLASH_UID`、`FLCLASH_GID` 默认均为 `1000`；若部署用户不是这个 ID，在 `.env` 中改为 `id -u`、`id -g` 的结果，确保数据目录和密码文件属于该用户。
- 默认代理端口映射为 `127.0.0.1:17890` → 容器 `7890`，包含 TCP/UDP。导入配置后，还需在面板启动代理，并开启“局域网代理”，容器端口才可经桥接网络访问。面板密码不等于代理认证。
- 默认无 `privileged`、host 网络或 TUN 设备授权，移除全部 Linux capabilities，根文件系统只读，临时写入限定在 tmpfs `/tmp`。这个模板面向 Web 面板和普通端口代理，不是透明网关部署方案。
- 健康检查访问容器内 `8080/login`，不读取或记录凭据；`healthy` 只说明面板 HTTP 服务就绪，不表示代理、订阅或出站网络已经配置。
- `FLCLASH_WEB_SECURE_COOKIE=true` 仅用于 HTTPS 反代；HTTP 本地访问保持 `false`。对远端开放前应按下文配置 HTTPS。

停止服务用 `docker compose down`，再运行 `docker compose up -d --wait` 会继续使用同一份相对目录数据。更新镜像后用 `docker compose up -d --force-recreate --wait`；不要执行删除数据目录的清理操作。

The Compose template keeps data in `./data` and the password in a separate `./secrets/web_password` file. It uses a locally built image, loopback-only published ports and a non-root, read-only container by default. `tool/test_container.py` checks the real image, authentication, restart persistence and the deployment defaults described here.

## 设置用户名和密码

**没有默认账号或默认密码。缺少用户名或密码时服务拒绝启动。**

推荐通过密码文件提供密码，放在数据目录和静态资源目录之外。下面示例在 Bash 中交互输入密码，避免把密码写入命令历史或进程参数：

```bash
mkdir -p "$HOME/.config/flclash-web"
chmod 700 "$HOME/.config/flclash-web"
read -rs -p 'Web password: ' panel_password; printf '\n'
(umask 077; printf '%s' "$panel_password" > "$HOME/.config/flclash-web/password")
unset panel_password

FLCLASH_WEB_USERNAME=admin \
FLCLASH_WEB_PASSWORD_FILE="$HOME/.config/flclash-web/password" \
  ./dist/FlClash-web --data-dir "$HOME/.local/share/flclash-web"
```

然后访问 `http://127.0.0.1:8080`，输入所配置的用户名、密码。右上角的“退出登录 / Log out”结束当前会话。

也可由服务管理器注入 `FLCLASH_WEB_PASSWORD`；它与 `FLCLASH_WEB_PASSWORD_FILE` 二选一，不要同时设置。不要把真实凭据提交进仓库。更改账号或密码后重启服务，旧会话将失效。

| 配置 | 用途 |
| --- | --- |
| `FLCLASH_WEB_USERNAME` / `--username` | 必填用户名 |
| `FLCLASH_WEB_PASSWORD` | 通过环境提供密码 |
| `FLCLASH_WEB_PASSWORD_FILE` / `--password-file` | 从受保护文件读取密码 |
| `FLCLASH_WEB_LISTEN` / `--listen` | HTTP / WebSocket 监听地址，默认 `127.0.0.1:8080` |
| `FLCLASH_DATA_DIR` / `--data-dir` | 服务端数据目录；请使用独立目录，不与正在运行的原生客户端共享 |
| `FLCLASH_WEB_DIR` / `--web-dir` | 可选，使用已编译的 Web 资源目录替代内嵌资源 |
| `FLCLASH_WEB_SECURE_COOKIE` / `--secure-cookie` | HTTPS 反代部署时启用 Secure Cookie |

Credentials are mandatory. Use either `FLCLASH_WEB_PASSWORD` or a password file outside both the data and static directories. Sessions are held in memory and are invalidated by logout, expiry or server restart. Credentials are not stored in browser local storage.

## 远程访问与安全边界

- 默认只监听本机。如需局域网监听，可显式设置 `--listen 0.0.0.0:8080`；**HTTP 不加密密码、会话及代理配置，远程使用应部署 HTTPS**。
- HTTPS 反向代理应保留客户端的原始 `Host`，转发 WebSocket Upgrade，并在后端启用 `--secure-cookie`。后端保持监听 loopback，避免绕过 HTTPS 直接访问。不要把 `X-Forwarded-*` 当作可信身份信息。
- 登录会话同时保护静态资源、`/api/fs/*`、`/api/http-proxy` 和 `/ws/core`，不是仅在前端隐藏页面。跨源操作会被拒绝。
- Web 模式不开放独立的 Clash 外部控制器（包括 9090 REST API）；否则该接口会成为另一条管理入口。代理端口是数据通道，不受面板登录保护，需要按用途另配代理认证与防火墙。
- 已登录用户是管理员，能够修改代理配置并让服务端发起网络请求。登录保护不是操作系统沙箱，也不是多租户权限隔离。优先用非 root 用户运行，限制数据目录访问权限，不与不可信本地用户共享可写目录。
- `/data` 和 `/tmp` 是浏览器使用的虚拟路径，分别对应数据目录及其中的临时文件。文件接口拒绝目录逃逸；不要依赖它来隔离能够直接修改服务器文件的本地进程。
- Web 页面、密码文件和数据目录应彼此分离。备份数据前停止写入，避免数据库快照不一致；不要把密码文件放进可下载的数据里。

Use HTTPS for remote access. The authenticated account is an administrator, not a sandboxed tenant. The Web panel login does not authenticate proxy traffic. Run with the minimum required OS privileges and keep its data separate from a live native installation.

## Web 版边界

- 托盘、系统代理设置、全局快捷键和系统证书安装等原生能力不能作用于访问页面的电脑。Web 兼容层只替代浏览器无法使用的原生接口。
- 数据保存在服务端，关闭浏览器不等于停止已运行的核心。但服务器重启后的启动行为与原生系统服务不同，不能把浏览器中的“开机启动”当作 systemd 服务注册。
- 保留原生客户端的安全默认值，不自动放宽 DNS 监听或外部控制器地址。
- 不要同时用多个浏览器标签编辑同一份配置；浏览器缓存与服务端数据之间没有多用户编辑冲突合并机制。
- 请求体、文件读取和网络转发响应各限制为 32 MiB，单条 WebSocket 请求限制为 4 MiB。首次加载快照最多 8192 项、16 MiB 内联文件内容；大文件按需读取。超出限制会明确报错。
- 当前构建入口面向 Bash 主机；其他操作系统的运行、TUN 权限及系统服务部署需要在目标环境另行验证。

## 验证

Web 入口的认证、会话失效、WebSocket 和文件边界测试位于 `core/web*_test.go`；命令行凭据配置测试位于 `core/cmd/flclash-web/`。Web 构建适配与浏览器桥接测试位于 `tool/web/`。这些测试同时约束本页和 README 中的 Web 能力声明。

```bash
bash tool/web/test.sh
(cd core && CGO_ENABLED=0 go test -tags webpanel,with_gvisor . ./cmd/flclash-web)
(cd core && go test -race -tags webpanel,with_gvisor -run '^TestWeb' . ./cmd/flclash-web)
flutter pub get
flutter analyze --no-fatal-infos
bash tool/build_web.sh
```

竞态检测单独选择 Web 测试：当前子模块 `Clash.Meta/hub/route/server.go` 的既有控制器重建测试存在独立竞态，在不带 `webpanel` 标签时也能复现；本改动没有修改该子模块。

构建成功不代表 TUN、透明网关、真实订阅或所有浏览器行为都已验证。实际部署前应在独立数据目录和测试网络中验证所需功能。
