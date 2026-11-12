package main

import (
	"context"
	"net/http"
	"strconv"

	"gochen-runtime/api/rest"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/host"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/module"
	"gochen/app"
	"gochen/auth"
	"gochen/db/orm"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/httpx"
)

const approveAction = "approve"

// newOrders 是唯一组合根：授权与执行绑定一次，再由多个入口引用。
func newOrders(database orm.IOrm) (*OrderApprover, module.IModule, error) {
	repository, err := repo.NewRepo[*Order, int64](database, "orders",
		repo.WithResourceKind[*Order, int64]("order"))
	if err != nil {
		return nil, nil, err
	}
	authorizer, err := auth.Compile(auth.Declaration{
		Descriptor: auth.Descriptor{Resource: "order", Actions: []auth.Action{
			{Name: approveAction, Kind: auth.KindUpdate, Permission: "order:api:approve"},
		}},
		ResourceAccess: auth.ResourceAccessData,
		Rules:          []auth.Rule{{Scope: &auth.ScopeRule{Resolver: departmentScope{}}, System: auth.SystemCheckConstraints}},
	}, auth.CompileOptions{Checker: authz.NewActionChecker(), Metadata: repository.ExecutionDescriptor().Metadata()})
	if err != nil {
		return nil, nil, err
	}
	command, err := app.NewCommand(app.CommandOptions[ApproveOrder, approvalAccess, Approval]{
		Resources: []app.CommandResource[ApproveOrder]{{
			Name: "order", Repository: repository, Authorizer: authorizer, Action: approveAction,
			Intent: func(input ApproveOrder) (auth.Request, error) {
				return auth.Request{IDs: []string{strconv.FormatInt(input.ID, 10)}}, nil
			},
		}},
		Access: func(_ context.Context, _ ApproveOrder, grants []*app.CommandGrant) (approvalAccess, error) {
			orders, err := app.NewCommandRepository(repository, grants[0])
			return approvalAccess{orders: orders, withinTx: repository.WithinTx}, err
		},
		Handler: approveOrder,
	})
	if err != nil {
		return nil, nil, err
	}
	// 同一个 command 同时提供调用能力与冻结的授权元数据，HTTP 不另选动作。
	approver := &OrderApprover{execute: command.Execute}
	orders, err := host.Module("orders").Name("订单审批").
		Binding(rest.Bind(http.MethodPost, "/:id/approve", command, decodeApprove)).Build()
	return approver, orders, err
}

type departmentScope struct{}

func (departmentScope) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension("department")}}
}

func (departmentScope) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok {
		return spec.Spec{}, nil, errors.NewCode(errors.Unauthorized, "principal is required")
	}
	if principal.ActiveScopeID <= 0 {
		return spec.Spec{}, nil, errors.NewCode(errors.Forbidden, "department is required")
	}
	return spec.Eq(meta.ByDimension("department"), principal.ActiveScopeID), nil, nil
}

func decodeApprove(c httpx.IContext) (ApproveOrder, error) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		return ApproveOrder{}, errors.NewCode(errors.InvalidInput, "order ID must be positive")
	}
	var body struct {
		ExpectedVersion *uint64 `json:"expected_version"`
		Reason          string  `json:"reason"`
	}
	if err := c.BindJSON(&body); err != nil {
		return ApproveOrder{}, errors.Wrap(err, errors.InvalidInput, "invalid approval request")
	}
	if body.ExpectedVersion == nil {
		return ApproveOrder{}, errors.NewCode(errors.InvalidInput, "expected_version is required")
	}
	return ApproveOrder{ID: id, ExpectedVersion: *body.ExpectedVersion, Reason: body.Reason}, nil
}

// demoContext 只模拟可信认证结果；角色、部门和权限均来自固定演示资料。
func demoContext(ctx context.Context, role string) (context.Context, error) {
	principal := authz.Principal{SubjectID: role, ActiveScopeID: 10}
	switch role {
	case "approver":
		principal.Permissions = []string{"order:api:approve"}
	case "reader":
		principal.Permissions = []string{"order:api:read"}
	default:
		return ctx, nil
	}
	return authz.WithPrincipal(ctx, principal)
}

// demoIdentity 仅供回环服务使用，生产环境必须替换为可信认证入口。
func demoIdentity(c httpx.IContext, next func() error) error {
	ctx, err := demoContext(c.RequestContext(), c.Header("X-Demo-Role"))
	if err != nil {
		return err
	}
	c.SetContext(c.RequestContext().WithContext(ctx))
	return next()
}
