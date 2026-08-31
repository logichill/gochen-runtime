package initcap

import "testing"

func TestKeyIdentityDoesNotCollideByName(t *testing.T) {
	first := NewKey[string]("same")
	second := NewKey[string]("same")

	store := With(Store{}, first, "first")
	if got, ok := Get(store, second); ok {
		t.Fatalf("expected same-name key to miss, got %q", got)
	}
	if got, ok := Get(store, first); !ok || got != "first" {
		t.Fatalf("expected original key to resolve first, got %q ok=%v", got, ok)
	}
}

func TestZeroKeyDoesNotMutateStore(t *testing.T) {
	var key Key[string]
	store := With(Store{}, key, "value")
	if got, ok := Get(store, key); ok {
		t.Fatalf("expected zero key to miss, got %q", got)
	}
}

func TestNewStoreAppliesSettersOnce(t *testing.T) {
	first := NewKey[string]("first")
	second := NewKey[int]("second")

	store := NewStore(Set(first, "value"), Set(second, 42), nil)
	if got, ok := Get(store, first); !ok || got != "value" {
		t.Fatalf("expected first value, got %q ok=%v", got, ok)
	}
	if got, ok := Get(store, second); !ok || got != 42 {
		t.Fatalf("expected second value, got %d ok=%v", got, ok)
	}
}
