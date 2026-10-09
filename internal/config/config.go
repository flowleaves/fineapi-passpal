// Package config 读取并严格校验进程环境。
//
// 生产配置只来自进程环境（compose env_file / systemd EnvironmentFile）。
// 本包不实现 dotenv 语法，尤其不对 Argon2 hash 内的 '$' 做任何展开。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env 取值。
const (
	EnvProduction  = "production"
	EnvDevelopment = "development"
)

// Config 是服务运行所需的全部配置。
type Config struct {
	AppEnv       string
	AppOrigin    string // 形如 https://accounts.example.com，必须与浏览器访问地址一致
	BindAddr     string // 监听地址；默认 127.0.0.1，容器内需显式设为 0.0.0.0
	Port         int
	DatabasePath string
	BackupDir    string

	AdminPasswordHash string

	// EncryptionKeys 为 key_version -> 32 字节密钥；CurrentKeyVersion 是写入时使用的版本。
	EncryptionKeys    map[int][]byte
	CurrentKeyVersion int

	SessionTTL      time.Duration
	LoginMaxAttempt int
	LoginLockFor    time.Duration
	BackupRetain    time.Duration

	TrustedProxyCIDRs []*net.IPNet
}

// IsProduction 报告是否运行在生产模式。
func (c *Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// CookieSecure 报告会话 cookie 是否应带 Secure 标志。
//
// 依据是 APP_ORIGIN 的**实际 scheme**，而不是 APP_ENV：
// Secure cookie 只在 HTTPS 下才会被浏览器回传，若在明文 HTTP 站点上误加该标志，
// 浏览器会直接丢弃 cookie —— 表现为「登录返回 200，但下一个请求就报会话无效」。
// 所以只有当访问地址确实是 https:// 时才加。
//
// APP_ORIGIN 是已经过校验的规范访问地址（必须与浏览器地址一致），
// 反代终止 TLS 的场景下它同样会被配成 https://，因此这里判断是准确的。
func (c *Config) CookieSecure() bool {
	return strings.HasPrefix(strings.ToLower(c.AppOrigin), "https://")
}

// UsesPlainHTTP 报告规范访问地址是否为明文 HTTP。
// 用于启动时给出安全告警（凭据会以明文经网络传输）。
func (c *Config) UsesPlainHTTP() bool {
	return strings.HasPrefix(strings.ToLower(c.AppOrigin), "http://")
}

// Load 从进程环境加载配置并做完整校验。
// 生产环境缺少合法管理员 hash 或数据密钥时返回错误，调用方必须终止启动。
func Load() (*Config, error) {
	c := &Config{
		AppEnv:       strings.ToLower(strings.TrimSpace(getenv("APP_ENV", EnvProduction))),
		AppOrigin:    strings.TrimRight(strings.TrimSpace(os.Getenv("APP_ORIGIN")), "/"),
		BindAddr:     getenv("BIND_ADDR", "127.0.0.1"),
		DatabasePath: getenv("DATABASE_PATH", "./data/database.sqlite"),
		BackupDir:    getenv("BACKUP_DIR", "./backups"),
	}

	if c.AppEnv != EnvProduction && c.AppEnv != EnvDevelopment {
		return nil, fmt.Errorf("APP_ENV 必须是 %q 或 %q，实际为 %q", EnvProduction, EnvDevelopment, c.AppEnv)
	}

	port, err := strconv.Atoi(getenv("PORT", "3000"))
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("PORT 非法：%q", os.Getenv("PORT"))
	}
	c.Port = port

	if c.AppOrigin == "" {
		if c.IsProduction() {
			return nil, errors.New("生产环境必须设置 APP_ORIGIN（必须与浏览器访问地址完全一致）")
		}
		c.AppOrigin = fmt.Sprintf("http://127.0.0.1:%d", c.Port)
	}
	if err := validateOrigin(c.AppOrigin); err != nil {
		return nil, err
	}

	// 管理员密码 hash：生产必填且格式必须合法。
	c.AdminPasswordHash = strings.TrimSpace(os.Getenv("ADMIN_PASSWORD_HASH"))
	if c.AdminPasswordHash == "" {
		if c.IsProduction() {
			return nil, errors.New("生产环境必须设置 ADMIN_PASSWORD_HASH（用 `passpal hash-password` 生成）")
		}
	} else if err := validateArgon2idHash(c.AdminPasswordHash); err != nil {
		// 不回显 hash 内容，只报告格式问题。
		return nil, fmt.Errorf("ADMIN_PASSWORD_HASH 格式非法：%w", err)
	}

	// 数据加密密钥：支持多版本读取，写入使用 CURRENT。
	keys, err := loadEncryptionKeys()
	if err != nil {
		return nil, err
	}
	c.EncryptionKeys = keys

	cur, err := strconv.Atoi(getenv("DATA_ENCRYPTION_KEY_CURRENT", "1"))
	if err != nil || cur < 1 || cur > 255 {
		return nil, errors.New("DATA_ENCRYPTION_KEY_CURRENT 必须是 1..255 的整数")
	}
	if _, ok := keys[cur]; !ok {
		return nil, fmt.Errorf("DATA_ENCRYPTION_KEY_CURRENT=%d 指向的密钥版本未配置", cur)
	}
	c.CurrentKeyVersion = cur

	ttlHours, err := positiveInt("SESSION_TTL_HOURS", 12, 1, 24*365)
	if err != nil {
		return nil, err
	}
	c.SessionTTL = time.Duration(ttlHours) * time.Hour

	c.LoginMaxAttempt, err = positiveInt("LOGIN_MAX_ATTEMPTS", 5, 1, 100)
	if err != nil {
		return nil, err
	}
	lockMin, err := positiveInt("LOGIN_LOCK_MINUTES", 15, 1, 24*60)
	if err != nil {
		return nil, err
	}
	c.LoginLockFor = time.Duration(lockMin) * time.Minute

	retainDays, err := positiveInt("BACKUP_RETAIN_DAYS", 7, 1, 3650)
	if err != nil {
		return nil, err
	}
	c.BackupRetain = time.Duration(retainDays) * 24 * time.Hour

	cidrs, err := parseCIDRs(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return nil, err
	}
	c.TrustedProxyCIDRs = cidrs

	return c, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func positiveInt(key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是整数，实际为 %q", key, raw)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s 必须在 %d..%d 之间，实际为 %d", key, min, max, n)
	}
	return n, nil
}

