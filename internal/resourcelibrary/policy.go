package resourcelibrary

import (
	"archive/zip"
	"io"
	"os"
	"strings"
)

const (
	KindSkill  = "skill"
	KindPlugin = "plugin"

	// 与现有安装器相同的上限。云端资源库不能另定一套更大的包。
	SkillArchiveLimit  int64 = 128 << 20
	PluginArchiveLimit int64 = 64 << 20
	SkillExtractLimit  int64 = 128 << 20
	PluginExtractLimit int64 = 256 << 20
	MaxPackageFiles          = 10000
	maxControlBody           = 64 * 1024
)

func archiveLimit(kind string) int64 {
	if kind == KindPlugin {
		return PluginArchiveLimit
	}
	return SkillArchiveLimit
}

func extractLimit(kind string) int64 {
	if kind == KindPlugin {
		return PluginExtractLimit
	}
	return SkillExtractLimit
}

// InspectArchive 在把候选 ZIP 交给原生安装器之前拒绝明显危险的包。
// 不对入站包施加源码扩展名白名单；Skill/Plugin 能否安装仍由原生安装器决定，
// 这里负责路径穿越、符号链接、特殊文件、文件数和解压大小。
func InspectArchive(archivePath, kind string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return failed("PACKAGE_INVALID", "validation", "candidate archive is not a readable ZIP")
	}
	defer reader.Close()

	var files int
	var total int64
	limit := extractLimit(kind)
	for _, file := range reader.File {
		if file.Flags&0x1 != 0 {
			return failed("PACKAGE_INVALID", "validation", "encrypted ZIP entries are not allowed")
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return failedf("PACKAGE_INVALID", "validation", "ZIP symlink is not allowed: %s", file.Name)
		}
		name := strings.TrimSuffix(file.Name, "/")
		if err := validateArchiveName(name); err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			continue
		}
		mode := file.Mode()
		if !mode.IsRegular() {
			return failedf("PACKAGE_INVALID", "validation", "ZIP special file is not allowed: %s", file.Name)
		}
		files++
		if files > MaxPackageFiles {
			return failedf("PACKAGE_INVALID", "validation", "package exceeds %d files", MaxPackageFiles)
		}
		body, err := file.Open()
		if err != nil {
			return failed("PACKAGE_INVALID", "validation", "ZIP entry cannot be read")
		}
		copied, copyErr := io.Copy(io.Discard, io.LimitReader(body, limit-total+1))
		closeErr := body.Close()
		if copyErr != nil {
			return failed("PACKAGE_INVALID", "validation", "ZIP entry cannot be read")
		}
		if closeErr != nil {
			return failed("PACKAGE_INVALID", "validation", "ZIP entry cannot be read")
		}
		total += copied
		if total > limit {
			return failedf("PACKAGE_INVALID", "validation", "uncompressed package exceeds %d bytes", limit)
		}
	}
	if files == 0 {
		return failed("PACKAGE_INVALID", "validation", "candidate archive has no files")
	}
	return nil
}

func validateArchiveName(name string) error {
	if name == "" {
		return failed("PACKAGE_INVALID", "validation", "ZIP entry path is empty")
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, ":") {
		return failedf("PACKAGE_INVALID", "validation", "ZIP path escapes package root: %s", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return failedf("PACKAGE_INVALID", "validation", "ZIP path escapes package root: %s", name)
		}
	}
	return nil
}
