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
	if _, err := service.deposit(stranger, Deposit{AccountID: 888, Amount: 1000}); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("unauthorized deposit: %v", err)
	}
	if _, err := service.read(stranger, 888); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("unauthorized read: %v", err)
	}
	if _, err := service.deposit(ctx, Deposit{AccountID: 999, Amount: 1000}); !errors.Is(err, errors.Forbidden) {
		t.Fatalf("wrong account: %v", err)
	}
	funded, err := service.deposit(ctx, Deposit{AccountID: 888, Amount: 1000})
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
