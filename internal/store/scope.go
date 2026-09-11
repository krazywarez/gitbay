package store

import (
	"errors"
	"strings"
)

// ErrOrgScoped is returned when a repository-level write names a label or
// milestone its org holds; the org commands manage those.
var ErrOrgScoped = errors.New("held by the org")

// scopeClause selects the label or milestone rows a repository sees: its
// own, and its org's when an org owns it. alias is the table alias in the
// query.
func scopeClause(alias string, repo Repo) (string, []any) {
	if repo.OwnerKind == "org" {
		return "(" + alias + ".repo_id = ? OR " + alias + ".org_id = ?)", []any{repo.ID, repo.OwnerID}
	}
	return alias + ".repo_id = ?", []any{repo.ID}
}

// inClause renders ids as a parenthesised placeholder list. An empty set
// yields (NULL), which matches nothing.
func inClause(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "(NULL)", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}
