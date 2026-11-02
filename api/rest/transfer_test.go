package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/app/crud"
	"gochen/app/operation"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/httpx"
	"gochen/testkit"
	"gochen/testkit/contracttest"
)

type httpTransferPolicy struct{}

func (httpTransferPolicy) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Assignable: []meta.Dimension{"department"}}
}
func (httpTransferPolicy) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	decision := auth.Decision{Allowed: true, Spec: spec.All()}
	if req.Action == "receive" {
		decision.Assign = req.Targets
	}
	return decision, nil
}

type httpTransferApplication struct {
	*crud.Application[*contracttest.Document, int64]
	calls       int
	version     uint64
	operationID string
	degraded    bool
}

func (a *httpTransferApplication) Transfer(ctx context.Context, action string, id int64, version uint64, targets []auth.Assignment, opts ...crud.ExecOption) (*contracttest.Document, error) {
	a.calls++
	a.version = version
	a.operationID = operation.OperationIDFromContext(ctx)
	entity, err := a.Application.Transfer(ctx, action, id, version, targets, opts...)
	if err == nil && a.degraded {
		err = contextx.WrapAfterCommitError(errors.NewCode(errors.Internal, "notification failed"))
	}
	return entity, err
}

func TestTransferHTTPRequiresVersionAndIdempotencyAndReplaysResult(t *testing.T) {
	r, err := testkit.NewMemoryRepository[*contracttest.Document, int64](testkit.NewInt64Sequence(1), testkit.WithMemoryResourceContract[*contracttest.Document, int64](contracttest.Contract()), testkit.WithMemoryIsolationField[*contracttest.Document, int64](meta.ByDimension("tenant"), nil))
	if err != nil {
		t.Fatal(err)
	}
	application, err := crud.NewApplication(r, crud.Options[*contracttest.Document, int64]{Resource: "document", Authorizer: httpTransferPolicy{},
		Actions: []auth.Action{{Name: "move", Kind: auth.KindTransfer, ReceiveAction: "receive", Dimensions: []meta.Dimension{"department"}}, {Name: "receive", Kind: auth.KindTransfer}}})
	if err != nil {
		t.Fatal(err)
	}
	app := &httpTransferApplication{Application: application}
	ctx, err := contextx.WithOperator(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = contextx.WithTenantID(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	entity := &contracttest.Document{Department: 1}
	if err := r.Create(ctx, entity, spec.All()); err != nil {
		t.Fatal(err)
	}
	store := operation.NewMemoryStore(operation.WithMaxRecords(1))
	runner := operation.NewRunner(&operation.RunnerOptions{Store: store})
	router := testkit.NewRecordingRouter()
	var captured error
	if err := Transfer(router, app, runner, "/documents/:id/move", "move", WithErrorHandler[*contracttest.Document, int64](func(c httpx.IContext, err error) httpx.IResponse { captured = err; return DefaultErrorHandler(c, err) })); err != nil {
		t.Fatal(err)
	}
	handler := router.RecordedRoutes()[0].Handler
	invoke := func(key, body string) (*operation.Result, error) {
		captured = nil
		request := httptest.NewRequest(http.MethodPost, "/documents/1/move", strings.NewReader(body)).WithContext(ctx)
		request.Header.Set("Idempotency-Key", key)
		writer := httptest.NewRecorder()
		c, err := nethttp.NewBaseContext(writer, request)
		if err != nil {
			return nil, err
		}
		c.SetParam("id", "1")
		if err := handler(c); err != nil {
			return nil, err
		}
		if captured != nil {
			return nil, captured
		}
		var result operation.Result
		err = json.Unmarshal(writer.Body.Bytes(), &result)
		if app.degraded && (writer.Code != http.StatusOK || writer.Header().Get("X-Execution-Status") != "degraded" || result.Operation.Status != operation.StatusDegraded) {
			t.Fatalf("degraded response: %d %v %+v", writer.Code, writer.Header(), result)
		}
		return &result, err
	}
	for _, missing := range []struct{ key, body string }{
		{"", `{"expected_version":0,"targets":[{"dimension":"department","value":2}]}`},
		{" \t ", `{"expected_version":0,"targets":[{"dimension":"department","value":2}]}`},
		{"key", `{"targets":[{"dimension":"department","value":2}]}`},
		{"key", `{"expected_version":null,"targets":[{"dimension":"department","value":2}]}`},
	} {
		_, err := invoke(missing.key, missing.body)
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("missing input: %v", err)
		}
	}
	if app.calls != 0 {
		t.Fatal("invalid input called application")
	}
	body := `{"expected_version":0,"targets":[{"dimension":"department","value":2}]}`
	app.degraded = true
	first, err := invoke("request-1", body)
	if err != nil {
		t.Fatal(err)
	}
	if app.calls != 1 || app.version != 0 || first.Operation.ID == "" || first.Operation.ID != app.operationID {
		t.Fatalf("first operation: %+v", first)
	}
	replay, err := invoke("request-1", body)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Operation.ID != first.Operation.ID || app.calls != 1 || replay.Result["version"] != first.Result["version"] {
		t.Fatalf("replayed operation: %+v", replay)
	}
	app.degraded = false
	for _, destination := range []int64{1, 2} {
		current, err := r.Get(ctx, 1, spec.All())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := application.Transfer(ctx, "move", 1, current.Version, []auth.Assignment{{Dimension: "department", Value: destination}}); err != nil {
			t.Fatal(err)
		}
	}
	// 容量淘汰使键失效，等价于幂等存储过期后的重新执行边界。
	_, err = runner.Execute(ctx, &operation.Spec{Type: "other", Mode: operation.ModeTracked, IdempotencyKey: "evict"}, func(context.Context) (*operation.Result, error) { return &operation.Result{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = invoke("request-1", body)
	if !errors.Is(err, errors.Concurrency) || app.calls != 2 || app.version != 0 || app.operationID == first.Operation.ID {
		t.Fatalf("expired old request: calls=%d version=%d error=%v", app.calls, app.version, err)
	}
}
