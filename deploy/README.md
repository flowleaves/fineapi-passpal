# 服务器部署（117.72.104.206:3200）

面向 jdssh 服务器 `lavm-abh31msinu` 的部署说明。**与仓库根目录那套 compose 是两条路**，
不要混用。

## 为什么不用根目录的多阶段 Dockerfile

那台机器是 **2C2G**，且到 Docker Hub / npm 的链路很慢。根目录的 Dockerfile 要在服务器上
拉 `golang:1.26-alpine` + `node:22-alpine`，再跑 `npm ci`（约 200MB 依赖）——
这台机器历史上就因为 `docker build` 被拖死过一次（SSH 读不到 banner、1Panel 无响应）。

而 passpal 的二进制已经把 **前端产物（`web/dist`）和迁移 SQL 全部 `go:embed` 进去了**，
所以运行镜像只需要 `alpine` + 一个文件：

| | 根目录 Dockerfile | `deploy/Dockerfile.runtime` |
|---|---|---|
| 服务器上编译 | 是（Go + npm） | **否** |
| 需拉取的镜像 | golang + node（约 200MB+） | 仅 `alpine:3.22`（约 3MB） |
| 上传体积 | 源码 | **11.7MB 二进制** |
| 实测耗时 | 数分钟~数十分钟，有拖死风险 | 上传 8.5s + 构建 2s |

## 部署步骤

### 1. 开发机：交叉编译

```bash
cd passpal
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o deploy/dist/passpal ./cmd/passpal
```

> `CGO_ENABLED=0` 是因为 SQLite 驱动用 `modernc.org/sqlite`（纯 Go）。
> 产物是静态链接的 stripped ELF，不依赖 libc。
> `deploy/dist/` 已被 `.gitignore` 排除。

### 2. 生成配置

```bash
# 管理员密码 → Argon2id hash（用本机二进制生成即可，hash 与平台无关）
./passpal hash-password --password '<你的密码>'
# 数据加密密钥：32 字节 base64
openssl rand -base64 32
```

写入 `deploy/pkg/.env`，内容参考仓库根的 `.env.example`，服务器侧关键项：

```ini
APP_ENV=production
APP_ORIGIN=http://117.72.104.206:3200   # 必须与浏览器访问地址完全一致，否则登录 403
BIND_ADDR=0.0.0.0
PORT=3000
DATABASE_PATH=/app/data/database.sqlite
BACKUP_DIR=/app/backups
ADMIN_PASSWORD_HASH=<hash>
DATA_ENCRYPTION_KEY_V1=<key>
DATA_ENCRYPTION_KEY_CURRENT=1
```

### 3. 上传并启动

```bash
cd Desktop/jdssh
export MSYS_NO_PATHCONV=1                 # ← 见下方「坑 1」，不加必失败
python xssh.py run "mkdir -p /opt/passpal/dist /opt/passpal/data /opt/passpal/backups" --confirm
python xssh.py put <pkg>/.env                /opt/passpal/.env                --confirm
python xssh.py put <pkg>/Dockerfile.runtime  /opt/passpal/Dockerfile.runtime  --confirm
python xssh.py put <pkg>/docker-compose.yml  /opt/passpal/docker-compose.yml  --confirm
python xssh.py put <pkg>/dist/passpal        /opt/passpal/dist/passpal        --confirm

python xssh.py run "chmod 600 /opt/passpal/.env
  chown -R 10001:10001 /opt/passpal/data /opt/passpal/backups   # ← 见下方「坑 3」
  cd /opt/passpal && docker compose up -d --build" --confirm
```

### 4. 验收

```bash
curl -s http://117.72.104.206:3200/api/health        # {"data":{"status":"ok"}}
docker ps --filter name=passpal                      # Up (healthy)
```

## 三个必须知道的坑（都实际踩过）

### 坑 1 · Git Bash 会改写传给 `put` 的远程路径