func validateOrigin(origin string) error {
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("APP_ORIGIN 不是合法 URL：%w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("APP_ORIGIN 的 scheme 必须是 http 或 https")
	}
	if u.Host == "" {
		return errors.New("APP_ORIGIN 缺少 host")
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("APP_ORIGIN 只能是 scheme://host[:port]，不能带路径、查询或片段")
	}
	return nil
}

func parseCIDRs(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS 含非法地址：%q（请写成 CIDR，例如 172.16.0.0/12）", part)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			part = fmt.Sprintf("%s/%d", part, bits)
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS 含非法 CIDR：%q", part)
		}
		out = append(out, n)
	}
	return out, nil
}

// loadEncryptionKeys 扫描 DATA_ENCRYPTION_KEY_V1..V255。
func loadEncryptionKeys() (map[int][]byte, error) {
	keys := make(map[int][]byte)
	for v := 1; v <= 255; v++ {
		raw := strings.TrimSpace(os.Getenv(fmt.Sprintf("DATA_ENCRYPTION_KEY_V%d", v)))
		if raw == "" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("DATA_ENCRYPTION_KEY_V%d 不是合法 base64", v)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("DATA_ENCRYPTION_KEY_V%d 解码后必须是 32 字节，实际 %d 字节", v, len(key))
		}
		keys[v] = key
	}
	if len(keys) == 0 {
		return nil, errors.New("必须至少配置一个 DATA_ENCRYPTION_KEY_Vn（32 字节 base64）")
	}
	return keys, nil
}

// validateArgon2idHash 只校验编码结构，不做任何明文比较。
func validateArgon2idHash(h string) error {
	if !strings.HasPrefix(h, "$argon2id$") {
		return errors.New("必须以 $argon2id$ 开头")
	}
	parts := strings.Split(h, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 6 {
		return errors.New("段数不正确，期望 $argon2id$v=..$m=..,t=..,p=..$salt$hash")
	}
	if parts[2] != "v=19" {
		return fmt.Errorf("仅支持 argon2 版本 v=19，实际为 %q", parts[2])
	}
	if !strings.HasPrefix(parts[3], "m=") {
		return errors.New("参数段必须以 m= 开头")
	}
	if _, err := base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return errors.New("salt 不是合法 base64")
	}
	if _, err := base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return errors.New("hash 不是合法 base64")
	}
	return nil
}
