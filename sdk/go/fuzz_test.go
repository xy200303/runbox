package sandbox

import (
	"strings"
	"testing"
)

func FuzzNormalizeRel(f *testing.F) {
	for _, seed := range []string{
		"out/a.txt",
		"../x",
		"/etc/passwd",
		"",
		`a\b`,
		"a/../b",
		"报表/八月.xlsx",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		normalized, err := NormalizeRel(input)
		if err != nil {
			return
		}
		if strings.HasPrefix(normalized, "/") {
			t.Fatalf("absolute path escaped: %q", normalized)
		}
		for _, segment := range strings.Split(normalized, "/") {
			if segment == ".." {
				t.Fatalf("traversal escaped: %q", normalized)
			}
		}
	})
}

func FuzzSanitizeFileName(f *testing.F) {
	f.Add("report.xlsx")
	f.Add("../../etc/passwd")
	f.Add("   ")
	f.Add(`a/b\c.txt`)
	f.Fuzz(func(t *testing.T, input string) {
		name := SanitizeFileName(input)
		if name == "" {
			t.Fatal("sanitized name is empty")
		}
		if strings.ContainsAny(name, `/\`) {
			t.Fatalf("sanitized name still has a separator: %q", name)
		}
	})
}

func FuzzValidIdentity(f *testing.F) {
	for _, seed := range []string{"ns", "user-1", "a/b", "", "..", strings.Repeat("a", 65)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		valid := ValidIdentity(input)
		if valid {
			if input == "" || input == "." || input == ".." {
				t.Fatalf("accepted special identity %q", input)
			}
			for _, r := range input {
				switch {
				case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
					r == '-', r == '_', r == '.':
				default:
					t.Fatalf("accepted identity with %q", r)
				}
			}
		}
	})
}
