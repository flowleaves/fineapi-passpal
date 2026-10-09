# PassPal

**个人凭据管理系统** —— 单人自用的本地 / 私有部署账号凭据库。

集中管理 Grok / GPT / Gemini / Claude / Kiro / Google 等账号及其关联凭据
（邮箱、密码、辅助邮箱、F2A、JSON 凭证）。不是企业级密码管理平台。

> 本项目是 `doc/fineapi-mima.md`（v2.1 审计修正版）的实现。
> 文档中的代号为 `credential-hub`，按项目命名要求落地为 **PassPal**。

## 它解决什么

```
① 打开网站 → 输密码 → 进工作台 → 点 Gemini → 直接复制密码 → 完成
② 打开 Gemini → 批量导入 → Ctrl+V → 自动解析 → 确认 → 100 个账号完成
```

其余功能都围绕这两条流程服务。

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.24+，`net/http` + ServeMux，零路由依赖 |
| 数据库 | SQLite（WAL + `synchronous=FULL`），`modernc.org/sqlite` 纯 Go 驱动 |
| 加密 | AES-256-GCM（标准库）+ Argon2id（`golang.org/x/crypto`） |
| 前端 | Vue 3 + TypeScript + Vite + Tailwind CSS v4 |
| 交付 | 单个二进制（内嵌前端）或 ~30MB Docker 镜像 |

没有 Redis、没有 PostgreSQL、没有 ORM、没有 Node 运行时依赖。

## 快速开始

### 1. 准备配置

```bash
cp .env.example .env
```

生成管理员密码 hash：

```bash
# Docker 之外（已构建二进制）
./passpal hash-password
# 或
go run ./cmd/passpal hash-password
```

生成数据加密密钥：

```bash
openssl rand -base64 32
```

把两者写入 `.env`：

```bash
ADMIN_PASSWORD_HASH='$argon2id$v=19$m=65536,t=3,p=4$...'
DATA_ENCRYPTION_KEY_V1=<32 字节 base64>
DATA_ENCRYPTION_KEY_CURRENT=1
APP_ORIGIN=https://accounts.example.com
```

> `ADMIN_PASSWORD_HASH` 内含 `$`，在 `.env` 里**必须用单引号包裹**，
> 否则会被 shell 或 compose 展开。

### 2. 启动

Docker：

```bash
docker compose up -d
```

