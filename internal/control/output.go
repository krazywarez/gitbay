package control

// Named payloads for the commands other surfaces decode. A command used
// to declare its output inline, so the web type-asserted map keys and
// nothing tied what a command emits to what a client reads (#126).

// Created is what issue create, issue comment and mr comment emit.
type Created struct {
	Number int64 `json:"number"`
}

// MRCreated is what mr create emits.
type MRCreated struct {
	Number    int64     `json:"number"`
	HeadSHA   string    `json:"head_sha"`
	StackedOn *stackRef `json:"stacked_on,omitempty"`
}

// IssueShow is issue show's payload: the issue and its comments.
type IssueShow struct {
	issueOut
	Comments []commentOut `json:"comments,omitempty"`
}

// MRShow is mr show's payload.
type MRShow struct {
	mrOut
	Checks            []CheckOut   `json:"checks,omitempty"`
	Combined          string       `json:"checks_combined,omitempty"`
	UnresolvedThreads int          `json:"unresolved_threads,omitempty"`
	Commits           []CommitOut  `json:"commits,omitempty"`
	Comments          []commentOut `json:"comments,omitempty"`
	Reviews           []ReviewOut  `json:"reviews,omitempty"`
}

// ReviewOut is one review on a merge request.
type ReviewOut struct {
	Reviewer  string `json:"reviewer"`
	Verdict   string `json:"verdict"`
	Stale     bool   `json:"stale"`
	CreatedAt string `json:"created_at"`
}

// CheckOut is one commit status on a merge request head.
type CheckOut struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Duration  string `json:"duration,omitempty"` // CI checks only, once finished
}

// CommitOut is one commit a merge request carries.
type CommitOut struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}
