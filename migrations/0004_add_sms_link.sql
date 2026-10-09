-- 0004_add_sms_link.sql
--
-- 新增「取码链接」加密列。
--
-- 真实导出里常见这种形态（放在 Tab 元数据列中）：
--   13800000000|https://card.example.com/api/sms/record?key=xxxx
-- 手机号 + 取码接口地址。它等价于「随时能拿到登录验证码」，
-- 敏感度与 F2A 同级，必须走同样的 AES-GCM 入口与 AAD 绑定。
--
-- 只加列，不动既有数据；旧行该列为 NULL，读出来就是「未设置」。

ALTER TABLE accounts ADD COLUMN sms_link_encrypted BLOB;
