package main

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