裸二进制（更轻）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags="-s -w" -o passpal ./cmd/passpal
scp passpal server:/opt/passpal/
```

裸机部署建议配 systemd，并保持 `BIND_ADDR=127.0.0.1`，由 OpenResty 反代。

### 3. 验证

```bash
curl -s http://127.0.0.1:3000/api/health
# {"data":{"status":"ok"}}
```

## 配置项

| 变量 | 说明 |
|---|---|
| `APP_ENV` | `production` / `development`。生产必须 HTTPS |
| `APP_ORIGIN` | 必须与浏览器访问地址完全一致，用于同源校验 |
| `BIND_ADDR` | 监听地址，默认 `127.0.0.1`；容器内需设 `0.0.0.0` |
| `PORT` | 默认 3000 |
| `DATABASE_PATH` | SQLite 路径，默认 `./data/database.sqlite` |
| `BACKUP_DIR` | 备份目录，默认 `./backups` |
| `ADMIN_PASSWORD_HASH` | Argon2id hash，**生产必填** |
| `DATA_ENCRYPTION_KEY_Vn` | 32 字节 base64，支持多版本读取 |
| `DATA_ENCRYPTION_KEY_CURRENT` | 写入使用的密钥版本 |
| `SESSION_TTL_HOURS` | 会话有效期，默认 12 |
| `LOGIN_MAX_ATTEMPTS` | 同一来源失败上限，默认 5 |
| `LOGIN_LOCK_MINUTES` | 锁定时长，默认 15 |
| `TRUSTED_PROXY_CIDRS` | 只信任这些网段提供的转发头，默认空 |
| `BACKUP_RETAIN_DAYS` | 备份保留天数，默认 7 |

配置只从进程环境读取（compose `env_file` / systemd `EnvironmentFile`），
程序**不读取 `.env` 文件本身**，也不做任何 dotenv 语法解析。

运行期可调项（会话时长、失败上限、锁定时长、备份保留）在设置页修改后写入
`settings` 表，**库值优先于环境变量**；环境变量只在首次播种时生效。

## 命令

```bash
passpal serve             # 启动 HTTP 服务
passpal hash-password     # 生成管理员密码 hash
passpal auth-unlock       # 解除来源 IP 登录锁定：--source <IP>
passpal backup            # 立即执行一次在线备份
passpal restore           # 离线恢复：--from <备份绝对路径>
passpal version
```

`auth-unlock` 是本地 CLI，不提供 HTTP 解锁端点：共享出口 IP 被误锁时用它解除。

## 支持的导入格式

解析器按 **JSON → CSV → Key-Value → 特殊分隔符 → 普通文本** 的顺序自动识别。

| 形态 | 示例 |
|---|---|
| 四段分隔 | `email----password----backup_email----f2a` |
| 竖线分隔 | `email\|password\|backup_email\|f2a` |
| 空字段 | `email----password--------f2a----refresh_token`（连续分隔符 = 空列，会跳过） |
| Key-Value | `email: …` / `password: …` / `rt: …` |
| CSV | 带表头按列名映射，无表头按位置；用标准库处理引号与逗号 |
| JSON | 对象、数组，或 `{"accounts":[…]}` 包装 |
| 一行多列 | `主体 <TAB> 状态 <TAB> 时间 <TAB> {"client_id":…}` |

### 字段识别与归一化

- **邮箱** —— 正则匹配，取行内第一个命中；邮箱是账号主标识，缺失或非法即为无效行
- **密码** —— 字段名优先 → 分隔符位置 → 位置推断
- **辅助邮箱** —— 关键词（`backup` / `recovery` / `辅助邮箱`…）或第二个邮箱
- **F2A** —— 关键词 / `otpauth://` URI / 裸 base32。
  裸 base32 会**去掉空格并转大写**（`bstm 7x2h zbrs …` → `BSTM7X2HZBRS…`），
  否则复制出去无法直接使用；`otpauth://` 原样保留
- **JSON 凭证** —— 含 `client_id` / `client_secret` / `private_key` / `token_uri` 的对象
- **Refresh Token** —— JSON 里的 `refresh_token` 键、`rt` 键，或 `1//` 开头的长串
- **取码链接** —— Tab 元数据列里的「手机号\|URL」形态，如
  `13800000000\|https://card.example.com/api/sms/record?key=…`；
  只扫描第一列之后的列且要求整列就是链接，不会把 JSON 里的 `token_uri` 误抓进来

### 一行多列导出

有些工具会把状态、时间、凭证塞进同一行的额外列（Tab 分隔）：

```
email----password----TOTP <TAB> 订阅成功 <TAB> 时间 <TAB> … <TAB> {"client_id":…}
```

只把**第一列**当账号主体，并从其余列提取 JSON 凭证；状态、时间、手机号等元数据列被忽略，
不会污染 F2A 或密码。

> 导入**不保存原文**：批次表只存 HMAC 指纹、计数与结构摘要（行号 / 主邮箱 / 错误码）。

### 账号状态

在账号详情里可切换 `正常` / `异常`。列表用**色点 + 文字**双重标记，不只靠颜色区分，
并且可以**在列表里直接点击状态徽章切换**。

### 列表可见字段

计划文档 §11.6 的默认约束是「列表接口不下发任何明文」。本实现按实际使用需要做了
**有意识的放宽**，范围明确如下：

| 字段 | 列表 | 详情 |
|---|---|---|
| 邮箱 / 用户名 / 状态 / 标签 | 明文 | 明文 |
| F2A | **明文 + 一键复制** | 明文 |
| Refresh Token | **明文（截断显示）+ 一键复制** | 明文 |
| 取码链接 | **明文 + 可点击打开 + 一键复制** | 明文 |
| 创建时间 | 明文（`YYYY-MM-DD HH:mm`） | 明文（创建 / 更新时间） |
| 密码 | 掩码 + 一键复制 | 打开即明文 |
| JSON 凭证 | 不显示 | 打开即明文 |
| 备注 | 不显示 | 打开即明文 |

