package bootstrap

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractZipTopDir 把 zip 解压到 dest，并剥掉单一根目录（node-vX-win-x64/）：
// 若 zip 只有一个顶层目录，则把它的内容直接落在 dest 下。
func ExtractZipTopDir(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	// 收集顶层前缀。
	roots := map[string]struct{}{}
	for _, f := range r.File {
		parts := strings.SplitN(strings.TrimPrefix(filepath.ToSlash(f.Name), "./"), "/", 2)
		if parts[0] != "" {
			roots[parts[0]] = struct{}{}
		}
	}
	strip := ""
	if len(roots) == 1 {
		for k := range roots {
			strip = k + "/"
		}
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, f := range r.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), strip)
		if name == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("写 %s: %w", target, err)
	}
	return nil
}
