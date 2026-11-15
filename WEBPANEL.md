# Web 控制面板 / Web panel

这是从 [FlClash-web](https://github.com/libzonda/FlClash-web) 移植的独立 Web 构建：浏览器运行 Flutter 界面，Go 进程只提供鉴权后的 typed HTTP API（`/api/v1/*`、`/api/auth/*`）与事件 WebSocket（`/ws/v1/events`）；业务数据保存在托管资源目录之外的私有 bbolt 状态目录（`/state`）。浏览器不运行 SQLite 引擎，也没有通用文件系统或 HTTP 转发通道；原生桌面/Android 仍使用 Drift/SQLite。它不是桌面窗口的远程镜像，也不会自动连接已运行的桌面客户端。

The Web build runs independently of the native app. Its authenticated typed HTTP API (`/api/v1/*`, `/api/auth/*`) and event WebSocket (`/ws/v1/events`) serve the Flutter interface, and business data lives in a private server-side bbolt store outside the managed resource directory. The browser runs no SQLite engine and has no generic filesystem or HTTP-proxy endpoint; native desktop and Android builds keep Drift/SQLite. Native builds retain their existing entry points.

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

Web 构建显式设置 `WEB_PANEL=true`，并只在隔离副本中加入账户页面和 Web 专属本地化文案。原生桌面、手机构建的入口、设置页和 ARB 文件不增加这些内容；Go 登录与账户接口也只编译在 `webpanel && !android` 条件下。

## Docker 与 Compose

`Dockerfile` 从当前源码构建 Web 界面和 Go 服务，不使用宿主机的 `dist/` 旧产物。构建阶段固定 Flutter 3.47.6 及其源码提交、Go 1.27.3；运行阶段只带可执行文件、CA 证书、时区数据和健康检查工具。本地构建需要 Docker BuildKit，构建上下文中须有已初始化的两个子模块：

```bash
docker build --build-arg VCS_REF="$(git rev-parse HEAD)" -t flclash-web:local .
python3 tool/test_container.py --image flclash-web:local
```

当前验证平台为 `linux/amd64`。默认 `compose.yaml` 只使用本地镜像（`pull_policy: never`），不会从镜像仓库拉取同名版本。可以直接在仓库根目录部署，也可以把 `compose.yaml`、`.env.example` 复制到独立部署目录，例如 `~/deploy/flclash`，然后在该目录首次初始化：

```bash
cp -n .env.example .env
mkdir -p data auth state secrets
chmod 700 data auth state secrets
# Compose 使用 bind.create_host_path: false：目录必须预先存在且属于运行时 UID/GID。
# Dockerfile 的 USER 为 1000:1000；若 .env 改了 FLCLASH_UID/FLCLASH_GID，用 id -u / id -g 的结果。
chown 1000:1000 data auth state
(umask 077; python3 -c "import pathlib,secrets; pathlib.Path('secrets/web_password').open('x').write(secrets.token_urlsafe(32))")
chown 1000:1000 secrets/web_password
docker compose up -d --wait --wait-timeout 60
```

密码文件使用排他创建，已存在时不会覆盖；首次引导密码须是有效 Unicode，长度为 1–256 个字符且不能包含换行（文件末尾单个换行会被去掉）。重新部署时不要删除 `data`、`auth`、`state` 或 `secrets`。`.env.example` 中的用户名为 `admin`，没有默认密码。需要查看首次生成的引导密码时，在部署目录执行 `cat secrets/web_password`，不要把它粘贴进日志或提交到 Git。网页修改密码后，该文件不再代表当前密码；新密码只以带盐派生值保存在 `auth/credentials.json` 中。

- 默认地址为 **`http://127.0.0.1:18080`**，可通过 `.env` 的 `FLCLASH_WEB_BIND`、`FLCLASH_WEB_PORT` 修改。
- 数据使用相对路径 **`./data:/data`**；持久账户使用 **`./auth:/auth`**；私有 API 状态使用 **`./state:/state`**；首次引导密码使用独立的 **`./secrets/web_password`**，只读挂载到 `/run/secrets/web_password`。相对路径以 Compose 文件所在目录为准，不依赖源码目录。Compose 明确设置 `create_host_path: false`，目录不存在时拒绝启动，不会替用户创建或猜测权限。
- 容器以非 root 身份运行。`FLCLASH_UID`、`FLCLASH_GID` 默认均为 `1000`；若部署用户不是这个 ID，在 `.env` 中改为 `id -u`、`id -g` 的结果，确保 `data`、`auth`、`state` 目录和密码文件属于该用户。
- 默认代理端口映射为 `127.0.0.1:17890` → 容器 `7890`，包含 TCP/UDP。导入配置后，还需在面板启动代理，并开启“局域网代理”，容器端口才可经桥接网络访问。面板密码不等于代理认证。
- 默认无 `privileged`、host 网络或 TUN 设备授权，移除全部 Linux capabilities，根文件系统只读，临时写入限定在 tmpfs `/tmp`。这个模板面向 Web 面板和普通端口代理，不是透明网关部署方案。
- 健康检查访问容器内 `8080/login`，不读取或记录凭据；`healthy` 只说明面板 HTTP 服务就绪，不表示代理、订阅或出站网络已经配置。
- `FLCLASH_WEB_SECURE_COOKIE=true` 仅用于 HTTPS 反代；HTTP 本地访问保持 `false`。对远端开放前应按下文配置 HTTPS。

停止服务用 `docker compose down`，再运行 `docker compose up -d --wait` 会继续使用同一份相对目录数据。更新镜像后用 `docker compose up -d --force-recreate --wait`；不要执行删除数据目录的清理操作。

The Compose template keeps data in `./data`, persistent account credentials in `./auth`, and the bootstrap password in a separate `./secrets/web_password` file. It uses a locally built image, loopback-only published ports and a non-root, read-only container by default. `tool/test_container.py` checks the real image, authentication, restart persistence and the deployment defaults described here.

## 设置用户名和密码

**服务没有默认账号或默认密码。首次初始化且没有持久账户文件时，缺少用户名或密码会拒绝启动。** 后续启动优先读取持久账户文件，不会用环境变量中的旧账号覆盖网页修改后的账号。损坏的账户文件会导致启动失败，而不是恢复到旧密码。

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

然后访问 `http://127.0.0.1:8080`，输入所配置的用户名、密码。在“工具 → 设置 → Web 账户”中修改用户名或密码；退出登录只在 Web 主导航（宽屏侧栏；窄屏在底部导航的 utility 区域），不在账户页。

也可由服务管理器注入 `FLCLASH_WEB_PASSWORD`；首次引导时它与 `FLCLASH_WEB_PASSWORD_FILE` 二选一，不要同时设置。不要把真实凭据提交进仓库。账户文件已创建后，环境变量和引导密码文件不再是当前账户的权威来源。

| 配置 | 用途 |
| --- | --- |
| `FLCLASH_WEB_USERNAME` / `--username` | 没有持久账户时必填的引导用户名 |
| `FLCLASH_WEB_PASSWORD` | 通过环境提供首次引导密码 |
| `FLCLASH_WEB_PASSWORD_FILE` / `--password-file` | 从受保护文件读取首次引导密码 |
| `FLCLASH_WEB_AUTH_FILE` / `--auth-file` | 持久账户文件，默认位于数据目录旁的 `<数据目录>.auth.json`；必须在数据/静态资源目录之外 |
| `FLCLASH_WEB_LISTEN` / `--listen` | HTTP / WebSocket 监听地址，默认 `127.0.0.1:8080` |
| `FLCLASH_DATA_DIR` / `--data-dir` | 服务端数据目录；请使用独立目录，不与正在运行的原生客户端共享 |
| `FLCLASH_WEB_DIR` / `--web-dir` | 可选，使用已编译的 Web 资源目录替代内嵌资源 |
| `FLCLASH_WEB_SECURE_COOKIE` / `--secure-cookie` | HTTPS 反代部署时启用 Secure Cookie |

Bootstrap credentials are mandatory when no account store exists. An existing valid account store takes precedence over the environment or bootstrap password file. Sessions are invalidated by logout, account changes, expiry or server restart. Passwords are not stored in browser local storage.

### 修改 Web 账户

打开“工具 → 设置 → Web 账户”，输入当前密码后修改用户名或设置新密码。用户名不允许为空，最多 64 个 Unicode 字符；新密码留空表示保持原密码，否则需要 8–256 个字符且不能全为空白。用户名和密码都不允许换行。这里只修改 Web 面板登录，不改变代理端口认证。

修改成功后立即撤销所有已登录会话和 WebSocket 连接，包括当前页面，随后使用新凭据重新登录。不需要重启核心；账户变更在容器/进程重启后仍然保留。错误的当前密码或发布前保存失败不会让旧凭据失效。如果文件已经原子替换、但目录同步失败，新凭据仍然生效并撤销旧会话，登录页会明确提示“持久化确认失败”；应检查服务器存储，不能假定旧密码仍然有效。

Compose 的账户文件为 `./auth/credentials.json`，采用带随机盐的密码派生值存储，权限为 `0600`。应用生成的备份不包含该文件；如需保留登录账户，应在停服后单独保护 `auth` 目录做主机级快照，且不要把它放进可下载数据。

从旧版本 Compose 更新时，先在原部署目录创建 `auth`、`state` 并赋予部署用户写权限（`mkdir -p auth state && chmod 700 auth state && chown 1000:1000 auth state`），再更新模板和镜像。首次启动会以原引导账号初始化账户文件，不会主动更换原密码。

若忘记网页设置的新密码，应先停止服务，把账户文件备份到同一个私有 `auth` 目录中的另一个文件名，再用明确设置的引导账号启动。只有账户文件不存在时才会重新初始化；不要删除数据目录来重置登录。回退到不支持账户文件的旧镜像会重新使用引导凭据，因此回退前应同步检查账号安全。

## 远程访问与安全边界

- 默认只监听本机。如需局域网监听，可显式设置 `--listen 0.0.0.0:8080`；**HTTP 不加密密码、会话及代理配置，远程使用应部署 HTTPS**。
- HTTPS 反向代理应保留客户端的原始 `Host`，转发 WebSocket Upgrade，并在后端启用 `--secure-cookie`。后端保持监听 loopback，避免绕过 HTTPS 直接访问。不要把 `X-Forwarded-*` 当作可信身份信息。
- 登录会话同时保护静态资源、`/api/v1/*`、`/api/auth/*` 和 `/ws/v1/events`，不是仅在前端隐藏页面。跨源操作会被拒绝；旧的 `/api/fs/*`、`/api/http-proxy`、`/ws/core` 不再注册，也没有通用回退通道。
- Web 模式不开放独立的 Clash 外部控制器（包括 9090 REST API）；否则该接口会成为另一条管理入口。代理端口是数据通道，不受面板登录保护，需要按用途另配代理认证与防火墙。
- 已登录用户是管理员，能够修改代理配置并让服务端发起网络请求。登录保护不是操作系统沙箱，也不是多租户权限隔离。优先用非 root 用户运行，限制数据目录访问权限，不与不可信本地用户共享可写目录。
- `/data` 是服务端托管资源目录，`/state` 是托管目录与账户目录之外的私有 API 权威库（目录 `0700`、库文件 `0600`）；两者都不是浏览器虚拟文件系统，也没有可写的通用文件接口。不要把它们与原生客户端正在使用的数据目录共享。
- Web 页面、密码文件、`/data`、`/auth` 和 `/state` 应彼此分离。备份由服务端生成一致快照；不要直接复制运行中的 `state/resources.db`，也不要把密码文件放进可下载的数据里。

Use HTTPS for remote access. The authenticated account is an administrator, not a sandboxed tenant. The Web panel login does not authenticate proxy traffic. Run with the minimum required OS privileges and keep its data separate from a live native installation.

## 数据、迁移与备份

Web 的业务数据由服务端私有 bbolt 库 `/state/resources.db` 持有（目录 `0700`、文件 `0600`）；`/data` 只放托管资源；原生客户端的 Drift/SQLite 实时库不变，Web 进程不打开这个 live 库、不镜像也不上传它。唯一例外是服务端处理 native legacy archive 时，会在私有临时目录以只读方式打开归档内的 SQLite 做受限导入校验；它不是浏览器引擎、不是权威库，也不会触碰 live 原生库。

- **没有自动迁移或覆盖**：启动时不会把旧 `database.sqlite`、`shared_preferences.json` 或旧 Web 数据目录导入并覆盖当前库；当前也没有生产迁移授权。需要迁移时，先用原客户端做合法备份，在副本上通过 typed 导入/预览与确认流程验证，再决定是否操作真实部署。
- **不支持的旧格式明确拒绝**：不支持的 legacy/v0 转换返回明确错误，不会静默丢字段或声称已覆盖所有历史版本。
- **备份仍含敏感项**：代理密码、订阅 token、显式配置的私钥等会随备份保存。WebDAV 凭据默认按整组排除，恢复时保留当前值；要把它们包含进备份，需要单独的显式确认。Web 登录账户（`/auth/credentials.json`）不导出、不备份。
- **运行态身份不随包恢复**：`storeId`、已提交修订号与幂等回执属于服务端运行态，不从备份包恢复；恢复包到另一个库时保留目标库自己的身份。自动生成的 Tailscale/ZeroTier 设备身份与运行 state 不随包 clone/restore；planet 引用等 typed 配置资产由 Web（flclash-v1）备份/clone 保留，显式配置的私钥仍按敏感处理并原样保留。
- **`native-v1` 不降级**：若 profile 引用了 planet 等运行态资源，`native-v1` 导出明确拒绝（`backup_native_profile_asset_unsupported`），不会为了导出而丢资产。

## 脚本求值沙箱

脚本求值只在服务端运行一个无 import 的 QuickJS WASM guest，该 guest 不进入浏览器产物（Web 资源若包含 `quickjs*.wasm` 会被构建门禁拒绝）。

- 并发准入 2 个 guest；每个 guest 线性内存上限 256 MiB（4096 页），guest 截止时间 10 秒。
- 单次输入/输出上限 32 MiB；求值缓存 64 MiB（最多 8 项）。
- 这不是整个 Go 进程的 RSS 上限，也不是对请求总 CPU 的硬上限；宿主侧 YAML/JSON 解析另有工作量预算，其他接口仍受各自的请求体与超时限制约束。

## Web 版边界

- 托盘、系统代理设置、全局快捷键和系统证书安装等原生能力不能作用于访问页面的电脑。Web 兼容层只替代浏览器无法使用的原生接口。
- 数据保存在服务端，关闭浏览器不等于停止已运行的核心。但服务器重启后的启动行为与原生系统服务不同，不能把浏览器中的“开机启动”当作 systemd 服务注册。
- 保留原生客户端的安全默认值，不自动放宽 DNS 监听或外部控制器地址。
- 多个标签页可以同时打开，但写操作必须携带服务端修订号与幂等 operationId；过期写入返回 `409`，不会静默合并或由前端覆盖服务端。
- typed 内容读写每次上限 32 MiB；命令 JSON 默认 1 MiB，批量替换等有独立上限；单条事件 1 MiB、每连接事件队列 4 MiB。超出限制会明确报错，不会退回通用文件或 HTTP 转发。
- 当前构建入口面向 Bash 主机；其他操作系统的运行、TUN 权限及系统服务部署需要在目标环境另行验证。

## 验证

本页声称的 Web 能力都由仓库中的功能测试约束：认证、typed API、CAS/幂等、私有库持久化在 `core/web_api*_test.go`、`core/web*_test.go` 与 `core/cmd/flclash-web/`；staging、artifact 校验、VFS 与浏览器桥接在 `tool/web/`；Compose 模板与真实镜像冒烟在 `tool/test_container.py`。文档改动不新增未测试能力。

```bash
# Python 与 Node bridge/login 批次
python3 -m unittest discover -s tool/web -p 'test_*.py'
node --test tool/web/bridge_test.mjs tool/web/login_test.mjs

# 完整 Web 测试（Python + Node + staged Dart/JS VFS + Chrome typed API/UI）：
python3 tool/web/test.py

# 只校验 staged 源与 release 产物（--web/--server 需要真实构建产物）：
python3 tool/web/validate_artifact.py --staged <staged-src> \
  --deps <staged-src>/.dart_tool/package_config.json \
  --web <build/web> --server dist/FlClash-web --trace <requests.json|har.json> \
  --origin https://<panel-host>

# Go HTTP/API 与竞态子集：
(cd core && CGO_ENABLED=0 go test -tags webpanel,with_gvisor . ./cmd/flclash-web)
(cd core && go test -race -tags webpanel,with_gvisor -run '^TestWeb' . ./cmd/flclash-web)

# 镜像与 Compose 模板（需本机 Docker 与已构建镜像）：
python3 tool/test_container.py --image flclash-web:local
```

Flutter/Chrome 部分、真实 release 三处 artifact 门禁、`core` 全量 Go 测试和 `test_container.py` 需要对应工具链/镜像，可在本地或 CI 按上述命令复现。构建或单元测试成功不等于 release 或镜像验证。

竞态检测单独选择 Web 测试：当前子模块 `Clash.Meta/hub/route/server.go` 的既有控制器重建测试存在独立竞态，在不带 `webpanel` 标签时也能复现；本改动没有修改该子模块。

构建或单元测试成功不代表 TUN、透明网关、真实订阅或所有浏览器行为都已验证。实际部署前应在独立数据目录和测试网络中验证所需功能。
