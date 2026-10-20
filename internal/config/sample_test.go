package config

import (
	"path/filepath"
	"testing"
)

// TestSampleConfigLoads keeps conf/go/data-info.yml.sample honest. A sample that no longer
// parses, or that misses a required setting, is worse than none: it is what an operator
// copies when standing up a new environment.
func TestSampleConfigLoads(t *testing.T) {
	c, err := Load(Settings{
		ConfigPath: filepath.Join("..", "..", "conf", "go", "data-info.yml.sample"),
		DotEnvPath: filepath.Join(t.TempDir(), "absent.env"),
		EnvPrefix:  "DATAINFOSAMPLETEST_",
	})
	if err != nil {
		t.Fatalf("the sample configuration does not load: %v", err)
	}

	if c.Port != DefaultPort {
		t.Errorf("port = %d, want %d", c.Port, DefaultPort)
	}
	if c.BadChars != DefaultBadChars {
		t.Errorf("badchars = %q, want %q; the sample must spell the control characters correctly",
			c.BadChars, DefaultBadChars)
	}
	if len(c.AnonFiles.Mappings) != 2 {
		t.Errorf("anonfiles.mappings has %d entries, want 2", len(c.AnonFiles.Mappings))
	}
}
