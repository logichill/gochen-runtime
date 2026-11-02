// 演示权限码与项目维度条件授权，存储和内存解释器使用同一份条件。
package main

import (
	"context"
	"gochen-runtime/host/authz"
	"gochen/auth"
	"gochen/auth/scoped"
	"gochen/domain/spec"
	"log"
)

type document struct {
	ID         string `entity:"id"`
	Department int64  `dim:"department"`
}

func main() {
	log.SetFlags(0)
	authorizer, err := scoped.New(authz.NewActionChecker(), authz.NewScopeResolver("department", nil))
	must(err)
	ctx, err := authz.WithPrincipal(context.Background(), authz.Principal{SubjectID: "alice", ActiveScopeID: 1001, Permissions: []string{"document:api:read"}})
	must(err)
	decision, err := authorizer.Authorize(ctx, auth.Request{Resource: "document", Action: "read", Permission: "document:api:read"})
	must(err)
	log.Printf("read allowed=%t condition=%s", decision.Allowed, decision.Spec.Canonical())
	for _, doc := range []*document{{ID: "doc-1", Department: 1001}, {ID: "doc-2", Department: 2002}} {
		visible, err := spec.NewInterpreter().Match(decision.Spec, doc)
		must(err)
		log.Printf("document=%s visible=%t", doc.ID, visible)
	}
	decision, err = authorizer.Authorize(ctx, auth.Request{Resource: "document", Action: "update", Permission: "document:api:write"})
	must(err)
	log.Printf("write allowed=%t reason=%s", decision.Allowed, decision.Reason)
	ctx, err = scoped.WithDataScope(ctx, scoped.DenyAll())
	must(err)
	decision, err = authorizer.Authorize(ctx, auth.Request{Resource: "document", Action: "read", Permission: "document:api:read"})
	must(err)
	log.Printf("empty scope condition=%s", decision.Spec.Canonical())
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
