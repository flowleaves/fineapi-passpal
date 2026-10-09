-- 0002_seed_settings.sql
--
-- 运行期可变配置的播种策略（工作区铁律：库值 > 环境变量）：
--
--   * 与进程环境无关的项（如 last_backup_at、schema_seeded）在此播种。
--   * 由环境变量提供初值的项（session_ttl_hours / login_max_attempts /
--     login_lock_minutes / backup_retain_days）在服务启动时由
--     internal/database.SeedSettings 处理：仅当 key 不存在时写入环境值，
--     之后一律以库值优先，环境变量不再覆盖。
--
-- 密钥与当前密钥版本始终只由环境提供，不写入 settings，
-- 避免库值与环境版本冲突。

INSERT OR IGNORE INTO settings (key, value, updated_at, updated_by)
VALUES ('schema_seeded', '1', strftime('%s', 'now'), 'migration');

INSERT OR IGNORE INTO settings (key, value, updated_at, updated_by)
VALUES ('last_backup_at', NULL, strftime('%s', 'now'), 'migration');