**为什么密码和 JSON 不下发列表**：这两项一旦整页下发，XSS、浏览器扩展、投屏都会直接暴露
全部凭据；而列表里的「复制」已经能满足使用。F2A / RT / 取码链接是需要在列表里核对和取用的
短值，所以按需求放开。

**数据库层面所有字段依然加密**（含 F2A / RT / 取码链接），列表里的明文是运行时解密的结果，
不是存储态 —— `sqlite3 database.sqlite` 依然什么都看不到。

### 时间字段显示

| 位置 | 字段 | 格式 |
|---|---|---|
| 列表表格（取码链接右侧一列） | `created_at` | `YYYY-MM-DD HH:mm`（本地时区），悬停显示含秒与 UTC 偏移的完整值 |
| 列表手机端卡片 | `created_at` | 同上 |
| 账号详情 Drawer 底部 | `created_at` / `updated_at` | 同上，标注「创建时间」「更新时间」 |
| 设置页备份列表 | 备份 `created_at` | 同上 |

**数据来源**：全部取自后端 `accounts.created_at` / `updated_at` / `backups.created_at`，
即数据库中的 Unix 秒（`INTEGER NOT NULL`）。单条创建与批量导入两条写入路径都在同一事务内
写入 `now`，因此**同一次创建的 `created_at` 与 `updated_at` 相等**；后续编辑只推进
`updated_at`，`created_at` 永不变动。

**格式统一**：三个页面共用 `web/src/utils/datetime.ts` 的 `formatDateTime()`，
避免各处各写一份导致格式漂移。空值 / 非法值统一显示 `—`。

### 批量导出 Refresh Token

在列表里勾选账号（支持全选本页），工具栏会出现「导出选中 RT」，
输出每行 `邮箱----refresh_token` 的纯文本，可直接复制或下载 `.txt`。
没有 RT 的账号自动跳过。该操作写审计 `BATCH_EXPORT_RT`。

## 安全设计

### 分层

```
管理员密码 ──Argon2id──▶ 只负责登录
DATA_ENCRYPTION_KEY ──AES-256-GCM──▶ 负责解密账号数据
```

两者完全分离，管理员密码绝不参与数据解密。

### 密文格式

```
[1B key_version][12B nonce][N B ciphertext][16B tag]
```

- 版本号以密文头部为唯一依据，支持多版本读取
- AAD 绑定 `passpal:account:<id>:<字段名>`，防止跨账号或跨字段搬运密文
- 未知版本、密文截断、tag 校验失败一律显式报错，不返回空串

### 关键约束

- **密码、JSON 凭证、备注不下发列表**，只返回 `has_*` 存在标记；
  F2A / Refresh Token / 取码链接按使用需要随列表下发（见上文「列表可见字段」）
- 明文（含 `refresh_token`）必须经 `POST /api/accounts/{id}/reveal` 逐字段获取，每次写审计 `REVEAL_SECRET`
- **导入不持久化原文**：只存 HMAC 指纹、计数与白名单结构摘要
- 备份用 `VACUUM INTO` 生成一致性快照，**不用 `cp`**（WAL 下会漏 `-wal`）
- 全部 API `Cache-Control: no-store`
- Cookie：`HttpOnly` + `Secure` + `SameSite=Lax`；写请求校验 CSRF 与 Origin
- 登录按来源锁定，Argon2 校验全局并发 1 且有频率保护

> ⚠️ **任一仍被数据引用的 `DATA_ENCRYPTION_KEY_Vn` 丢失，对应密文永久无法恢复。**
> 密钥副本不要和数据库放在同一台机器上。

## 备份与恢复

### 在线备份

设置页「立即备份」，或：

```bash
passpal backup
```

每次备份产出一个**自包含归档** `backup-<时间戳>.tar.gz`，内含三项：

| 条目 | 内容 |
|---|---|
| `database.sqlite` | `VACUUM INTO` 出的一致性快照（敏感字段仍是密文） |
| `keys.json` | 该快照加密时用到的**数据密钥**（按版本） |
| `manifest.json` | 格式版本、schema 版本、密钥版本、各表行数、库体积 |

