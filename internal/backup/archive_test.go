package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "src.sqlite")
	content := []byte("fake-sqlite-content-0123456789-abcdefghij")
	if err := os.WriteFile(dbPath, content, 0o600); err != nil {
		t.Fatal(err)
	}

	keys := map[int][]byte{1: bytes.Repeat([]byte{0xAA}, 32)}
	counts := map[string]int{"projects": 2, "tags": 3, "accounts": 5}
	dst := filepath.Join(dir, namePrefix+"20261009-190000"+ArchiveSuffix)

	if err := writeArchive(dst, dbPath, keys, 1, counts, 4); err != nil {
		t.Fatalf("writeArchive 失败：%v", err)
	}

	got, err := extractArchive(dst, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("extractArchive 失败：%v", err)
	}

	// 数据库内容必须逐字节一致。
	back, err := os.ReadFile(got.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, content) {
		t.Fatalf("数据库内容不一致：%q", back)
	}

	// 密钥必须能还原出来 —— 这正是「不依赖外部密钥即可恢复」的前提。
	if !bytes.Equal(got.Keys[1], keys[1]) {
		t.Fatalf("密钥不一致：%x", got.Keys[1])
	}

	if got.Manifest == nil {
		t.Fatal("清单缺失")
	}
	if got.Manifest.Format != ArchiveFormat || got.Manifest.FormatVersion != ArchiveFormatVersion {
		t.Fatalf("清单格式标识不对：%+v", got.Manifest)
	}
	if got.Manifest.SchemaVersion != 4 {
		t.Fatalf("schema 版本不对：%d", got.Manifest.SchemaVersion)
	}
	if got.Manifest.Counts["accounts"] != 5 {
		t.Fatalf("内容统计不对：%v", got.Manifest.Counts)
	}
	sum := sha256.Sum256(content)
	if got.Manifest.DBSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("清单里的 sha256 与实际内容不符：%s", got.Manifest.DBSHA256)
	}
	if got.Manifest.DBSize != int64(len(content)) {
		t.Fatalf("清单里的体积不对：%d", got.Manifest.DBSize)
	}
}

func TestArchiveRejectsMissingKeys(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nokeys"+ArchiveSuffix)

	// 手工构造一个只含 database.sqlite 的归档。
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("x")
	if err := writeTarEntry(tw, archiveDBName, body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// 缺 keys.json 必须被拒绝：否则会得到一个「看起来能恢复、实际读不出」的假象。
	if _, err := extractArchive(src, filepath.Join(dir, "out")); err == nil {
		t.Fatal("缺少 keys.json 的归档应被拒绝")
	}
}

func TestArchiveIgnoresUnexpectedEntries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "extra"+ArchiveSuffix)

	// 手工构造一个含路径穿越条目的归档。
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{
		archiveDBName:       []byte("db"),
		archiveKeysName:     []byte(`{"current":1,"keys":{"1":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + `"}}`),
		"../../evil.txt":    []byte("pwned"),
		"/tmp/absolute.txt": []byte("pwned"),
	} {
		if err := writeTarEntry(tw, name, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out := filepath.Join(dir, "out")
	got, err := extractArchive(src, out)
	if err != nil {
		t.Fatalf("含多余条目的归档仍应能解出已知内容：%v", err)
	}
	if got.DBPath != filepath.Join(out, archiveDBName) {
		t.Fatalf("数据库路径不对：%s", got.DBPath)
	}
	// 未知条目必须被丢弃，而不是写到 workDir 之外。
	if _, err := os.Stat(filepath.Join(dir, "evil.txt")); err == nil {
		t.Fatal("路径穿越条目被写到了目录外")
	}
	if _, err := os.Stat("/tmp/absolute.txt"); err == nil {
		t.Fatal("绝对路径条目被写出了 workDir")
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("workDir 内应只有解出的数据库，实际：%v", names)
	}
}

func TestMergeKeys(t *testing.T) {
	envKey := bytes.Repeat([]byte{0x11}, 32)
	arcKey := bytes.Repeat([]byte{0x22}, 32)

	// 归档补足缺失版本，并回报「需要更新 .env」。
	got, need := mergeKeys(map[int][]byte{1: envKey}, map[int][]byte{2: arcKey})
	if len(got) != 2 || !bytes.Equal(got[2], arcKey) {
		t.Fatalf("归档密钥未合并：%v", got)
	}
	if len(need) != 1 || need[0] != 2 {
		t.Fatalf("应回报版本 2 需要更新，实际：%v", need)
	}

	// 同版本同内容：不需要提示。
	got, need = mergeKeys(map[int][]byte{1: envKey}, map[int][]byte{1: envKey})
	if len(need) != 0 {
		t.Fatalf("完全一致时不应提示：%v", need)
	}
	if !bytes.Equal(got[1], envKey) {
		t.Fatal("合并结果不对")
	}

	// 同版本不同内容：归档对它自己的数据是权威 —— 以归档为准，但必须回报冲突。
	got, need = mergeKeys(map[int][]byte{1: envKey}, map[int][]byte{1: arcKey})
	if !bytes.Equal(got[1], arcKey) {
		t.Fatal("冲突时应以归档为准")
	}
	if len(need) != 1 || need[0] != 1 {
		t.Fatalf("冲突版本必须被回报：%v", need)
	}

	// 没有任何密钥：合并结果为空，调用方据此报错。
	got, _ = mergeKeys(nil, nil)
	if len(got) != 0 {
		t.Fatal("无密钥时结果应为空")
	}
}

func TestBackupNameRecognition(t *testing.T) {
	// 命名识别只看前缀/后缀，不校验时间戳（时间戳由 parseNameTime 负责）。
	recognized := []string{
		namePrefix + "20261009-190000" + ArchiveSuffix,
		namePrefix + "20261009-190000" + legacySuffix, // 旧格式仍可识别
	}
	for _, n := range recognized {
		if !isBackupName(n) {
			t.Errorf("isBackupName(%q) 应为 true", n)
		}
		if _, err := parseNameTime(n); err != nil {
			t.Errorf("parseNameTime(%q) 失败：%v", n, err)
		}
	}

	rejected := []string{
		".tmp-" + namePrefix + "20261009-190000" + ArchiveSuffix, // 未发布的临时文件
		namePrefix + "20261009-190000.tar",
		"random.tar.gz",
		"backup-.tar.gz",
	}
	for _, n := range rejected {
		if isBackupName(n) {
			t.Errorf("isBackupName(%q) 应为 false", n)
		}
	}

	// 时间戳非法：命名识别会通过，但解析时间必须失败。
	if _, err := parseNameTime(namePrefix + "bad-format" + ArchiveSuffix); err == nil {
		t.Error("非法时间戳应解析失败")
	}
}

func TestKeyFileEncoding(t *testing.T) {
	// 密钥以 base64 存放，必须能无损往返。
	raw := bytes.Repeat([]byte{0x5A}, 32)
	enc := base64.StdEncoding.EncodeToString(raw)
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || !bytes.Equal(dec, raw) {
		t.Fatalf("密钥 base64 往返失败：%v", err)
	}
}
