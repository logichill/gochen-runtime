package repo

import (
	"gochen/app"
	"gochen/domain"
)

// WithResourceContract 声明资源实例的维度写能力。
func WithResourceContract[T domain.IEntity[ID], ID comparable](contract app.ResourceContract) Option[T, ID] {
	return func(r *Repo[T, ID]) { r.contract = contract }
}

// ExecutionDescriptor 返回仓储已验证且只读的有效契约。
func (r *Repo[T, ID]) ExecutionDescriptor() app.ExecutionDescriptor { return r.execution }
