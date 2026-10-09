-- 0003_add_refresh_token.sql
--
-- 新增 refresh_token 加密列。
--
-- 背景：真实导入数据里常见 5 段格式
--   email----password----<空 backup>----TOTP----refresh_token
-- 以及 JSON 凭证里的 refresh_token 键。它和密码同级敏感，
-- 必须走与其它秘密完全相同的 AES-GCM 入口与 AAD 绑定。
--
-- 只加列，不动既有数据；旧行的该列为 NULL，读出来就是「未设置」。

ALTER TABLE accounts ADD COLUMN refresh_token_encrypted BLOB;
