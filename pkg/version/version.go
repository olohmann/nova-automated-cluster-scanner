// Package version provides tolerant version comparison for Helm chart versions and
// container image tags.
package version

import (
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Severity levels returned by Severity.
const (
	SeverityUnknown = -1 // a version could not be parsed
	SeverityNone    = 0  // latest is not newer than current
	SeverityPatch   = 1  // patch bump (also suffix/prerelease-only differences)
	SeverityMinor   = 2  // minor bump
	SeverityMajor   = 3  // major bump
)

var chunkPattern = regexp.MustCompile(`\d+|\D+`)

// Parse parses a chart version or image tag, tolerating a leading "v" and ignoring
// build metadata ("1.9.2+be034275b55d").
func Parse(s string) (*semver.Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	return semver.NewVersion(s)
}

// Compare returns -1, 0 or 1 if a is older than, equal to or newer than b. ok is false
// when either side cannot be parsed.
//
// Unlike plain semver, prerelease/suffix identifiers are compared "naturally", so that
// "0.7.6-nbxyz24" is newer than "0.7.6-nbxyz9" (semver compares them lexically).
func Compare(a, b string) (result int, ok bool) {
	va, err := Parse(a)
	if err != nil {
		return 0, false
	}
	vb, err := Parse(b)
	if err != nil {
		return 0, false
	}
	return compareParsed(va, vb), true
}

func compareParsed(a, b *semver.Version) int {
	if c := cmpUint(a.Major(), b.Major()); c != 0 {
		return c
	}
	if c := cmpUint(a.Minor(), b.Minor()); c != 0 {
		return c
	}
	if c := cmpUint(a.Patch(), b.Patch()); c != 0 {
		return c
	}
	pa, pb := a.Prerelease(), b.Prerelease()
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1
	case pb == "":
		return -1
	}
	return naturalCompare(pa, pb)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// naturalCompare compares two strings chunk-wise, comparing digit runs numerically.
func naturalCompare(a, b string) int {
	ca := chunkPattern.FindAllString(a, -1)
	cb := chunkPattern.FindAllString(b, -1)
	for i := 0; i < len(ca) && i < len(cb); i++ {
		x, y := ca[i], cb[i]
		if isDigits(x) && isDigits(y) {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				if len(x) < len(y) {
					return -1
				}
				return 1
			}
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	switch {
	case len(ca) < len(cb):
		return -1
	case len(ca) > len(cb):
		return 1
	}
	return 0
}

func isDigits(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// IsNewer reports whether latest is newer than current. When either version cannot be
// parsed it returns true as long as the strings differ, so unparsable versions are
// reported rather than silently dropped.
func IsNewer(current, latest string) bool {
	if c, ok := Compare(latest, current); ok {
		return c > 0
	}
	return latest != "" && latest != current
}

// Severity classifies the step from current to latest: SeverityMajor, SeverityMinor,
// SeverityPatch (also for suffix/prerelease-only differences), SeverityNone if latest
// is not newer, or SeverityUnknown if either version cannot be parsed.
func Severity(current, latest string) int {
	cv, err := Parse(current)
	if err != nil {
		return SeverityUnknown
	}
	lv, err := Parse(latest)
	if err != nil {
		return SeverityUnknown
	}
	if compareParsed(lv, cv) <= 0 {
		return SeverityNone
	}
	switch {
	case lv.Major() > cv.Major():
		return SeverityMajor
	case lv.Minor() > cv.Minor():
		return SeverityMinor
	default:
		return SeverityPatch
	}
}

// IsStable reports whether s parses and has no prerelease component.
func IsStable(s string) bool {
	v, err := Parse(s)
	return err == nil && v.Prerelease() == ""
}

// Latest returns the newest stable version in versions (OCI tags with "_" instead of
// "+" are accepted). It returns "" if no stable version exists.
func Latest(versions []string) string {
	best := ""
	for _, v := range versions {
		v = strings.ReplaceAll(v, "_", "+")
		if !IsStable(v) {
			continue
		}
		if best == "" || IsNewer(best, v) {
			best = v
		}
	}
	return best
}

var (
	digitPattern    = regexp.MustCompile(`\d+`)
	realPrereleases = []string{"alpha", "beta", "rc", "snapshot", "dev", "prerelease", "pre"}
)

// SameVariant reports whether candidate keeps the image variant of current.
//
// A suffix such as "-alpine" or "-ls45" marks a variant (base image, build flavour), not a
// prerelease. Proposing "1.6.45" for "1.6.39-alpine" would silently change the base image,
// and semver ranks the bare tag above any suffixed one. Variants match when their suffixes
// are equal after removing digits ("alpine3.19" and "alpine3.20" match). Unparsable
// versions, stable currents and real prereleases (alpha, beta, rc, ...) always match.
func SameVariant(current, candidate string) bool {
	cv, err := Parse(current)
	if err != nil {
		return true
	}
	tv, err := Parse(candidate)
	if err != nil {
		return true
	}
	cp := cv.Prerelease()
	if cp == "" {
		return true
	}
	lower := strings.ToLower(cp)
	for _, p := range realPrereleases {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return digitPattern.ReplaceAllString(cp, "") == digitPattern.ReplaceAllString(tv.Prerelease(), "")
}
