package policy

import "gitbay.org/gitbay/internal/store"

// CanReadSnippet: anyone for public and unlisted, the owner and admins
// for private. Anonymous readers have user.ID 0.
func CanReadSnippet(user store.User, sn store.Snippet) bool {
	if sn.Visibility != "private" {
		return true
	}
	return user.ID != 0 && (user.ID == sn.OwnerID || user.IsAdmin)
}

// CanWriteSnippet: the owner and admins.
func CanWriteSnippet(user store.User, sn store.Snippet) bool {
	return user.ID != 0 && (user.ID == sn.OwnerID || user.IsAdmin)
}
