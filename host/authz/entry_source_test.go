package authz_test

import (
	"context"
	hostauthz "gochen-runtime/host/authz"
	"gochen/auth"
	"gochen/errors"
	"gochen/messaging/command"
	"testing"
)

func TestCommandEntriesRequireBindingsAndRejectHandlerReplacement(t *testing.T) {
	executor := command.NewCommandExecutor()
	if err := executor.RegisterHandler("send", func(context.Context, *command.Command) error { return nil }); err != nil {
		t.Fatal(err)
	}
	source, err := hostauthz.NewHandlerEntries("command", executor)
	if err != nil {
		t.Fatal(err)
	}
	registry := hostauthz.NewEntryRegistry(source)
	if err := registry.ValidateEntries(t.Context(), nil); !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("missing binding=%v", err)
	}
	if len(registry.Report()) != 1 || registry.Report()[0].Class != auth.EntryInvalid {
		t.Fatalf("report=%+v", registry.Report())
	}
	if err := registry.Bind(auth.EntryBinding{Entry: auth.Entry{Kind: "command", Key: "send"}, ResourceAccess: auth.ResourceAccessNone, Source: "transport probe", AllowReason: "internal liveness"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateEntries(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler("send", func(context.Context, *command.Command) error { return nil }); !errors.Is(err, errors.FailedPrecondition) {
		t.Fatalf("replacement=%v", err)
	}
}
