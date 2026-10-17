package version

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.21.2", "1.21.2", 0},
		{"1.9.2+be034275b55d", "1.9.2", 0},
		{"1.9.3", "1.9.2+be034275b55d", 1},
		{"0.7.6-nbxyz9", "0.7.6-nbxyz24", -1},
		{"0.7.6-nbxyz24", "0.7.6-nbxyz9", 1},
		{"1.2.3-rc1", "1.2.3", -1},
		{"1.2.3-rc.10", "1.2.3-rc.2", 1},
		{"1.2.3-ls45", "1.2.3-ls46", -1},
		{"2023.9.18", "1.9.2", 1},
	}
	for _, tt := range tests {
		got, ok := Compare(tt.a, tt.b)
		if !ok || got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d", tt.a, tt.b, got, ok, tt.want)
		}
	}
	if _, ok := Compare("RELEASE.2024-12-18T13-15-44Z", "1.0.0"); ok {
		t.Error("expected unparsable tag to report ok=false")
	}
}

func TestSeverity(t *testing.T) {
	tests := []struct {
		current, latest string
		want            int
	}{
		{"1.0.0", "2.0.0", SeverityMajor},
		{"1.0.0", "1.1.0", SeverityMinor},
		{"1.0.0", "1.0.1", SeverityPatch},
		{"1.2.3-rc1", "1.2.3", SeverityPatch},
		{"1.2.3-ls45", "1.2.3-ls46", SeverityPatch},
		{"1.2.3", "1.2.3", SeverityNone},
		{"1.2.3", "1.2.2", SeverityNone},
		{"0.7.6-nbxyz24", "0.7.6-nbxyz9", SeverityNone},
		{"v1.21.2", "1.21.2", SeverityNone},
		{"abc", "1.0.0", SeverityUnknown},
	}
	for _, tt := range tests {
		if got := Severity(tt.current, tt.latest); got != tt.want {
			t.Errorf("Severity(%q, %q) = %d; want %d", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestIsNewer_UnparsableFallsBackToInequality(t *testing.T) {
	if !IsNewer("RELEASE.2024-01-01", "RELEASE.2024-02-01") {
		t.Error("different unparsable versions should be treated as newer")
	}
	if IsNewer("same", "same") {
		t.Error("identical versions are not newer")
	}
}

func TestLatest(t *testing.T) {
	got := Latest([]string{"1.2.0", "1.10.0", "2.0.0-rc.1", "1.9.0", "latest", "1.10.0_abc"})
	if got != "1.10.0" && got != "1.10.0+abc" {
		t.Errorf("Latest = %q", got)
	}
	if Latest([]string{"2.0.0-rc.1"}) != "" {
		t.Error("expected no stable version")
	}
}

func TestSameVariant(t *testing.T) {
	tests := []struct {
		current, candidate string
		want               bool
	}{
		{"1.6.39-alpine", "1.6.45", false},
		{"1.6.39-alpine", "1.6.45-alpine", true},
		{"1.27.3-alpine3.19", "1.27.5-alpine3.20", true},
		{"0.7.6-nbxyz24", "0.7.7-nbxyz1", true},
		{"1.2.3", "1.2.4", true},
		{"1.2.3-rc1", "1.2.3", true},
		{"RELEASE.2024", "RELEASE.2025", true},
	}
	for _, tt := range tests {
		if got := SameVariant(tt.current, tt.candidate); got != tt.want {
			t.Errorf("SameVariant(%q, %q) = %v; want %v", tt.current, tt.candidate, got, tt.want)
		}
	}
}
