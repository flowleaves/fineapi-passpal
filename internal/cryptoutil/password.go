package cryptoutil

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params 是 Argon2id 的校验参数。
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// DefaultArgon2Params 对应 doc §6.1 建议值：time=3, memory=64MiB, threads=4,
// keyLen=32, saltLen=16。生产不得为压低内存而降低这些参数。
var DefaultArgon2Params = Argon2Params{
	Memory:  64 * 1024,
	Time:    3,
	Threads: 4,
	KeyLen:  32,
	SaltLen: 16,
}

// 校验参数上限，防止被篡改的 hash 触发超大内存分配。
const (
	maxArgon2MemoryKiB = 256 * 1024 // 256 MiB
	maxArgon2Time      = 10
	maxArgon2Threads   = 16
)

// HashPassword 生成 `$argon2id$v=19$m=..,t=..,p=..$salt$hash` 形式的编码串。
func HashPassword(password string, p Argon2Params) (string, error) {
	if password == "" {
		return "", errors.New("密码不能为空")
	}
	if err := p.validate(); err != nil {
		return "", err
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成 salt 失败：%w", err)
	}
	sum := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

// VerifyPassword 以常量时间比较明文与编码串。
// 任何解析错误都返回 (false, err)，调用方不得把它当成“密码错误”之外的成功路径。
func VerifyPassword(password, encoded string) (bool, error) {
	p, salt, want, err := ParseArgon2Hash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ParseArgon2Hash 解析编码串并返回参数、salt 与期望的 hash。
func ParseArgon2Hash(encoded string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return p, nil, nil, errors.New("argon2 编码段数不正确")
	}
	if parts[1] != "argon2id" {
		return p, nil, nil, fmt.Errorf("只支持 argon2id，实际为 %q", parts[1])
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, errors.New("argon2 版本段非法")
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("argon2 版本不匹配：编码 %d，运行库 %d", version, argon2.Version)
	}
	var mem, t uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &par); err != nil {
		return p, nil, nil, errors.New("argon2 参数段非法")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, errors.New("salt 不是合法 base64")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, errors.New("hash 不是合法 base64")
	}
	p = Argon2Params{Memory: mem, Time: t, Threads: par, KeyLen: uint32(len(want)), SaltLen: uint32(len(salt))}
	if err := p.validate(); err != nil {
		return p, nil, nil, err
	}
	return p, salt, want, nil
}

func (p Argon2Params) validate() error {
	if p.Memory < 8 || p.Memory > maxArgon2MemoryKiB {
		return fmt.Errorf("argon2 内存参数 %d KiB 超出允许范围", p.Memory)
	}
	if p.Time < 1 || p.Time > maxArgon2Time {
		return fmt.Errorf("argon2 迭代次数 %d 超出允许范围", p.Time)
	}
	if p.Threads < 1 || p.Threads > maxArgon2Threads {
		return fmt.Errorf("argon2 并行度 %d 超出允许范围", p.Threads)
	}
	if p.KeyLen < 16 || p.KeyLen > 64 {
		return fmt.Errorf("argon2 输出长度 %d 超出允许范围", p.KeyLen)
	}
	if p.SaltLen < 8 || p.SaltLen > 64 {
		return fmt.Errorf("argon2 salt 长度 %d 超出允许范围", p.SaltLen)
	}
	return nil
}
