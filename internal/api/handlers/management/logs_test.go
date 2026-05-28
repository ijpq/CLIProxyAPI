package management

import "testing"

func TestExtractRequestLogID(t *testing.T) {
	cases := []struct {
		name    string
		wantID  string
		wantOK  bool
		comment string
	}{
		{"v1-messages-2025-12-23T195811-a1b2c3d4.log", "a1b2c3d4", true, "normal request log"},
		{"error-v1-messages-2025-12-23T195811-deadbeef.log", "deadbeef", true, "error-prefixed log"},
		{"v1-responses-2025-12-23T195811-00FFaa11.log", "00FFaa11", true, "mixed-case hex id"},
		{"main.log", "", false, "app log file"},
		{"main.log.1", "", false, "numeric-rotated app log"},
		{"main-2025-12-23T195811.log", "", false, "no trailing id segment"},
		{"v1-messages-2025-12-23T195811-not-hex.log", "", false, "trailing segment not hex"},
		{"v1-messages-2025-12-23T195811-.log", "", false, "empty id segment"},
		{"random.txt", "", false, "non-log extension"},
	}

	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			gotID, gotOK := extractRequestLogID(tc.name)
			if gotOK != tc.wantOK || gotID != tc.wantID {
				t.Fatalf("extractRequestLogID(%q) = (%q, %v), want (%q, %v)",
					tc.name, gotID, gotOK, tc.wantID, tc.wantOK)
			}
		})
	}
}
