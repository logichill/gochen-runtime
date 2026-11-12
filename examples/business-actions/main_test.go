package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	"gochen/auth"
	"gochen/db"
	"gochen/errors"
)

func TestApprovalHTTPAndDirectShareAuthorization(t *testing.T) {
	database, approver, request := startOrders(t)
	baseline := snapshotOrders(t, database)
	for _, test := range []struct {
		name   string
		role   string
		input  ApproveOrder
		status int
		code   errors.ErrorCode
	}{
		{"unauthenticated", "", ApproveOrder{101, 7, "Budget confirmed"}, 401, errors.Unauthorized},
		{"permission denied", "reader", ApproveOrder{101, 7, "Budget confirmed"}, 403, errors.Forbidden},
		{"outside department", "approver", ApproveOrder{103, 7, "Budget confirmed"}, 404, errors.NotFound},
		{"stale version", "approver", ApproveOrder{101, 6, "Budget confirmed"}, 409, errors.Concurrency},
		{"invalid transition", "approver", ApproveOrder{104, 8, "Approve twice"}, 409, errors.Conflict},
		{"empty reason", "approver", ApproveOrder{101, 7, "  "}, 400, errors.InvalidInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"expected_version": test.input.ExpectedVersion, "reason": test.input.Reason})
			if err != nil {
				t.Fatal(err)
			}
			request(t, test.input.ID, test.role, string(body), test.status)
			assertOrdersUnchanged(t, database, baseline)
			ctx, err := demoContext(t.Context(), test.role)
			if err != nil {
				t.Fatal(err)
			}
			result, err := approver.Approve(ctx, test.input)
			if errors.Code(err) != test.code || result != (Approval{}) {
				t.Fatalf("direct approval = %+v, %v; want zero result and %s", result, err, test.code)
			}
			assertOrdersUnchanged(t, database, baseline)
		})
	}
	request(t, 101, "approver", `{"reason":"Missing version"}`, http.StatusBadRequest)
	assertOrdersUnchanged(t, database, baseline)

	var viaHTTP Approval
	if err := json.Unmarshal(request(t, 101, "approver", `{"expected_version":7,"reason":" Budget confirmed "}`, http.StatusOK), &viaHTTP); err != nil {
		t.Fatal(err)
	}
	ctx, err := demoContext(t.Context(), "approver")
	if err != nil {
		t.Fatal(err)
	}
	viaDirect, err := approver.Approve(ctx, ApproveOrder{102, 7, "Budget confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	if viaHTTP != (Approval{101, 8, "approved"}) || viaDirect != (Approval{102, 8, "approved"}) {
		t.Fatalf("approvals differ: HTTP=%+v direct=%+v", viaHTTP, viaDirect)
	}
	stored := snapshotOrders(t, database)
	for _, id := range []int64{101, 102} {
		order := stored[id]
		if order.Status != "approved" || order.Version != 8 || order.Reason != "Budget confirmed" || order.DepartmentID != 10 {
			t.Fatalf("stored order %d = %+v", id, order)
		}
	}
	// HTTP 已提交的版本对直接调用立即可见，旧输入不能重放为第二次成功。
	if _, err := approver.Approve(ctx, ApproveOrder{101, 7, "Repeat"}); errors.Code(err) != errors.Concurrency {
		t.Fatalf("replayed HTTP input = %v", err)
	}
	assertOrdersUnchanged(t, database, stored)
}

