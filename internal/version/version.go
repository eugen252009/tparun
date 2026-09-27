package version

const Service = "tparun"

var Current = "0.1.5~beta5"

// BuildRevision is injected by release builds with -ldflags. Development
// builds remain explicitly identifiable as unknown rather than pretending to
// correspond to a source revision.
var BuildRevision = "unknown"
