package main

import (
	"context"
	"gochen/auth"
	"gochen/errors"
	"testing"
)

func TestAccountEntryAuthorizationAndExpectedVersion(t *testing.T) {
	service, err := newAccountService()
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := auth.WithPrincipal(context.Background(), auth.Principal{SubjectID: "demo-user"})
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := auth.WithPrincipal(context.Background(), auth.Principal{SubjectID: "stranger"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.open(stranger, Deposit{AccountID: 888, Amount: 1000}); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("unauthorized deposit: %v", err)
	}
	if _, err := service.read(stranger, 888); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("unauthorized read: %v", err)
	}
	if _, err := service.open(ctx, Deposit{AccountID: 999, Amount: 1000}); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("wrong account: %v", err)
	}
	funded, err := service.open(ctx, Deposit{AccountID: 888, Amount: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.withdraw(ctx, Withdraw{AccountID: 888, ExpectedVersion: 0, Amount: 100}); !errors.Is(err, errors.Concurrency) {
		t.Fatalf("stale command: %v", err)
	}
	loaded, err := service.read(ctx, 888)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version() != funded.Version() || loaded.Value().Balance != 1000 {
		t.Fatal("denied or stale command appended events")
	}
}

func TestExplicitApplicationUsesCompiledBindingAndReplaysBusinessFlow(t *testing.T) {
	service, err := newAccountService()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"read", "open", "deposit", "withdraw"} {
		binding, err := service.application.AuthorizationBinding(action)
		if err != nil {
			t.Fatal(err)
		}
		binding.Entry = auth.Entry{Kind: "service", Key: action}
		if _, err := auth.ValidateEntries([]auth.Entry{binding.Entry}, []auth.EntryBinding{binding}); err != nil {
			t.Fatalf("binding %s: %v", action, err)
		}
	}
	if _, err := service.application.AuthorizationBinding("undeclared"); err == nil {
		t.Fatal("undeclared binding accepted")
	}
	if _, err := service.open(context.Background(), Deposit{AccountID: 888, Amount: 1000}); !errors.Is(err, errors.Unauthorized) {
		t.Fatalf("missing principal: %v", err)
	}
	ctx, err := auth.WithPrincipal(context.Background(), auth.Principal{SubjectID: "demo-user"})
	if err != nil {
		t.Fatal(err)
	}
	funded, err := service.open(ctx, Deposit{AccountID: 888, Amount: 1000})
	if err != nil {
		t.Fatal(err)
	}
	deposited, err := service.deposit(ctx, Deposit{AccountID: 888, ExpectedVersion: funded.Version(), Amount: 500})
	if err != nil {
		t.Fatal(err)
	}
	paid, err := service.withdraw(ctx, Withdraw{AccountID: 888, ExpectedVersion: deposited.Version(), Amount: 250})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.withdraw(ctx, Withdraw{AccountID: 888, ExpectedVersion: paid.Version(), Amount: 2000}); !errors.Is(err, errors.Validation) {
		t.Fatalf("overdraft accepted: %v", err)
	}
	replayed, err := service.read(ctx, 888)
	if err != nil || replayed.Version() != 3 || replayed.Value().Balance != 1250 {
		t.Fatalf("business flow/replay mismatch: %+v %v", replayed, err)
	}
}