func TestApprovalRollsBackFailedSQLUpdate(t *testing.T) {
	database, approver, request := startOrders(t)
	before := snapshotOrders(t, database)
	// SQLite RAISE(FAIL) 保留本语句在失败前的变更，必须由用例事务回滚。
	// 因而该测试不是“写入前返回错误”的伪回滚测试，也不需要生产故障注入 Hook。
	_, err := database.Exec(t.Context(), `CREATE TRIGGER fail_approval AFTER UPDATE ON orders
		WHEN NEW.id = 105 BEGIN SELECT RAISE(FAIL, 'injected approval failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := demoContext(t.Context(), "approver")
	if err != nil {
		t.Fatal(err)
	}
	result, err := approver.Approve(ctx, ApproveOrder{105, 7, "Budget confirmed"})
	if err == nil || !strings.Contains(err.Error(), "injected approval failure") || result != (Approval{}) {
		t.Fatalf("failure injection = %+v, %v", result, err)
	}
	assertOrdersUnchanged(t, database, before)
	request(t, 105, "approver", `{"expected_version":7,"reason":"Budget confirmed"}`, http.StatusInternalServerError)
	assertOrdersUnchanged(t, database, before)
	if _, err := database.Exec(t.Context(), "DROP TRIGGER fail_approval"); err != nil {
		t.Fatal(err)
	}
	result, err = approver.Approve(ctx, ApproveOrder{105, 7, "Budget confirmed"})
	if err != nil || result != (Approval{105, 8, "approved"}) {
		t.Fatalf("retry after rollback = %+v, %v", result, err)
	}
}

func TestOrderApproveFailureDoesNotMutate(t *testing.T) {
	for _, order := range []Order{{Status: "approved", Reason: "Earlier approval"}, {Status: "pending"}} {
		before := order
		if err := order.Approve(" "); err == nil {
			t.Fatal("expected domain error")
		}
		if order != before {
			t.Fatalf("failed transition mutated order: %+v", order)
		}
	}
}

type approvalRequest func(t *testing.T, id int64, role, body string, status int) json.RawMessage

func startOrders(t *testing.T) (db.IDatabase, *OrderApprover, approvalRequest) {
	t.Helper()
	database, ormEngine, err := openOrders(t.Context())
	if database != nil {
		t.Cleanup(func() {
			if err := database.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	approver, orders, err := newOrders(ormEngine)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	entries := authz.NewEntryRegistry()
	runner, err := quick.New(config.WithName("business-orders-test"), config.WithAddress("127.0.0.1"), config.WithPort(port),
		config.WithEntryValidator(entries), config.WithShutdownTimeout(2*time.Second)).Use(demoIdentity).
		Modules(func() (module.IModule, error) { return orders, nil }).Build()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runner.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("host did not stop")
		}
		if err := database.Ping(context.Background()); err != nil {
			t.Errorf("host closed borrowed database: %v", err)
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", port)
	client := &http.Client{Timeout: time.Second}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(base + "/health")
		if err == nil {
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP listener unavailable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, entry := range entries.Report() {
		if entry.Class == auth.EntryInvalid {
			t.Fatalf("invalid entry binding: %+v", entry)
		}
		if entry.Action == approveAction {
			found = true
			if entry.Class != auth.EntryDeclared || entry.Plan == nil || entry.Plan.Descriptor.Resource != "order" ||
				entry.Entry.Kind != "http" || !strings.Contains(entry.Entry.Key, "/orders/") {
				t.Fatalf("unexpected approval binding: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("binding did not register approval authorization metadata")
	}
	request := func(t *testing.T, id int64, role, body string, status int) json.RawMessage {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, fmt.Sprintf("%s/orders/%d/approve", base, id), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Demo-Role", role)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("POST order %d (%s) = %d, want %d: %s", id, role, response.StatusCode, status, payload)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	return database, approver, request
}

func snapshotOrders(t *testing.T, database db.IDatabase) map[int64]Order {
	t.Helper()
	rows, err := database.Query(t.Context(), "SELECT id, version, department_id, status, reason FROM orders ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	orders := map[int64]Order{}
	for rows.Next() {
		var order Order
		if err := rows.Scan(&order.ID, &order.Version, &order.DepartmentID, &order.Status, &order.Reason); err != nil {
			t.Fatal(err)
		}
		orders[order.ID] = order
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return orders
}

func assertOrdersUnchanged(t *testing.T, database db.IDatabase, before map[int64]Order) {
	t.Helper()
	if after := snapshotOrders(t, database); !reflect.DeepEqual(before, after) {
		t.Fatalf("orders changed on failure: before=%+v after=%+v", before, after)
	}
}
