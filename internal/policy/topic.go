package policy

import (
	"fmt"
	"regexp"
)

// MaxTopics caps topics per repository.
const MaxTopics = 20

var topicPat = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,34}$`)

// ValidateTopic checks a repository topic: lowercase alphanumerics and
// dashes, must start with an alphanumeric, max 35 chars.
func ValidateTopic(topic string) error {
	if !topicPat.MatchString(topic) {
		return fmt.Errorf("invalid topic %q: lowercase letters, digits, and '-' only; must start with a letter or digit; max 35 chars", topic)
	}
	return nil
}
