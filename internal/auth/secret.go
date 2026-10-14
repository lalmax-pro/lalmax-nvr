package auth

import "crypto/subtle"

// ConstantTimeEqual compares non-empty secrets without data-dependent
// comparison of their contents.
func ConstantTimeEqual(got, want string) bool {
	return got != "" && len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
