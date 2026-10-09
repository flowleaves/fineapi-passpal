// Package cryptoutil 提供 PassPal 的全部密码学原语。
//
// 设计约束（见 doc/fineapi-mima.md §7）：
//   - 账号凭据使用 AES-256-GCM，密文自描述版本，支持多密钥版本读取；
//   - 管理员密码使用 Argon2id，与数据加密密钥完全分离；
//   - 任何未知版本、截断、tag 校验失败都必须显式报错，绝不返回空串冒充成功。
package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// 密文布局：[1B key_version][12B nonce][N B ciphertext][16B tag]
const (
	VersionLen = 1
	NonceLen   = 12
	TagLen     = 16

	// KeyLen 是 AES-256 密钥长度。
	KeyLen = 32
	// MaxKeyVersion 对应 1 字节版本号的上限。
	MaxKeyVersion = 255
)

// 敏感字段枚举。AAD 与审计都用这里的固定值，不接受任意字符串。
const (
	FieldPassword       = "password"
	FieldBackupEmail    = "backup_email"
	FieldF2A            = "f2a"
	FieldCredentialJSON = "credential_json"
	FieldNotes          = "notes"
	FieldRefreshToken   = "refresh_token"
	FieldSMSLink        = "sms_link"
)

// AllSecretFields 列出所有可 reveal 的加密字段，顺序即 API 文档顺序。
var AllSecretFields = []string{
	FieldPassword,
	FieldBackupEmail,
	FieldF2A,
	FieldCredentialJSON,
	FieldNotes,
	FieldRefreshToken,
	FieldSMSLink,
}

// IsSecretField 判断字段名是否属于加密字段枚举。
func IsSecretField(f string) bool {
	for _, s := range AllSecretFields {
		if s == f {
			return true
		}
	}
	return false
}

var (
	// ErrCiphertextTruncated 表示密文长度不足以容纳头部与 tag。
	ErrCiphertextTruncated = errors.New("密文被截断或长度非法")
	// ErrUnknownKeyVersion 表示密文头部引用了未配置的密钥版本。
	ErrUnknownKeyVersion = errors.New("密文引用了未知的密钥版本")
	// ErrAuthFailed 表示 GCM 认证失败（密钥错误、AAD 不匹配或密文被篡改）。
	ErrAuthFailed = errors.New("密文认证失败")
)

// Cipher 持有全部可用版本的数据密钥，并按版本读写密文。
type Cipher struct {
	keys    map[int][]byte
	current int
}

// NewCipher 校验并构造 Cipher。keys 为 版本 -> 32 字节密钥。
func NewCipher(keys map[int][]byte, current int) (*Cipher, error) {
	if len(keys) == 0 {
		return nil, errors.New("没有可用的数据加密密钥")
	}
	for v, k := range keys {
		if v < 1 || v > MaxKeyVersion {
			return nil, fmt.Errorf("密钥版本 %d 超出 1..%d", v, MaxKeyVersion)
		}
		if len(k) != KeyLen {
			return nil, fmt.Errorf("密钥版本 %d 长度不是 %d 字节", v, KeyLen)
		}
	}
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("当前密钥版本 %d 未配置", current)
	}
	// 复制一份，避免调用方后续修改。
	cp := make(map[int][]byte, len(keys))
	for v, k := range keys {
		b := make([]byte, len(k))
		copy(b, k)
		cp[v] = b
	}
	return &Cipher{keys: cp, current: current}, nil
}

// CurrentVersion 返回写入时使用的密钥版本。
func (c *Cipher) CurrentVersion() int { return c.current }

// Versions 返回全部已配置的密钥版本。
func (c *Cipher) Versions() []int {
	out := make([]int, 0, len(c.keys))
	for v := range c.keys {
		out = append(out, v)
	}
	return out
}

// HasVersion 报告某版本密钥是否可用（恢复流程预检用）。
func (c *Cipher) HasVersion(v int) bool {
	_, ok := c.keys[v]
	return ok
}

// Encrypt 使用 CURRENT 版本加密，返回自描述 BLOB。
func (c *Cipher) Encrypt(plaintext, aad []byte) ([]byte, error) {
	gcm, err := c.gcm(c.current)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce 失败：%w", err)
	}
	out := make([]byte, 0, VersionLen+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, byte(c.current))
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, aad)
	return out, nil
}

// Decrypt 依据密文头部版本选择密钥解密。
func (c *Cipher) Decrypt(blob, aad []byte) ([]byte, error) {
	if len(blob) < VersionLen+NonceLen+TagLen {
		return nil, ErrCiphertextTruncated
	}
	ver := int(blob[0])
	key, ok := c.keys[ver]
	if !ok {
		return nil, fmt.Errorf("%w：%d", ErrUnknownKeyVersion, ver)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := blob[VersionLen : VersionLen+NonceLen]
	ct := blob[VersionLen+NonceLen:]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrAuthFailed
	}
	return pt, nil
}

// KeyVersionOf 读取密文头部声明的版本，不做解密。
func KeyVersionOf(blob []byte) (int, error) {
	if len(blob) < VersionLen+NonceLen+TagLen {
		return 0, ErrCiphertextTruncated
	}
	return int(blob[0]), nil
}

func (c *Cipher) gcm(version int) (cipher.AEAD, error) {
	key, ok := c.keys[version]
	if !ok {
		return nil, fmt.Errorf("%w：%d", ErrUnknownKeyVersion, version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// AccountAAD 构造无歧义的附加认证数据，防止跨账号或跨字段搬运密文。
// 形如 passpal:account:<十进制 id>:<固定字段枚举>
func AccountAAD(accountID int64, field string) []byte {
	return []byte("passpal:account:" + itoa(accountID) + ":" + field)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
