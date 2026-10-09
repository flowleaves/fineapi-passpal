package cryptoutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RandomBytes 返回 n 字节密码学安全随机数。
func RandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("随机字节长度必须为正数，实际 %d", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("读取随机源失败：%w", err)
	}
	return b, nil
}

// RandomHex 返回 n 字节随机数的十六进制编码（长度 2n）。
func RandomHex(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
