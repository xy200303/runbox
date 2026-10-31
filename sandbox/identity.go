package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const identityVersion = "v1"

// Identity is the caller identity. Exactly one mode is used:
//
//   - Token: a single opaque identifier, for callers that already have one;
//   - Scoped: Namespace + Principal, for tenant/user models.
//
// The canonical storage key is derived from the mode and is deliberately not
// reversible. Namespace and Principal are kept as metadata for tenant-level
// operations; Token is never persisted.
type Identity struct {
	Token     string `json:"token,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Principal string `json:"principal,omitempty"`
}

// TokenIdentity builds a single-token identity.
func TokenIdentity(token string) Identity {
	return Identity{Token: strings.TrimSpace(token)}
}

// ScopedIdentity builds a namespace + principal identity.
func ScopedIdentity(namespace, principal string) Identity {
	return Identity{
		Namespace: strings.TrimSpace(namespace),
		Principal: strings.TrimSpace(principal),
	}
}

// Mode reports "token", "scoped", or "" for an unset identity.
func (i Identity) Mode() string {
	switch {
	case strings.TrimSpace(i.Token) != "":
		return "token"
	case strings.TrimSpace(i.Namespace) != "" || strings.TrimSpace(i.Principal) != "":
		return "scoped"
	default:
		return ""
	}
}

// Validate enforces exactly one identity mode and the per-mode charset.
func (i Identity) Validate() *Error {
	token := strings.TrimSpace(i.Token)
	namespace := strings.TrimSpace(i.Namespace)
	principal := strings.TrimSpace(i.Principal)

	if token != "" && (namespace != "" || principal != "") {
		return BadRequest("ambiguous identity: use either a token or namespace + principal, not both")
	}
	if token != "" {
		if !validToken(token) {
			return BadRequest("invalid identity token: use 1-256 visible ASCII characters without spaces")
		}
		return nil
	}
	if namespace == "" || principal == "" {
		return BadRequest("missing identity: use a token or both namespace and principal")
	}
	if !ValidIdentity(namespace) || !ValidIdentity(principal) {
		return BadRequest("invalid namespace or principal: use 1-128 characters from [A-Za-z0-9._-]")
	}
	return nil
}

// CanonicalKey returns the fixed-length, path-safe storage key. The encoding is
// versioned and NUL-delimited; identity parts cannot contain NUL.
func (i Identity) CanonicalKey() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write([]byte("runbox.identity." + identityVersion + "\x00"))
	if token := strings.TrimSpace(i.Token); token != "" {
		hash.Write([]byte("token\x00"))
		hash.Write([]byte(token))
	} else {
		hash.Write([]byte("scoped\x00"))
		hash.Write([]byte(strings.TrimSpace(i.Namespace)))
		hash.Write([]byte{0})
		hash.Write([]byte(strings.TrimSpace(i.Principal)))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ValidKey reports whether key is a canonical storage key produced by
// CanonicalKey.
func ValidKey(key string) bool {
	if len(key) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

func validToken(token string) bool {
	if token == "" || len(token) > 256 {
		return false
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
