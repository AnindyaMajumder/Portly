package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// Release builds carry a plain X.Y.Z version
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// IsReleaseVersion is true for X.Y.Z, false for development and evidence builds.
func IsReleaseVersion(version string) bool { return releaseVersion.MatchString(version) }

// IsNewerVersion compares two release versions. Anything else is never newer.
func IsNewerVersion(candidate, current string) bool {
	if !IsReleaseVersion(candidate) || !IsReleaseVersion(current) {
		return false
	}
	next, now := strings.Split(candidate, "."), strings.Split(current, ".")
	for i := range next {
		a, errA := strconv.Atoi(next[i])
		b, errB := strconv.Atoi(now[i])
		if errA != nil || errB != nil {
			return false
		}
		if a != b {
			return a > b
		}
	}
	return false
}
