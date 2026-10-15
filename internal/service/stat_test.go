package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestStatMD5Serialisation pins the wire shape: terrain's response schema requires md5 on
// every file, so an empty checksum has to arrive as "md5": "" rather than not at all.
func TestStatMD5Serialisation(t *testing.T) {
	tests := []struct {
		name string
		md5  *string
		want string
	}{
		{"absent", nil, ""},
		{"empty", new(""), `"md5":""`},
		{"present", new("abc123"), `"md5":"abc123"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(Stat{Path: "/z/home/u/f", MD5: tt.md5})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			has := strings.Contains(string(encoded), `"md5"`)
			if tt.want == "" && has {
				t.Errorf("%s has md5, want none", encoded)
			}
			if tt.want != "" && !strings.Contains(string(encoded), tt.want) {
				t.Errorf("%s does not contain %s", encoded, tt.want)
			}
		})
	}
}
