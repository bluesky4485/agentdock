package resourcelibrary

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	skills "github.com/uvwt/agentdock/internal/skill"
)

// Export 是受管资源的安全备份 ZIP 摘要。本地绝对路径不会出现在结果里。
type Export struct {
	Files         []FileEntry
	Size          int64
	ArchiveDigest string
	ContentDigest string
}

type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ExportDirectory 把一个已安装的受管目录打成 ZIP。
// 从已安装的受管目录导出完整资源，不对凭据、缓存或普通文件做隐式剔除。
func ExportDirectory(root, kind, destination string) (Export, error) {
	if kind != KindSkill && kind != KindPlugin {
		return Export{}, failed("PACKAGE_KIND_INVALID", "validation", "package kind must be skill or plugin")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return Export{}, failed("PACKAGE_NOT_FOUND", "not_found", "managed package directory cannot be inspected")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Export{}, failed("PACKAGE_INVALID", "validation", "managed package must be a regular directory")
	}

	files, err := collectBackupFiles(root, kind)
	if err != nil {
		return Export{}, err
	}
	if kind == KindSkill && !hasFile(files, "SKILL.md") {
		return Export{}, failed("PACKAGE_INVALID", "validation", "managed Skill export requires SKILL.md")
	}
	if kind == KindPlugin && !hasFile(files, "plugin.json") {
		return Export{}, failed("PACKAGE_INVALID", "validation", "managed Plugin export requires plugin.json")
	}

	// 摘要按实际文件集计算，原生安装器仅忽略操作系统 ZIP 传输元数据。
	size, archiveDigest, contentDigest, err := writeBackupZip(root, files, destination, archiveLimit(kind))
	if err != nil {
		return Export{}, err
	}
	return Export{
		Files: files, Size: size,
		ArchiveDigest: archiveDigest, ContentDigest: contentDigest,
	}, nil
}

func hasFile(files []FileEntry, name string) bool {
	for _, file := range files {
		if file.Path == name {
			return true
		}
	}
	return false
}

func collectBackupFiles(root, kind string) ([]FileEntry, error) {
	fsRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, failed("PACKAGE_INVALID", "validation", "managed package directory cannot be opened")
	}
	defer fsRoot.Close()

	files := make([]FileEntry, 0)
	var total int64
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return failed("PACKAGE_INVALID", "validation", "managed package cannot be walked")
		}
		if current == root {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || !filepath.IsLocal(relative) {
			return failed("PACKAGE_INVALID", "validation", "managed package path escapes its root")
		}
		relative = filepath.ToSlash(relative)
		if skills.IsIgnoredPackageMetadataPath(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// os.Root 逐段拒绝符号链接替换。WalkDir 看到的类型不够，因为中间目录可能在遍历后被换掉。
		info, err := fsRoot.Lstat(relative)
		if err != nil {
			return failed("PACKAGE_INVALID", "validation", "managed package path cannot be inspected")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return failedf("PACKAGE_INVALID", "validation", "symlink is not allowed: %s", relative)
		}
		if err := validateArchiveName(relative); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return failedf("PACKAGE_INVALID", "validation", "special file is not allowed: %s", relative)
		}
		if info.Size() < 0 || total > extractLimit(kind)-info.Size() {
			return failedf("PACKAGE_INVALID", "validation", "package exceeds %d bytes", extractLimit(kind))
		}
		total += info.Size()
		if len(files)+1 > MaxPackageFiles {
			return failedf("PACKAGE_INVALID", "validation", "package exceeds %d files", MaxPackageFiles)
		}
		files = append(files, FileEntry{Path: relative, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, failed("PACKAGE_INVALID", "validation", "managed package has no portable files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func writeBackupZip(root string, files []FileEntry, destination string, limit int64) (int64, string, string, error) {
	fsRoot, err := os.OpenRoot(root)
	if err != nil {
		return 0, "", "", failed("PACKAGE_INVALID", "validation", "managed package directory cannot be opened")
	}
	defer fsRoot.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", "", failed("PACKAGE_INVALID", "validation", "export archive cannot be created")
	}
	writer := zip.NewWriter(output)
	failedWrite := false
	// 使用安装器相同的路径、权限和内容序列计算 digest。
	contentHash := sha256.New()
	for _, file := range files {
		info, err := fsRoot.Lstat(file.Path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != file.Size {
			failedWrite = true
			break
		}
		header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
		// 与原生 ZIP 安装器的权限归一化一致，保留脚本可执行位。
		mode := info.Mode().Perm() & 0o755
		if mode == 0 {
			mode = 0o600
		}
		header.SetMode(mode)
		fmt.Fprintf(contentHash, "%s\x00%o\x00%d\x00", file.Path, mode, file.Size)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			failedWrite = true
			break
		}
		source, err := fsRoot.Open(file.Path)
		if err != nil {
			failedWrite = true
			break
		}
		written, copyErr := io.Copy(io.MultiWriter(entry, contentHash), io.LimitReader(source, file.Size+1))
		closeErr := source.Close()
		if copyErr != nil || closeErr != nil || written != file.Size {
			failedWrite = true
			break
		}
		_, _ = contentHash.Write([]byte{0})
	}
	closeZipErr := writer.Close()
	closeFileErr := output.Close()
	if failedWrite || closeZipErr != nil || closeFileErr != nil {
		_ = os.Remove(destination)
		return 0, "", "", failed("PACKAGE_INVALID", "validation", "export archive cannot be written")
	}
	info, err := os.Lstat(destination)
	if err != nil {
		_ = os.Remove(destination)
		return 0, "", "", failed("PACKAGE_INVALID", "validation", "export archive cannot be inspected")
	}
	if info.Size() > limit || !info.Mode().IsRegular() {
		_ = os.Remove(destination)
		return 0, "", "", failedf("PACKAGE_INVALID", "validation", "export archive exceeds %d bytes", limit)
	}
	digest, err := skills.DigestFile(destination)
	if err != nil {
		_ = os.Remove(destination)
		return 0, "", "", failed("PACKAGE_INVALID", "validation", "export archive digest failed")
	}
	return info.Size(), digest, "sha256:" + hex.EncodeToString(contentHash.Sum(nil)), nil
}
