package store

import "fmt"

// Reaction is one entry of the fixed reaction set (#291).
type Reaction struct {
	Name  string // what is stored and what the commands take
	Emoji string // what surfaces render
}

// Reactions is the whole set, in display order.
var Reactions = []Reaction{
	{"+1", "👍"}, {"-1", "👎"}, {"laugh", "😄"}, {"hooray", "🎉"},
	{"confused", "😕"}, {"heart", "❤️"}, {"rocket", "🚀"}, {"eyes", "👀"},
}

// ParseReaction maps a name or its emoji to the stored name.
func ParseReaction(v string) (string, bool) {
	for _, r := range Reactions {
		if v == r.Name || v == r.Emoji {
			return r.Name, true
		}
	}
	if v == "❤" { // heart without the emoji variation selector
		return "heart", true
	}
	return "", false
}

// ReactionEmoji is the emoji for a stored name.
func ReactionEmoji(name string) string {
	for _, r := range Reactions {
		if r.Name == name {
			return r.Emoji
		}
	}
	return name
}

// ReactionCount is how many people gave one reaction to one item, and
// whether the viewer is among them.
type ReactionCount struct {
	Reaction string
	Count    int
	Me       bool
}

// reactionTables names the tables for a noun, "issue" or "mr".
func reactionTables(noun string) (reactions, threadCol, comments string, err error) {
	switch noun {
	case "issue":
		return "issue_reactions", "issue_id", "issue_comments", nil
	case "mr":
		return "mr_reactions", "mr_id", "mr_comments", nil
	}
	return "", "", "", fmt.Errorf("no reactions on %q", noun)
}

// ReactionTarget checks that commentID is a conversation comment of the
// thread and not a system entry. commentID 0 is the thread itself.
func (s *Store) ReactionTarget(noun string, threadID, commentID int64) error {
	if commentID == 0 {
		return nil
	}
	_, threadCol, comments, err := reactionTables(noun)
	if err != nil {
		return err
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM "+comments+
		" WHERE id = ? AND "+threadCol+" = ? AND kind != 'system'",
		commentID, threadID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetReaction adds or removes one user's reaction. Doing either twice is
// not an error. commentID 0 targets the thread's own body.
func (s *Store) SetReaction(noun string, threadID, commentID, userID int64, reaction string, on bool) error {
	table, threadCol, _, err := reactionTables(noun)
	if err != nil {
		return err
	}
	var cid any
	cond := "comment_id IS NULL"
	if commentID != 0 {
		cid, cond = commentID, "comment_id = ?"
	}
	if !on {
		args := []any{threadID, userID, reaction}
		if commentID != 0 {
			args = append(args, cid)
		}
		_, err := s.DB.Exec("DELETE FROM "+table+" WHERE "+threadCol+" = ? AND user_id = ? AND reaction = ? AND "+cond, args...)
		return err
	}
	_, err = s.DB.Exec("INSERT INTO "+table+" ("+threadCol+", comment_id, user_id, reaction) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING",
		threadID, cid, userID, reaction)
	return err
}

// ReactionCounts returns the counts for a thread and its comments, keyed
// by comment id, with 0 for the thread itself. Each list is in the
// order of Reactions and holds only reactions somebody gave. viewerID 0
// marks none as the viewer's.
func (s *Store) ReactionCounts(noun string, threadID, viewerID int64) (map[int64][]ReactionCount, error) {
	table, threadCol, _, err := reactionTables(noun)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`SELECT COALESCE(comment_id, 0), reaction, COUNT(*),
		COALESCE(MAX(user_id = ?), 0) FROM `+table+` WHERE `+threadCol+` = ?
		GROUP BY comment_id, reaction`, viewerID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[int64]map[string]ReactionCount{}
	for rows.Next() {
		var cid int64
		var rc ReactionCount
		var me int
		if err := rows.Scan(&cid, &rc.Reaction, &rc.Count, &me); err != nil {
			return nil, err
		}
		rc.Me = me != 0 && viewerID != 0
		if by[cid] == nil {
			by[cid] = map[string]ReactionCount{}
		}
		by[cid][rc.Reaction] = rc
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[int64][]ReactionCount{}
	for cid, m := range by {
		for _, r := range Reactions {
			if rc, ok := m[r.Name]; ok {
				out[cid] = append(out[cid], rc)
			}
		}
	}
	return out, nil
}
