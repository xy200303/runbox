package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/xy200303/runbox/sdk/go"
)

func TestIdentityLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runbox.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	identity := sandbox.ScopedIdentity("tenant-a", "alice")
	key, err := identity.CanonicalKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	record := FromSandbox(identity, 20001, key)
	if err := st.Create(record); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.Get(key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UID != 20001 || got.Namespace != "tenant-a" || got.Principal != "alice" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if err := st.Touch(key); err != nil {
		t.Fatalf("touch: %v", err)
	}
	all, err := st.All()
	if err != nil || len(all) != 1 {
		t.Fatalf("all: %+v, %v", all, err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The assignment survives a reopen.
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	got, err = st.Get(key)
	if err != nil || got.UID != 20001 {
		t.Fatalf("reopened record: %+v, %v", got, err)
	}
	if _, err := st.Get("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing identity error %v", err)
	}
}

func TestUniqueUID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "runbox.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	a := FromSandbox(sandbox.TokenIdentity("a"), 20000, "a")
	b := FromSandbox(sandbox.TokenIdentity("b"), 20000, "b")
	if err := st.Create(a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if err := st.Create(b); err == nil {
		t.Fatal("duplicate UID was accepted")
	}
}
