package cryptoutil

import (
	"bytes"
	"errors"
	"testing"
)

func testCipher(t *testing.T, keys map[int][]byte, current int) *Cipher {
	t.Helper()
	c, err := NewCipher(keys, current)
	if err != nil {
		t.Fatalf("NewCipher 失败：%v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{7}, KeyLen)}, 1)
	aad := AccountAAD(42, FieldPassword)
	plain := []byte("SuperSecret!123")

	blob, err := c.Encrypt(plain, aad)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if bytes.Contains(blob, plain) {
		t.Fatal("密文中不应出现明文")
	}
	got, err := c.Decrypt(blob, aad)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("往返结果不一致：%q", got)
	}
}

func TestNonceIsFresh(t *testing.T) {
	c := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{3}, KeyLen)}, 1)
	aad := AccountAAD(1, FieldPassword)
	a, _ := c.Encrypt([]byte("same"), aad)
	b, _ := c.Encrypt([]byte("same"), aad)
	if bytes.Equal(a, b) {
		t.Fatal("相同明文两次加密结果不应相同（nonce 必须每次重新生成）")
	}
}

func TestAADBindingPreventsCrossFieldAndCrossAccount(t *testing.T) {
	c := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{9}, KeyLen)}, 1)
	blob, err := c.Encrypt([]byte("secret"), AccountAAD(1, FieldPassword))
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}

	if _, err := c.Decrypt(blob, AccountAAD(2, FieldPassword)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("跨账号解密应当失败，实际 %v", err)
	}
	if _, err := c.Decrypt(blob, AccountAAD(1, FieldF2A)); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("跨字段解密应当失败，实际 %v", err)
	}
}

func TestTamperedCiphertextRejected(t *testing.T) {
	c := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{5}, KeyLen)}, 1)
	aad := AccountAAD(7, FieldNotes)
	blob, _ := c.Encrypt([]byte("hello world"), aad)

	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := c.Decrypt(tampered, aad); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("被篡改的密文应当认证失败，实际 %v", err)
	}
}

func TestTruncatedRejected(t *testing.T) {
	c := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{5}, KeyLen)}, 1)
	if _, err := c.Decrypt([]byte{1, 2, 3}, AccountAAD(1, FieldPassword)); !errors.Is(err, ErrCiphertextTruncated) {
		t.Fatalf("截断密文应当报错，实际 %v", err)
	}
}

func TestUnknownKeyVersionRejected(t *testing.T) {
	enc := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{1}, KeyLen)}, 1)
	blob, _ := enc.Encrypt([]byte("x"), AccountAAD(1, FieldPassword))

	// 只有版本 2 的解密器无法读取版本 1 的密文
	other := testCipher(t, map[int][]byte{2: bytes.Repeat([]byte{2}, KeyLen)}, 2)
	if _, err := other.Decrypt(blob, AccountAAD(1, FieldPassword)); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Fatalf("未知版本应当报错，实际 %v", err)
	}
}

func TestMultiVersionRead(t *testing.T) {
	// 用版本 1 写入
	v1 := testCipher(t, map[int][]byte{1: bytes.Repeat([]byte{1}, KeyLen)}, 1)
	old, _ := v1.Encrypt([]byte("legacy"), AccountAAD(3, FieldPassword))

	// 轮换后仍能读旧版本，新写入使用版本 2
	rotated := testCipher(t, map[int][]byte{
		1: bytes.Repeat([]byte{1}, KeyLen),
		2: bytes.Repeat([]byte{2}, KeyLen),
	}, 2)

	got, err := rotated.Decrypt(old, AccountAAD(3, FieldPassword))
	if err != nil || string(got) != "legacy" {
		t.Fatalf("多版本读取失败：%q %v", got, err)
	}
	fresh, _ := rotated.Encrypt([]byte("new"), AccountAAD(3, FieldPassword))
	ver, err := KeyVersionOf(fresh)
	if err != nil || ver != 2 {
		t.Fatalf("新写入应使用当前版本 2，实际 %d %v", ver, err)
	}
}

func TestNewCipherRejectsBadKeys(t *testing.T) {
	if _, err := NewCipher(map[int][]byte{1: []byte("short")}, 1); err == nil {
		t.Fatal("长度错误的密钥应当被拒绝")
	}
	if _, err := NewCipher(map[int][]byte{1: bytes.Repeat([]byte{1}, KeyLen)}, 2); err == nil {
		t.Fatal("当前版本未配置应当被拒绝")
	}
	if _, err := NewCipher(map[int][]byte{}, 1); err == nil {
		t.Fatal("空密钥集合应当被拒绝")
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	params := Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}
	hash, err := HashPassword("correct horse battery staple", params)
	if err != nil {
		t.Fatalf("生成 hash 失败：%v", err)
	}
	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Fatalf("正确密码应当通过：%v", err)
	}
	bad, err := VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatalf("校验错误密码不应报错：%v", err)
	}
	if bad {
		t.Fatal("错误密码不应通过")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "not-a-hash"); err == nil {
		t.Fatal("非法 hash 应当报错")
	}
}
