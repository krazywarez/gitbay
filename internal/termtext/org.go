package termtext

// Org renders org-mode source. Task 3.2 replaces this stub.
func Org(src string, o Options) string {
	return renderOrg(src, &out{o: o})
}

// renderOrg is a stub until Task 3.2.
func renderOrg(src string, w *out) string { return src }
