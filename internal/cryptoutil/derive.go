package cryptoutil

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// 派生用途。三个用途互不复用，且都不参与凭据解密（见 doc §7.4）。
const (
	// PurposeSourceFingerprint 用于把已验证的来源 IP 变成不可逆的 source_key。
	PurposeSourceFingerprint = "source-fingerprint"
	// PurposeRawFingerprint 用于对导入原文做 HMAC 指纹。
	PurposeRawFingerprint = "raw-fingerprint"
	// PurposeCSRF 用于对会话 token 签名，生成绑定会话的 CSRF 值。
	PurposeCSRF = "csrf"
)

// DeriveKey 从主密钥派生固定用途的子密钥：HMAC-SHA256(master, "passpal:derive:"+purpose)。
func DeriveKey(master []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("passpal:derive:"))
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

// HMACHex 返回 HMAC-SHA256(key, data) 的十六进制编码。
func HMACHex(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// HMACEqual 以常量时间比较两个十六进制指纹。
func HMACEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// SHA256Hex 返回 SHA-256 的十六进制编码，用于非敏感的完整性引用。
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqualString 以常量时间比较两个字符串。
func ConstantTimeEqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
