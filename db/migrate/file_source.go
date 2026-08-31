package migrate

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gochen/errors"
)

// FileSource 表示基于本地目录的 migration source。
type FileSource struct {
	root string
}

// NewFileSource 创建基于本地目录的 migration source。
func NewFileSource(root string) (*FileSource, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.NewCode(errors.InvalidInput, "migration source root cannot be empty")
	}
	return &FileSource{root: root}, nil
}

// List 扫描目录并返回按版本升序排列的 migration 集合。
func (s *FileSource) List(_ context.Context) ([]Migration, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "read migration source failed")
	}

	files := make([]File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		file, ok, err := parseMigrationFileName(entry.Name())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		file.Path = filepath.Join(s.root, entry.Name())
		files = append(files, file)
	}
	return groupMigrationFiles(files)
}

// Read 读取指定 migration 文件内容。
func (s *FileSource) Read(_ context.Context, file File) ([]byte, error) {
	content, err := os.ReadFile(file.Path)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "read migration file failed").
			WithContext("path", file.Path)
	}
	return content, nil
}

// FS 通过 fs.FS 读取 migration 文件，适合 embed.FS。
type FS struct {
	fsys fs.FS
	dir  string
}

// NewFS 创建基于 fs.FS 的 migration source。
func NewFS(fsys fs.FS, dir string) (*FS, error) {
	if fsys == nil {
		return nil, errors.NewCode(errors.InvalidInput, "migration fs cannot be nil")
	}
	dir = strings.Trim(strings.TrimSpace(dir), "/")
	if dir == "" || dir == "." {
		dir = "."
	}
	return &FS{fsys: fsys, dir: dir}, nil
}

// List 扫描 fs.FS 目录并返回按版本升序排列的 migration 集合。
func (s *FS) List(_ context.Context) ([]Migration, error) {
	entries, err := fs.ReadDir(s.fsys, s.dir)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "read migration fs failed").
			WithContext("dir", s.dir)
	}
	files := make([]File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		file, ok, err := parseMigrationFileName(entry.Name())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if s.dir == "." {
			file.Path = entry.Name()
		} else {
			file.Path = s.dir + "/" + entry.Name()
		}
		files = append(files, file)
	}
	return groupMigrationFiles(files)
}

// Read 读取指定 migration 文件内容。
func (s *FS) Read(_ context.Context, file File) ([]byte, error) {
	content, err := fs.ReadFile(s.fsys, file.Path)
	if err != nil {
		return nil, errors.Wrap(err, errors.Dependency, "read migration fs file failed").
			WithContext("path", file.Path)
	}
	return content, nil
}