流程：`VACUUM INTO` 生成未发布临时快照 → 打开**备份文件**执行
`PRAGMA integrity_check` 与 `foreign_key_check` → 连同密钥打包 → 通过后原子发布。
校验失败的快照不会出现在可下载清单里。

**为什么要自带密钥**：只存快照的话，恢复时还必须另外找到当时的 `DATA_ENCRYPTION_KEY_*`，
一旦密钥遗失或轮换过，备份就是一堆读不出的密文。打包进去后，**恢复只需要这一个文件**。

### 离线恢复

```bash
# 必须先停止服务
passpal restore --from /opt/passpal/backups/backup-20260101-120000.tar.gz
```

恢复契约：

1. 识别输入是归档还是旧版纯快照；归档解包到 data 同一文件系统的临时目录
2. 合并密钥：环境配置的 + 归档自带的。**同版本但内容冲突时拒绝恢复** ——
   那意味着归档与当前环境对不上，硬恢复只会得到读不出的数据
3. 只读校验候选库的 integrity、外键、表结构与迁移版本（拒绝未知触发器与更高版本）
4. 逐字段检查密文头部密钥版本并验证 GCM tag/AAD（覆盖全部加密字段）
5. 把当前 `database.sqlite` 及 WAL/SHM 复制到 `restore-rollback/<时间戳>/`
6. 清除候选库中的旧会话与来源锁定，写入当前管理员指纹
7. 关闭句柄后替换；启动后验证业务，失败可用回滚材料还原

**归档密钥与环境不一致时**，恢复会打印需要写进 `.env` 的
`DATA_ENCRYPTION_KEY_V<n>=...` 行 —— 照着改完重启服务即可，否则服务解密不了恢复后的数据。

旧版 `backup-*.sqlite` 纯快照仍可恢复，此时需要环境里配置正确的密钥。

恢复**不开放 HTTP 端点**，不做网页上传。

## 开发

```bash
make web        # 构建前端
make build      # 构建二进制
make check      # vet + 单元测试 + 构建
make test       # Go 单元测试
```

前端开发时可以用 Vite 的代理：

```bash
cd web && npm run dev     # 代理 /api 到 127.0.0.1:3000
```

### 目录结构

```
passpal/
├── cmd/passpal/          # 入口与子命令
├── internal/
│   ├── config/           # 环境读取与严格校验
│   ├── database/         # 连接、PRAGMA、迁移、settings
│   ├── cryptoutil/       # AES-GCM / Argon2id / HMAC 派生
│   ├── auth/             # 登录、会话、来源锁定、限速
│   ├── account/          # 账号 CRUD + reveal
│   ├── project/ tag/     # 项目与标签
│   ├── importer/         # 解析器链、草稿、去重、幂等提交
│   ├── search/ export/ backup/
│   ├── tasklock/         # 导入与备份互斥
│   └── httpapi/          # 路由、中间件、处理器
├── migrations/           # SQL 迁移（内嵌进二进制）
├── web/                  # 前端源码与构建产物
├── embed.go              # go:embed 前端产物与迁移
├── Dockerfile
└── docker-compose.yml
```

### 改管理员密码

```bash
./passpal hash-password        # 生成新 hash
# 更新 .env 中的 ADMIN_PASSWORD_HASH，然后重启服务
```

启动时会比较 `SHA-256(ADMIN_PASSWORD_HASH)` 与库中的指纹；变化时**撤销全部会话**。

## 验收

`scripts/acceptance.py` 是端到端验收脚本，直接打真实 HTTP 接口（无 mock），覆盖
登录与来源锁定、CSRF/Origin、项目/标签/账号 CRUD、reveal、批量导入与幂等、
搜索、备份，以及「直接读 SQLite 看不到明文」这条硬要求。

```bash
PP_DB=/path/to/database.sqlite python scripts/acceptance.py
```

## 不在 MVP 范围内

多用户 / RBAC / OAuth、Redis / PostgreSQL、WebSocket、浏览器插件、手机 App、
自动登录第三方网站、加密备注搜索、网页数据库恢复、导入原文留档、
后台密钥轮换。理由：显著增加复杂度，却不提升「管理账号」的核心体验。
