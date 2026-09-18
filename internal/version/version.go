package version

const Service = "tpahub"
const Current = "0.1.1"

// BuildRevision is injected by release builds with -ldflags. Development
// builds remain explicitly identifiable as unknown rather than pretending to
// correspond to a source revision.
var BuildRevision = "unknown"
