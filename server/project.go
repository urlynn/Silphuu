package main

import "strings"

// Project identity.
// Distinct from siteName() in site.go: that is the *deployment's* name, read from
// its own configuration. These constants are the *upstream project's* identity,
// shown in the footer attribution.
//
// They are deliberately constants rather than configuration keys. Attribution is
// meant to point at this project, and a deployment should not be able to repoint
// it by editing a config file. It is not enforced — a deployment that wants the
// line gone replaces templates/footer.html through the overlay, which is the same
// thing as forking a badge off any other open-source project. Pretending
// otherwise would only add complexity and inconvenience honest users.

const (
	// ProjectName is the name shown in the footer attribution.
	ProjectName = "Silphuu"

	// ProjectURL is the upstream repository linked from the footer.
	//
	// Attribution is deliberate: publishing the repository links the project to this identity.
	ProjectURL = "https://github.com/urlynn/Silphuu"
)

// projectURLIsValid reports whether ProjectURL can be used as a footer link.
//
// A fork should keep this pointing at the upstream project — that is what
// attribution means — so this only catches an empty or malformed value, not a
// "wrong" one.
func projectURLIsValid() bool {
	return strings.HasPrefix(ProjectURL, "https://") && len(ProjectURL) > len("https://")
}
