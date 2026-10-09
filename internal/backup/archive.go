package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 归档格式标识。
//
// 设计目标（用户明确要求）：**备份默认必须可恢复**。
// 只存密文快照的话，一旦 DATA_ENCRYPTION_KEY 丢失，备份就是废纸 ——
// 所以这里把快照与密钥一起打包成自包含归档，恢复时无需任何外部密钥。
//
// 代价必须说清楚：归档文件本身等同于明文凭据。它应当按最高敏感级别保管
// （离线、加密介质、访问受控），不要放在能公网下载的地方。
const (
	ArchiveFormat        = "passpal-backup"
	ArchiveFormatVersion = 1

	archiveManifestName = "manifest.json"
	archiveKeysName     = "keys.json"
	archiveDBName       = "database.sqlite"
)

// ArchiveSuffix 是备份归档的扩展名。
const ArchiveSuffix = ".tar.gz"

// Manifest 描述一次备份的内容与来源，恢复前可据此核对。
type Manifest struct {
	Format            string         `json:"format"`
	FormatVersion     int            `json:"format_version"`
	CreatedAt         int64          `json:"created_at"`
	SchemaVersion     int64          `json:"schema_version"`
	CurrentKeyVersion int            `json:"current_key_version"`
	KeyVersions       []int          `json:"key_versions"`
	Counts            map[string]int `json:"counts"`
	DBSize            int64          `json:"db_size"`
	DBSHA256          string         `json:"db_sha256"`
}

// keyFile 是归档内的密钥清单（版本 -> base64 密钥）。
type keyFile struct {
	Current int               `json:"current"`
	Keys    map[string]string `json:"keys"`
}

// extracted 是解包后的归档内容。
type extracted struct {
	DBPath   string
	Keys     map[int][]byte
	Manifest *Manifest
}

// writeArchive 把数据库快照与密钥打成自包含归档。
//
// 写入顺序：keys.json -> database.sqlite（边写边算 sha256）-> manifest.json。
// 清单放最后，才能带上数据库文件的校验和。
func writeArchive(dst, dbPath string, keys map[int][]byte, current int,
	counts map[string]int, schemaVersion int64) error {

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	// 1) 密钥清单
	kf := keyFile{Current: current, Keys: make(map[string]string, len(keys))}
	versions := make([]int, 0, len(keys))
	for v, k := range keys {
		kf.Keys[strconv.Itoa(v)] = base64.StdEncoding.EncodeToString(k)
		versions = append(versions, v)
	}
	sort.Ints(versions)
	keyJSON, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarEntry(tw, archiveKeysName, keyJSON); err != nil {
		return err
	}

	// 2) 数据库快照（流式，同时算 sha256）
	src, err := os.Open(dbPath)
	if err != nil {
		return err
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil {
		return err
	}
	h := sha256.New()
	if err := writeTarEntryStream(tw, archiveDBName, fi.Size(), io.TeeReader(src, h)); err != nil {
		return err
	}

	// 3) 清单
	man := Manifest{
		Format:            ArchiveFormat,
		FormatVersion:     ArchiveFormatVersion,
		CreatedAt:         time.Now().Unix(),
		SchemaVersion:     schemaVersion,
		CurrentKeyVersion: current,
		KeyVersions:       versions,
		Counts:            counts,
		DBSize:            fi.Size(),
		DBSHA256:          hex.EncodeToString(h.Sum(nil)),
	}
	manJSON, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	if err := writeTarEntry(tw, archiveManifestName, manJSON); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return out.Sync()
}

func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(data)),
		ModTime: time.Now(),
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func writeTarEntryStream(tw *tar.Writer, name string, size int64, r io.Reader) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    size,
		ModTime: time.Now(),
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := io.Copy(tw, r)
	return err
}

// extractArchive 把归档解到 workDir，返回数据库路径、密钥与清单。
//
// 只接受已知的三个条目名，且拒绝任何路径穿越 —— 归档可能来自不受控的地方。
func extractArchive(src, workDir string) (*extracted, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("归档不是有效的 gzip：%w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}

	out := &extracted{DBPath: filepath.Join(workDir, archiveDBName)}
	var manifestRaw, keysRaw []byte
	seen := map[string]bool{}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取归档失败：%w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// 只认三个已知条目，其余一律忽略（不落地，避免任何路径穿越风险）。
		switch hdr.Name {
		case archiveDBName:
			if err := writeStreamToFile(out.DBPath, tr, 0o600); err != nil {
				return nil, err
			}
			seen[archiveDBName] = true
		case archiveKeysName:
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			keysRaw = b
			seen[archiveKeysName] = true
		case archiveManifestName:
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			manifestRaw = b
			seen[archiveManifestName] = true
		}
	}

	if !seen[archiveDBName] {
		return nil, errors.New("归档内缺少 database.sqlite")
	}
	if !seen[archiveKeysName] {
		return nil, errors.New("归档内缺少 keys.json，无法在无外部密钥的情况下恢复")
	}

	var kf keyFile
	if err := json.Unmarshal(keysRaw, &kf); err != nil {
		return nil, fmt.Errorf("解析归档密钥失败：%w", err)
	}
	keys := make(map[int][]byte, len(kf.Keys))
	for vs, b64 := range kf.Keys {
		v, err := strconv.Atoi(vs)
		if err != nil || v < 1 || v > 255 {
			return nil, fmt.Errorf("归档内密钥版本非法：%q", vs)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("归档内密钥版本 %d 不是合法 base64：%w", v, err)
		}
		keys[v] = raw
	}
	if len(keys) == 0 {
		return nil, errors.New("归档内没有可用密钥")
	}
	out.Keys = keys

	if len(manifestRaw) > 0 {
		var man Manifest
		if err := json.Unmarshal(manifestRaw, &man); err == nil {
			out.Manifest = &man
		}
	}
	return out, nil
}

func writeStreamToFile(path string, r io.Reader, perm os.FileMode) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// IsArchivePath 报告路径是否为自包含备份归档。
func IsArchivePath(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ArchiveSuffix)
}
