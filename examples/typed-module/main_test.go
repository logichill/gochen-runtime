package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/engine"
	"gochen-runtime/quick"
	"gochen/auth"
)

func TestTypedModuleHTTPAndCancellation(t *testing.T) {
	database, ormEngine, err := openNotes(t.Context())
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	entries := authz.NewEntryRegistry()
	runner, err := quick.New(config.WithName("typed-notes-test"), config.WithAddress("127.0.0.1"), config.WithPort(port),
		config.WithEntryValidator(entries), config.WithShutdownTimeout(2*time.Second)).
		Use(demoIdentity).Modules(newNotes(ormEngine)).Build()
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
			t.Error("host did not stop after cancellation")
		}
		if runner.State() != engine.StateStopped {
			t.Errorf("state = %v", runner.State())
		}
		// 注入连接不属于 Host，关闭 Host 后仍然可以访问它。
		if _, err := database.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Errorf("host closed external database: %v", err)
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d/api/v1", port)
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(base + "/health")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener unavailable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	request := func(method, path, role, body string, status int) json.RawMessage {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
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
			t.Fatalf("%s %s = %d, want %d: %s", method, path, response.StatusCode, status, payload)
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	request("GET", "/notes", "", "", 401)
	request("POST", "/notes", "reader", `{"id":"blocked","title":"forbidden"}`, 403)
	request("GET", "/notes/blocked", "reader", "", 404)
	request("POST", "/notes", "editor", `{"id":"note-1","title":"first"}`, 200)
	var note Note
	if err := json.Unmarshal(request("GET", "/notes/note-1", "reader", "", 200), &note); err != nil {
		t.Fatal(err)
	}
	if note.Title != "first" || note.Version != 0 {
		t.Fatalf("created note = %+v", note)
	}
	request("PUT", "/notes/note-1", "editor", `{"title":"second","version":0}`, 200)
	request("PUT", "/notes/note-1", "editor", `{"title":"stale","version":0}`, 409)
	if err := json.Unmarshal(request("GET", "/notes/note-1", "reader", "", 200), &note); err != nil {
		t.Fatal(err)
	}
	if note.Title != "second" || note.Version != 1 {
		t.Fatalf("updated note = %+v", note)
	}
	request("GET", "/notes", "reader", "", 200)
	request("DELETE", "/notes/note-1", "editor", "", 200)
	request("GET", "/notes/note-1", "reader", "", 404)
	if len(entries.Report()) == 0 {
		t.Fatal("actual routes were not checked")
	}
	for _, entry := range entries.Report() {
		if entry.Class == auth.EntryInvalid {
			t.Fatalf("invalid entry: %+v", entry)
		}
	}
}
