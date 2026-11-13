package rest

import (
	"net/http"

	"gochen/auth"
)

// Op 标识一条标准路由对应的处理语义。
//
// 一个 Descriptor 动作可能展开成多条路由（例如 create 同时有单条与 /bulk），
// 所以 Op 比 Action 更精确：它是路由表与 handler 的对应键，而 Action 只用于
// 权限码查找与文档分组。
type Op string

const (
	OpList        Op = "list"
	OpListDeleted Op = "list_deleted"
	OpCreate      Op = "create"
	OpCreateBulk  Op = "create_bulk"
	OpUpdate      Op = "update"
	OpUpdateBulk  Op = "update_bulk"
	OpDelete      Op = "delete"
	OpDeleteBulk  Op = "delete_bulk"
	OpRead        Op = "read"
	OpAuditRead   Op = "audit_read"
	OpRestore     Op = "restore"
	OpPurge       Op = "purge"
	OpAction      Op = "action"
)

// RouteSpec 是一条路由的纯数据描述。
//
// 它不含 handler，也不含任何泛型参数：标准路由集合是
// (auth.Descriptor, BasePath, Operations) 的纯函数，见 Specs。因此路由表可以在不启动
// Server 的情况下推导出来，用于 OpenAPI、跨应用路由冲突检测与路由清单。
//
// Path 是相对路由组/模块前缀的路径；空字符串表示路由组根路径。
type RouteSpec struct {
	Op         Op
	Method     string
	Path       string
	Action     string
	Permission string
}

// Specs 按显式操作清单与授权描述推导有序路由表。
// nil 清单使用标准 CRUD 与审计读取，不自动公开 restore、list_deleted 或 purge。
// Delete 路由使用描述中的 delete 动作；没有 delete 时使用 purge。
// NewRouteBuilder 另行验证显式操作、执行能力与 DeleteAction 的同源一致性。
func Specs(descriptor auth.Descriptor, basePath string, operations ...Op) []RouteSpec {
	actions := descriptorActions(descriptor)
	deleteAction := actionPurge
	if _, exists := actions[actionDelete]; exists {
		deleteAction = actionDelete
	}
	selected := operations
	if selected == nil {
		selected = defaultOperations()
	}
	wanted := make(map[Op]bool, len(selected))
	for _, op := range selected {
		wanted[op] = true
	}
	specs := make([]RouteSpec, 0, len(selected))
	for _, route := range routeTemplates(basePath, deleteAction) {
		if !wanted[route.Op] {
			continue
		}
		if _, declared := actions[route.Action]; !declared {
			continue
		}
		route.Permission, _ = descriptor.PermissionFor(route.Action)
		specs = append(specs, route)
	}
	return specs
}

func defaultOperations() []Op {
	return []Op{OpList, OpCreate, OpCreateBulk, OpUpdateBulk, OpDeleteBulk, OpAuditRead, OpRead, OpUpdate, OpDelete}
}

func routeTemplates(base, deleteAction string) []RouteSpec {
	return []RouteSpec{
		{Op: OpList, Method: http.MethodGet, Path: base, Action: actionList},
		{Op: OpListDeleted, Method: http.MethodGet, Path: base + "/deleted", Action: actionListDeleted},
		{Op: OpCreate, Method: http.MethodPost, Path: base, Action: actionCreate},
		{Op: OpCreateBulk, Method: http.MethodPost, Path: base + "/bulk", Action: actionCreate},
		{Op: OpUpdateBulk, Method: http.MethodPut, Path: base + "/bulk", Action: actionUpdate},
		{Op: OpDeleteBulk, Method: http.MethodDelete, Path: base + "/bulk", Action: deleteAction},
		{Op: OpAuditRead, Method: http.MethodGet, Path: base + "/:id/audit", Action: actionAuditRead},
		{Op: OpRestore, Method: http.MethodPost, Path: base + "/:id/restore", Action: actionRestore},
		{Op: OpPurge, Method: http.MethodDelete, Path: base + "/:id/purge", Action: actionPurge},
		{Op: OpRead, Method: http.MethodGet, Path: base + "/:id", Action: actionRead},
		{Op: OpUpdate, Method: http.MethodPut, Path: base + "/:id", Action: actionUpdate},
		{Op: OpDelete, Method: http.MethodDelete, Path: base + "/:id", Action: deleteAction},
	}
}
