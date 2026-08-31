package lite

import (
	"context"

	"gochen/errors"
)

// ------------------------------------------------------------------------
// Association 占位实现：暂不支持关联写入，直接返回 ErrUnsupported。
// ------------------------------------------------------------------------

type unsupportedAssociation struct {
	name string
}

func (a *unsupportedAssociation) Name() string { return a.name }

func (a *unsupportedAssociation) Owner() any { return nil }

func (a *unsupportedAssociation) Append(ctx context.Context, targets ...any) error {
	_ = ctx
	_ = targets
	return errors.NewCode(errors.Unsupported, "orm: capability unsupported")
}

// Replace 替换配置。
func (a *unsupportedAssociation) Replace(ctx context.Context, targets ...any) error {
	_ = ctx
	_ = targets
	return errors.NewCode(errors.Unsupported, "orm: capability unsupported")
}

// Delete 删除对象并同步到存储。
func (a *unsupportedAssociation) Delete(ctx context.Context, targets ...any) error {
	_ = ctx
	_ = targets
	return errors.NewCode(errors.Unsupported, "orm: capability unsupported")
}

func (a *unsupportedAssociation) Clear(ctx context.Context) error {
	_ = ctx
	return errors.NewCode(errors.Unsupported, "orm: capability unsupported")
}
