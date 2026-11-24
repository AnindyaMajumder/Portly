//go:build !portly_evidence

package stopper

// EvidenceBuild is true only in builds made to test real stops.
const EvidenceBuild = false

// withEvidence changes nothing in a release.
func withEvidence(platform gates) gates { return platform }
