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
// (auth.Descriptor, BasePath) 的纯函数，见 Specs。因此路由表可以在不启动
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

// Specs 把 Descriptor 声明的动作展开为有序路由表。
//
// 顺序与注册顺序一致：集合路由（/deleted、/bulk）先于 /:id 路由，
// 避免静态段被 :id 抢占。未声明的动作不会产生路由。
func Specs(descriptor auth.Descriptor, basePath string) []RouteSpec {
	actions := descriptorActions(descriptor)
	has := func(name string) bool { _, ok := actions[name]; return ok }
	base := basePath

	specs := make([]RouteSpec, 0, 12)
	add := func(op Op, method, path, action string) {
		permission, _ := descriptor.PermissionFor(action)
		specs = append(specs, RouteSpec{
			Op:         op,
			Method:     method,
			Path:       path,
			Action:     action,
			Permission: permission,
		})
	}

	if has(actionList) {
		add(OpList, http.MethodGet, base, actionList)
	}
	if has(actionListDeleted) {
		add(OpListDeleted, http.MethodGet, base+"/deleted", actionListDeleted)
	}
	if has(actionCreate) {
		add(OpCreate, http.MethodPost, base, actionCreate)
		add(OpCreateBulk, http.MethodPost, base+"/bulk", actionCreate)
	}
	if has(actionUpdate) {
		add(OpUpdateBulk, http.MethodPut, base+"/bulk", actionUpdate)
	}
	if has(actionDelete) {
		add(OpDeleteBulk, http.MethodDelete, base+"/bulk", actionDelete)
	}
	if has(actionAuditRead) {
		add(OpAuditRead, http.MethodGet, base+"/:id/audit", actionAuditRead)
	}
	if has(actionRestore) {
		add(OpRestore, http.MethodPost, base+"/:id/restore", actionRestore)
	}
	if has(actionPurge) {
		add(OpPurge, http.MethodDelete, base+"/:id/purge", actionPurge)
	}
	if has(actionRead) {
		add(OpRead, http.MethodGet, base+"/:id", actionRead)
	}
	if has(actionUpdate) {
		add(OpUpdate, http.MethodPut, base+"/:id", actionUpdate)
	}
	if has(actionDelete) {
		add(OpDelete, http.MethodDelete, base+"/:id", actionDelete)
	}
	return specs
}
