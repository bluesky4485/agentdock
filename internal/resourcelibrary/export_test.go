package resourcelibrary

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExportDirectory_完整保留配置文件(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Demo.\n---\n\n# Demo\n")
	files := map[string]string{
		".env": "API_TOKEN=local\n", "credentials/token.json": "{\"token\":\"sample\"}",
		"secrets/private.key": "private-key-fixture",
		"config.sqlite":       "fixture-data", "README.md": "Normal document",
	}
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	exported, err := ExportDirectory(root, KindSkill, archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Files) != 1+len(files) {
		t.Fatalf("lost files: %#v", exported.Files)
	}
	if err := InspectArchive(archive, KindSkill); err != nil {
		t.Fatal(err)
	}
	preview, err := validateSkillArchive(t, archive, exported.ArchiveDigest, "demo-skill", exported.ContentDigest)
	if err != nil || !preview.Valid {
		t.Fatalf("native Skill validator rejected complete backup: %#v %v", preview, err)
	}
	zipReader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zipReader.Close()
	for name, content := range files {
		if !hasFile(exported.Files, name) {
			t.Fatalf("missing file %s", name)
		}
		found := false
		for _, f := range zipReader.File {
			if f.Name != name {
				continue
			}
			found = true
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || string(data) != content {
				t.Fatalf("%s content mismatch: %v", name, err)
			}
		}
		if !found {
			t.Fatalf("zip lacks %s", name)
		}
	}
}

func TestExportDirectory_拒绝符号链接但允许自定义目录(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Export demo.\n---\n\n# Demo\n")
	if err := os.Symlink(filepath.Join(root, "SKILL.md"), filepath.Join(root, "references")); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportDirectory(root, KindSkill, filepath.Join(t.TempDir(), "link.zip")); err == nil {
		t.Fatal("export accepted a symlink")
	}
	os.Remove(filepath.Join(root, "references"))

	writeFile(t, filepath.Join(root, "bin", "tool"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "test_run.py"), "print('ok')\n")
	writeFile(t, filepath.Join(root, "package.json"), "{}\n")
	writeFile(t, filepath.Join(root, "docs", "使用说明.md"), "# 正常文档\n")
	if err := os.Chmod(filepath.Join(root, "bin", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "skill.zip")
	exported, err := ExportDirectory(root, KindSkill, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := InspectArchive(archive, KindSkill); err != nil {
		t.Fatal(err)
	}
	if !hasFile(exported.Files, "bin/tool") || !hasFile(exported.Files, "test_run.py") ||
		!hasFile(exported.Files, "package.json") || !hasFile(exported.Files, "docs/使用说明.md") {
		t.Fatalf("expected installed package files in backup, got %#v", exported.Files)
	}
	zipReader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range zipReader.File {
		if runtime.GOOS != "windows" && file.Name == "bin/tool" && file.Mode().Perm()&0o111 == 0 {
			t.Errorf("export lost executable bit: %s %v", file.Name, file.Mode())
		}
	}
	if err := zipReader.Close(); err != nil {
		t.Fatal(err)
	}
	preview, err := validateSkillArchive(t, archive, exported.ArchiveDigest, "demo-skill", exported.ContentDigest)
	if err != nil || !preview.Valid {
		t.Fatalf("native validator rejected exported Skill: preview=%#v err=%v", preview, err)
	}
}

func TestExportDirectory_源码和依赖目录都保留(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Demo.\n---\n\n# Demo\n")
	files := []string{
		"run.py", ".env.example", "_meta.json", ".clawhub/origin.json",
		".git/config", "node_modules/lib/package.json", "__pycache__/run.pyc",
		"scripts/__pycache__/cache.pyc",
	}
	for _, name := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), "content\n")
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	exported, err := ExportDirectory(root, KindSkill, archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Files) != len(files)+1 {
		t.Fatalf("unexpected files: %#v", exported.Files)
	}
	for _, name := range files {
		if !hasFile(exported.Files, name) {
			t.Fatalf("file omitted: %s", name)
		}
	}
	if err := InspectArchive(archive, KindSkill); err != nil {
		t.Fatal(err)
	}
}

func TestExportDirectory_目录名称不影响备份(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Demo.\n---\n\n# Demo\n")
	names := []string{"credentials/README.md", "credentials/token.json", "secrets/private.key", "secrets/examples/mock.json"}
	for _, name := range names {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), "fixture")
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	exported, err := ExportDirectory(root, KindSkill, archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !hasFile(exported.Files, name) {
			t.Fatalf("file omitted: %s", name)
		}
	}
	if err := InspectArchive(archive, KindSkill); err != nil {
		t.Fatal(err)
	}
}