MSYS 的路径转换会把独立参数形式的 `/opt/passpal/.env` 改写成
`C:/Users/.../PortableGit/opt/passpal/.env`，SFTP 于是报
`FileNotFoundError: [Errno 2] No such file` —— 看起来像服务器没建目录，实际是本地被改了。

**必须先 `export MSYS_NO_PATHCONV=1`。**

> `run` 子命令不受影响：整条命令作为**单个含空格的参数**传入，MSYS 不转换。
> 只有 `put` / `get` 这种「路径是独立参数」的场景才会中招。

### 坑 2 · Compose 会吃掉 `ADMIN_PASSWORD_HASH` 里的 `$`

Argon2id 的 PHC 格式是 `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>`。
Docker Compose 对 `env_file` 做**变量插值**，于是 `$argon2id` / `$v` / `$m` 被替换成空串，
容器里拿到的是 `=19=65536,t=3,p=4...`，启动直接报
`ADMIN_PASSWORD_HASH 格式非法：必须以 $argon2id$ 开头`，容器进入重启循环。

**`.env` 里的 `$` 必须写成 `$$`**（5 个 `$` → 5 个 `$$`）。
这是 compose 的转义约定，不是 shell 的。已验证：容器内取到正确的原始 hash。

### 坑 3 · 数据目录属主必须是 10001

容器以非 root 的 `10001:10001` 运行，而 `mkdir` 建出来的是 `root:root`，
导致 `unable to open database file (14)` 并重启循环。

```bash
chown -R 10001:10001 /opt/passpal/data /opt/passpal/backups
```

## 运行期配置

`deploy/docker-compose.yml` 已启用：非 root `10001`、`read_only` rootfs、
`cap_drop ALL`、`no-new-privileges`、`mem_limit 256m`、`pids_limit 64`、
日志轮转 10m×3。端口 `3200:3000`。

**不碰的东西**：同机上的 `momo`（5200）与 1Panel（12914）保持原样。

## 部署后修复的两个缺陷

### 1 · 明文 HTTP 下登录后立刻「会话无效」

`pp_session` cookie 带 `Secure` 标志，而 `Secure` cookie **只在 HTTPS 下才会被浏览器回传** ——
服务跑在明文 HTTP 上时，登录接口返回 200，但浏览器下次请求不带 cookie，页面报
「会话无效或已过期」。

根因是 `secure` 标志直接取自 `IsProduction()`。**正确做法是按 `APP_ORIGIN` 的实际
scheme 决定**，而不是按环境名。已改为 `Config.CookieSecure()`：`https://` 开头才置 `Secure`。
生产 + 明文 HTTP 时启动日志会打 `cookie_secure=false` 并给出告警。

> 这也解释了为什么本地 `APP_ENV=dev` 测不出来 —— 只有生产模式才触发。

### 2 · 备份只存快照，密钥一丢就再也恢复不了

原备份是 `VACUUM INTO` 出来的纯 `.sqlite` 快照，**敏感字段仍是密文**，
恢复时必须另外提供当时的 `DATA_ENCRYPTION_KEY_*`。密钥遗失或轮换过 = 备份作废。

已改为**自包含归档** `backup-<时间戳>.tar.gz`，内含加密快照 + `keys.json` + `manifest.json`，
**恢复只需这一个文件**。旧格式纯快照仍兼容。

> 顺带修了一个静默 bug：`maxSchemaVersion` 原为手写常量 `2`，而 schema 已演进到 4，
> 导致从迁移 0003 起恢复功能一直失效。已改为从内嵌迁移文件推导（`passpal.MaxMigrationVersion()`），
> 并有单测锁死。

## 升级

```bash
# 开发机重新交叉编译 → 覆盖上传 → 重建
python xssh.py put <pkg>/dist/passpal /opt/passpal/dist/passpal --confirm
python xssh.py run "cd /opt/passpal && docker compose up -d --force-recreate" --confirm
```

数据库在 `data/` 卷里，重建容器不影响数据；迁移会在启动时自动应用
（应用前会在 `backups/migrations/` 生成快照）。
