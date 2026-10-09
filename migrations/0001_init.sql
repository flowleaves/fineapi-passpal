-- 0001_init.sql
-- PassPal 初始化：9 张业务表 + 索引。
-- 时间统一用 Unix 秒（INTEGER）。软删除统一用 deleted_at。
-- 迁移元数据表 schema_migrations 由迁移器在应用本文件之前创建。

-- ─────────────────────────────────────────────────────────────
-- projects
-- ─────────────────────────────────────────────────────────────
CREATE TABLE projects (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT    NOT NULL UNIQUE,
  description TEXT,
  icon        TEXT,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  deleted_at  INTEGER,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- ─────────────────────────────────────────────────────────────
-- tags —— 标签必须属于某个项目
-- ─────────────────────────────────────────────────────────────
CREATE TABLE tags (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name        TEXT    NOT NULL,
  color       TEXT,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  deleted_at  INTEGER,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE (project_id, name)
);

-- ─────────────────────────────────────────────────────────────
-- import_batches —— 放在 accounts 之前，便于阅读外键方向
-- 只存幂等 key 哈希、提交参数哈希、计数、原文 HMAC 指纹与结构摘要。
-- 没有 raw_source / raw_source_preview / raw_source_encrypted。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE import_batches (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id          INTEGER NOT NULL REFERENCES projects(id),
  tag_id              INTEGER REFERENCES tags(id),
  commit_key          TEXT    NOT NULL UNIQUE,
  request_hash        TEXT    NOT NULL,
  preview_revision    INTEGER NOT NULL,

  total_count         INTEGER NOT NULL DEFAULT 0,
  success_count       INTEGER NOT NULL DEFAULT 0,
  failed_count        INTEGER NOT NULL DEFAULT 0,
  duplicate_count     INTEGER NOT NULL DEFAULT 0,
  skipped_count       INTEGER NOT NULL DEFAULT 0,
  updated_count       INTEGER NOT NULL DEFAULT 0,

  raw_source_hash     TEXT,
  summary_json        TEXT,

  created_at          INTEGER NOT NULL
);

-- ─────────────────────────────────────────────────────────────
-- accounts
-- 秘密字段全部为密文 BLOB；密钥版本以每个 BLOB 头部为准。
-- 不建 (project_id, email_normalized) 唯一约束：允许用户明确选择「全部保留」。
-- ─────────────────────────────────────────────────────────────
CREATE TABLE accounts (
  id                        INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id                INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,

  email                     TEXT    NOT NULL,
  email_normalized          TEXT    NOT NULL,
  username                  TEXT,

  password_encrypted        BLOB,
  backup_email_encrypted    BLOB,
  f2a_encrypted             BLOB,
  credential_json_encrypted BLOB,
  notes_encrypted           BLOB,

  extra_json                TEXT,
  status                    TEXT    NOT NULL DEFAULT 'normal',

  source_import_id          INTEGER REFERENCES import_batches(id),
  revision                  INTEGER NOT NULL DEFAULT 1,

  created_at                INTEGER NOT NULL,
  updated_at                INTEGER NOT NULL,
  last_used_at              INTEGER,
  deleted_at                INTEGER
);

-- ─────────────────────────────────────────────────────────────
-- account_tags —— 多对多
-- ─────────────────────────────────────────────────────────────
CREATE TABLE account_tags (
  account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  tag_id     INTEGER NOT NULL REFERENCES tags(id)     ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (account_id, tag_id)
);

-- ─────────────────────────────────────────────────────────────
-- sessions —— 会话落 SQLite，重启后仍可验证
-- ─────────────────────────────────────────────────────────────
CREATE TABLE sessions (
  id           TEXT    PRIMARY KEY,   -- SHA-256(cookie token) 的 hex
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip           TEXT,
  user_agent   TEXT
);

-- ─────────────────────────────────────────────────────────────
-- auth_state —— 按来源的登录失败计数与锁定
-- source_key = HMAC(专用派生密钥, 已验证来源 IP)，不存明文 IP
-- ─────────────────────────────────────────────────────────────
CREATE TABLE auth_state (
  source_key        TEXT    PRIMARY KEY,
  window_started_at INTEGER NOT NULL,
  failed_attempts   INTEGER NOT NULL DEFAULT 0,
  locked_until      INTEGER,
  last_failed_at    INTEGER,
  expires_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL
);

-- ─────────────────────────────────────────────────────────────
-- audit_logs —— 只记动作，不记任何敏感内容
-- ─────────────────────────────────────────────────────────────
CREATE TABLE audit_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  action      TEXT    NOT NULL,
  target_type TEXT,
  target_id   INTEGER,
  target_ref  TEXT,
  field       TEXT,
  ip          TEXT,
  created_at  INTEGER NOT NULL
);

-- ─────────────────────────────────────────────────────────────
-- settings —— 运行期可变配置；库值 > 环境变量
-- ─────────────────────────────────────────────────────────────
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT,
  updated_at INTEGER NOT NULL,
  updated_by TEXT
);

-- ─────────────────────────────────────────────────────────────
-- 索引（§5.10）
-- ─────────────────────────────────────────────────────────────
CREATE INDEX idx_accounts_project_email
  ON accounts(project_id, email_normalized) WHERE deleted_at IS NULL;
CREATE INDEX idx_accounts_project_created
  ON accounts(project_id, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_accounts_deleted
  ON accounts(deleted_at, id);
CREATE INDEX idx_tags_project
  ON tags(project_id);
CREATE INDEX idx_account_tags_t
  ON account_tags(tag_id, account_id);

-- 文档未列但查询路径需要的补充索引
CREATE INDEX idx_projects_deleted
  ON projects(deleted_at, sort_order, id);
CREATE INDEX idx_tags_deleted
  ON tags(deleted_at, project_id, sort_order, id);
CREATE INDEX idx_accounts_project_lastused
  ON accounts(project_id, last_used_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_accounts_project_updated
  ON accounts(project_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_audit_created
  ON audit_logs(created_at DESC, id DESC);
CREATE INDEX idx_import_batches_created
  ON import_batches(created_at DESC, id DESC);

-- sessions / auth_state 的过期清理索引
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
CREATE INDEX idx_auth_state_expires ON auth_state(expires_at);
