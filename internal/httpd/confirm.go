package httpd

import (
	"net/http"
	"strings"
)

// confirmed reports whether the form typed want into its confirm field.
// It guards controls that destroy data nothing else holds; the person
// is already authorised, so this is a check against a slip, not a
// permission.
func confirmed(r *http.Request, want string) (bool, string) {
	if strings.TrimSpace(r.FormValue("confirm")) == want {
		return true, ""
	}
	return false, "type " + want + " to confirm"
}
