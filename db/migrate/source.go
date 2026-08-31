package migrate

import "context"

// Direction 表示 migration 文件方向。
type Direction string

const (
	// DirectionUp 表示向前迁移脚本。
	DirectionUp Direction = "up"
	// DirectionDown 表示回滚脚本。
	DirectionDown Direction = "down"
)

// File 描述单个 migration 文件。
type File struct {
	Type      string
	Version   uint64
	Name      string
	Direction Direction
	Path      string
}

func (f File) fullName() string {
	if f.Path != "" {
		return f.Path
	}
	if f.Name == "" {
		return ""
	}
	return f.Name
}

// Migration 描述同一版本的一组 migration 文件。
type Migration struct {
	Type    string
	Version uint64
	Name    string
	Up      *File
	Down    *File
}

// ISource 抽象 migration 文件来源。
type ISource interface {
	// List 返回来源中的全部 migration 描述。
	List(ctx context.Context) ([]Migration, error)
	// Read 读取指定 migration 文件内容。
	Read(ctx context.Context, file File) ([]byte, error)
}