func TestExportDirectory_敏感文件名不触发额外校验(t *testing.T) {
	for _, name := range []string{".env", "credentials/token.json", "secret.json", "private_key.yaml", "state.sqlite", "sessions/state.json"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Demo.\n---\n\n# Demo\n")
			writeFile(t, filepath.Join(root, filepath.FromSlash(name)), "fixture")
			exported, err := ExportDirectory(root, KindSkill, filepath.Join(t.TempDir(), "backup.zip"))
			if err != nil {
				t.Fatal(err)
			}
			if !hasFile(exported.Files, name) {
				t.Fatalf("file omitted: %#v", exported)
			}
		})
	}
}

func TestExportDirectory_嵌套Skill文档作为普通资源保留(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), "---\nname: demo-skill\ndescription: Export demo.\n---\n\n# Demo\n")
	writeFile(t, filepath.Join(root, "docs", "SKILL.md"), "---\nname: example-only\ndescription: Example document.\n---\n\n# Example\n")

	archive := filepath.Join(t.TempDir(), "skill.zip")
	exported, err := ExportDirectory(root, KindSkill, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFile(exported.Files, "docs/SKILL.md") {
		t.Fatalf("nested SKILL.md was omitted: %#v", exported.Files)
	}
	preview, err := validateSkillArchive(t, archive, exported.ArchiveDigest, "demo-skill", exported.ContentDigest)
	if err != nil || !preview.Valid {
		t.Fatalf("native Skill validator rejected nested resource document: %#v %v", preview, err)
	}
}

func TestExportDirectory_Plugin完整保留配置(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "plugin.json"), "{\"name\":\"demo-plugin\",\"version\":\"1.0.0\"}")
	writeFile(t, filepath.Join(root, "skills", "demo-skill", "SKILL.md"), "---\nname: demo-skill\ndescription: Plugin skill.\n---\n\n# Demo\n")
	writeFile(t, filepath.Join(root, ".env"), "API_KEY=fixture")
	writeFile(t, filepath.Join(root, "notes.pem"), "fixture-key")
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	exported, err := ExportDirectory(root, KindPlugin, archive)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFile(exported.Files, ".env") || !hasFile(exported.Files, "notes.pem") {
		t.Fatalf("plugin files lost: %#v", exported.Files)
	}
	if err := InspectArchive(archive, KindPlugin); err != nil {
		t.Fatal(err)
	}
	manager := newPluginManager(t, filepath.Join(t.TempDir(), "home"))
	review := manager.ValidateSource(context.Background(), archive)
	if !review.Valid || review.PackageDigest != exported.ContentDigest {
		t.Fatalf("native Plugin validator rejected complete backup: %#v (digest %s)", review, exported.ContentDigest)
	}
}

func TestExportDirectory_Plugin保留原生安装器支持的文件(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "plugin.json"), `{"name":"demo-plugin","version":"1.0.0","description":"Backup"}`)
	writeFile(t, filepath.Join(root, "package.json"), `{"name":"demo-plugin"}`)
	writeFile(t, filepath.Join(root, "skills", "demo-skill", "SKILL.md"), "---\nname: demo-skill\ndescription: Backup.\n---\n\n# Demo\n\nBody.\n")
	writeFile(t, filepath.Join(root, "skills", "demo-skill", "test_run.py"), "print('ok')\n")
	writeFile(t, filepath.Join(root, "assets", "说明.md"), "# 文档\n")
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	exported, err := ExportDirectory(root, KindPlugin, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := InspectArchive(archive, KindPlugin); err != nil {
		t.Fatal(err)
	}
	if !hasFile(exported.Files, "package.json") || !hasFile(exported.Files, "skills/demo-skill/test_run.py") {
		t.Fatalf("plugin files lost: %#v", exported.Files)
	}
	manager := newPluginManager(t, filepath.Join(t.TempDir(), "agentdock-home"))
	review := manager.ValidateSource(context.Background(), archive)
	if !review.Valid || review.PackageDigest != exported.ContentDigest {
		t.Fatalf("native Plugin validator disagrees with backup digest: %#v, exported=%s", review, exported.ContentDigest)
	}
}

func TestInspectArchive_拒绝危险结构但不拒绝配置文件(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "traversal.zip")
	writeRawZip(t, bad, map[string]string{"../evil.txt": "fixture"})
	if err := InspectArchive(bad, KindSkill); err == nil {
		t.Fatal("accepted traversal")
	}
	link := filepath.Join(t.TempDir(), "link.zip")
	writeSymlinkZip(t, link, "link", "outside")
	if err := InspectArchive(link, KindSkill); err == nil {
		t.Fatal("accepted symlink")
	}
	good := filepath.Join(t.TempDir(), "good.zip")
	writeRawZip(t, good, map[string]string{
		"SKILL.md": "---\nname: demo-skill\ndescription: Demo.\n---\n\n# Demo\n",
		".env":     "fixture", "credentials/token.json": "fixture", "secrets/private.key": "fixture",
	})
	if err := InspectArchive(good, KindSkill); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRawZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeSymlinkZip(t *testing.T, path, name, target string) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	header := &zip.FileHeader{Name: name}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(target)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	return names
}
