package sandbox

import (
	"encoding/base64"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	fileIDSeparator = "\x00"
	fileIDVersion   = "1"
)

// EncodeFile turns a workspace-relative reference into an opaque, versioned
// file id: "rbx_" plus base64url of (version, identity key, kind, path).
func EncodeFile(key, kind, relPath string) string {
	raw := strings.Join([]string{fileIDVersion, key, kind, relPath}, fileIDSeparator)
	return FileIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeFile parses a file id. There is exactly one format; other prefixes
// and field counts are rejected rather than translated.
func DecodeFile(fileID string) (*FileRef, error) {
	if !strings.HasPrefix(fileID, FileIDPrefix) {
		return nil, BadRequest("invalid file id")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(fileID, FileIDPrefix))
	if err != nil {
		return nil, BadRequest("invalid file id")
	}
	parts := strings.Split(string(raw), fileIDSeparator)
	if len(parts) != 4 || parts[0] != fileIDVersion {
		return nil, BadRequest("invalid file id")
	}
	ref := &FileRef{Key: parts[1], Kind: parts[2], Path: parts[3]}
	if !ValidKey(ref.Key) {
		return nil, BadRequest("invalid file id")
	}
	if !validKind(ref.Kind) {
		return nil, BadRequest("invalid file kind")
	}
	clean, err := NormalizeRel(ref.Path)
	if err != nil {
		return nil, err
	}
	ref.Path = clean
	return ref, nil
}

func validKind(kind string) bool {
	switch kind {
	case KindFiles, KindOut, KeepDir:
		return true
	default:
		return false
	}
}

// ValidIdentity reports whether s can be used as an identity.
// It is deliberately strict: sanitising identity values silently maps
// distinct callers onto one workspace, which would break isolation.
func ValidIdentity(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// NormalizeRel validates and normalizes a workspace-relative path. Absolute
// paths, parent traversal and control characters are rejected.
func NormalizeRel(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	if p == "" || p == "." {
		return "", BadRequest("path is required")
	}
	if strings.HasPrefix(p, "/") {
		return "", BadRequest("absolute paths are not allowed")
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", BadRequest("drive-qualified paths are not allowed")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", BadRequest("path contains control characters")
		}
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", BadRequest("path escapes the workspace")
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", BadRequest("invalid path")
		}
	}
	return clean, nil
}

// SanitizeFileName reduces an uploaded file name to a single safe path
// segment. It is only used for uploads, where flattening is the intent.
func SanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.ReplaceAll(name, "..", "")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	return truncateName(name, 200)
}

func truncateName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	if cut == 0 {
		return "file"
	}
	return name[:cut]
}
