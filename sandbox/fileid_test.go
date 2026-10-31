package sandbox

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustKey(t *testing.T, identity Identity) string {
	t.Helper()
	key, err := identity.CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key: %v", err)
	}
	return key
}

func TestEncodeDecodeFile(t *testing.T) {
	key := mustKey(t, ScopedIdentity("tenant-1", "user_1"))
	fileID := EncodeFile(key, KindOut, "报表/八月.xlsx")
	ref, err := DecodeFile(fileID)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ref.Key != key || ref.Kind != KindOut || ref.Path != "报表/八月.xlsx" {
		t.Fatalf("unexpected ref: %+v", ref)
	}
	if got := EncodeFile(ref.Key, ref.Kind, ref.Path); got != fileID {
		t.Fatalf("round trip changed id: %s != %s", got, fileID)
	}
}

func TestDecodeRejectsFiveFieldFileID(t *testing.T) {
	key := mustKey(t, TokenIdentity("ns.user"))
	raw := strings.Join([]string{"1", key, KeepDir, "report.txt", "extra"}, "\x00")
	id := FileIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
	if _, err := DecodeFile(id); err == nil {
		t.Fatal("five-field file ids must be rejected")
	}
}

func TestDecodeRejectsForeignPrefix(t *testing.T) {
	key := mustKey(t, TokenIdentity("ns.user"))
	raw := strings.Join([]string{"1", key, KindFiles, "x"}, "\x00")
	id := "sbx_" + base64.RawURLEncoding.EncodeToString([]byte(raw))
	if _, err := DecodeFile(id); err == nil {
		t.Fatal("a foreign file-id prefix must be rejected")
	}
}

func TestDecodeFileRejectsBadInput(t *testing.T) {
	key := mustKey(t, TokenIdentity("ns.user"))
	cases := []string{
		"",
		"nope",
		FileIDPrefix + "!!!",
		EncodeFile(key, "bad-kind", "x"),
		EncodeFile(key, KindOut, "../escape"),
		EncodeFile("bad-key", KindOut, "x"),
	}
	for _, input := range cases {
		if _, err := DecodeFile(input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
}

func TestIdentityCanonicalKey(t *testing.T) {
	tokenKey := mustKey(t, TokenIdentity("01J-abc"))
	scopedKey := mustKey(t, ScopedIdentity("tenant-a", "user-42"))
	if tokenKey == scopedKey {
		t.Fatal("different identity modes must produce different keys")
	}
	if tokenKey != mustKey(t, TokenIdentity("01J-abc")) {
		t.Fatal("token key must be stable")
	}
	if !ValidKey(tokenKey) || !ValidKey(scopedKey) {
		t.Fatal("canonical keys must be valid")
	}
	if _, err := ScopedIdentity("tenant-a", "").CanonicalKey(); err == nil {
		t.Fatal("partial scoped identity must be rejected")
	}
	if _, err := (Identity{Token: "a", Namespace: "b"}).CanonicalKey(); err == nil {
		t.Fatal("ambiguous identity must be rejected")
	}
	if _, err := TokenIdentity("a b").CanonicalKey(); err == nil {
		t.Fatal("token with a space must be rejected")
	}
}

func TestNormalizeRel(t *testing.T) {
	valid := map[string]string{
		"out/a.txt":   "out/a.txt",
		"./out/a.txt": "out/a.txt",
		"out//a.txt":  "out/a.txt",
		`out\a.txt`:   "out/a.txt",
		"a/../b":      "b",
		"a/./b":       "a/b",
		"报表/八月.xlsx":  "报表/八月.xlsx",
	}
	for input, want := range valid {
		got, err := NormalizeRel(input)
		if err != nil {
			t.Fatalf("NormalizeRel(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeRel(%q)=%q want %q", input, got, want)
		}
	}
	invalid := []string{
		"", ".", "..", "../x", "/etc/passwd", `..\x`, `C:\x`, "a/../../b",
		"a\x00b", "a\rb",
	}
	for _, input := range invalid {
		if _, err := NormalizeRel(input); err == nil {
			t.Fatalf("expected error for %q", input)
		}
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": "passwd",
		"a/b\\c.txt":       "c.txt",
		"   ":              "file",
		"正常.xlsx":          "正常.xlsx",
	}
	for input, want := range cases {
		if got := SanitizeFileName(input); got != want {
			t.Fatalf("SanitizeFileName(%q)=%q want %q", input, got, want)
		}
	}
}

func TestValidIdentity(t *testing.T) {
	for _, value := range []string{"t1", "tenant-1", "user_1", "a.b", "UPPER"} {
		if !ValidIdentity(value) {
			t.Fatalf("%q should be valid", value)
		}
	}
	for _, value := range []string{"", ".", "..", "a/b", "a b", "a\\b", strings.Repeat("a", 129)} {
		if ValidIdentity(value) {
			t.Fatalf("%q should be invalid", value)
		}
	}
}

func TestTruncateTail(t *testing.T) {
	short := "short"
	if got := TruncateTail(short, 100); got != short {
		t.Fatal("short text should be untouched")
	}
	long := strings.Repeat("中", 500)
	got := TruncateTail(long, 100)
	if !utf8.ValidString(got) {
		t.Fatal("truncation broke UTF-8")
	}
	if len(got) >= len(long) || !strings.Contains(got, "truncated") {
		t.Fatalf("expected truncation marker, got len %d", len(got))
	}
}
