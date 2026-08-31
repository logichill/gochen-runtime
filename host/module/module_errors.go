package module

import (
	"context"
	"gochen/errors"
)

// wrapModuleErr 统一给模块错误补上模块上下文，并归一化成 AppError。
func wrapModuleErr(moduleID string, err error, message string) errors.IAppError {
	if err == nil {
		return nil
	}

	var appErr *errors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		return appErr.Wrap(message).WithContext("module", moduleID)
	}
	return errors.Wrap(err, errors.Internal, message).WithContext("module", moduleID)
}

// invalidModule 用一个始终返回预置错误的哨兵模块承接构建期失败。
//
// 这样模块注册表仍能保留模块 ID/Name，后续初始化、启动或路由注册阶段会得到
// 同一份错误，而不是因为空指针或缺项再触发次生故障。
type invalidModule struct {
	id   string
	name string
	err  error
}

func (m *invalidModule) ID() string { return m.id }

func (m *invalidModule) Name() string { return m.name }

// Init 在占位模块上直接透传原始错误。
func (m *invalidModule) Init(ModuleInitOptions) error {
	return m.err
}

// Start 在占位模块上直接透传原始错误。
func (m *invalidModule) Start(context.Context) (ModuleStopFunc, error) {
	return nil, m.err
}

// RegisterRoutes 在占位模块上直接透传原始错误。
func (m *invalidModule) RegisterRoutes(context.Context) error {
	return m.err
}

var _ IRouteModule = (*invalidModule)(nil)
