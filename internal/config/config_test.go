package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// requiredYAML is the smallest configuration that validates: the three settings the
// Clojure service declared with defprop-str, plus the two passwords. Everything else in a
// test that uses it is exercising a default.
const requiredYAML = `
anonfiles:
  baseurl: https://example.org/anon-files/
  mappings:
    /iplant/home/: cyverse/home/
kifshare:
  externalurl: https://example.org/dl
irods:
  password: notprod
icat:
  password: notprod
`

// withRequired merges extra YAML into requiredYAML. Naive concatenation would produce
// duplicate top-level keys, which YAML rejects, so this merges the two documents.
func withRequired(t *testing.T, extra string) string {
	t.Helper()

	var base, over map[string]any
	if err := yaml.Unmarshal([]byte(requiredYAML), &base); err != nil {
		t.Fatalf("parsing requiredYAML: %v", err)
	}
	if err := yaml.Unmarshal([]byte(extra), &over); err != nil {
		t.Fatalf("parsing extra YAML: %v", err)
	}
	merge(base, over)

	out, err := yaml.Marshal(base)
	if err != nil {
		t.Fatalf("re-marshalling: %v", err)
	}
	return string(out)
}

// merge deep-merges src into dst, so a test can override one nested field without
// restating its siblings.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := dst[k].(map[string]any); ok {
				merge(existing, sub)
				continue
			}
		}
		dst[k] = v
	}
}

func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "service.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return Load(Settings{
		ConfigPath: path,
		DotEnvPath: filepath.Join(dir, "absent.env"),
		EnvPrefix:  "DATAINFOTEST_",
	})
}

func mustLoadYAML(t *testing.T, body string) *Config {
	t.Helper()
	c, err := loadYAML(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadAppliesDefaults(t *testing.T) {
	c := mustLoadYAML(t, requiredYAML)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"port", c.Port, DefaultPort},
		{"request timeout", c.Timeouts.Request, DefaultRequestTimeout},
		{"upload timeout", c.Timeouts.Upload, DefaultUploadTimeout},
		{"community data", c.CommunityData, DefaultCommunityData},
		{"bad chars", c.BadChars, DefaultBadChars},
		{"max paths", c.MaxPathsInRequest, DefaultMaxPathsInRequest},
		{"anon user", c.AnonUser, DefaultAnonUser},
		{"download template", c.Kifshare.DownloadTemplate, DefaultKifshareDownloadTemplate},
		{"async-tasks url", c.Services.AsyncTasks, DefaultAsyncTasksBaseURL},
		{"irods host", c.IRODS.Host, DefaultIRODSHost},
		{"irods port", c.IRODS.Port, DefaultIRODSPort},
		{"irods use trash", c.IRODS.UseTrash, true},
		{"icat database", c.ICAT.Database, DefaultICATDatabase},
		{"type attribute", c.TypeDetect.TypeAttribute, DefaultTypeAttribute},
		{"ht info type", c.PathLists.HT.InfoType, DefaultHTPathListInfoType},
		{"amqp exchange", c.AMQP.Exchange.Name, DefaultAMQPExchangeName},
		{"amqp durable", c.AMQP.Exchange.Durable, true},
		{"dataone base", c.DataONE.MemberNodeBase, DefaultDataONEMemberNodeBase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}

	if want := DefaultPermsFilter(); len(c.PermsFilter) != len(want) {
		t.Errorf("perms filter = %v, want %v", c.PermsFilter, want)
	}
}

// TestBadCharsDefault pins the literal characters, since they are invisible in source.
func TestBadCharsDefault(t *testing.T) {
	if want := "`'\n\t"; DefaultBadChars != want {
		t.Errorf("DefaultBadChars = %q, want %q", DefaultBadChars, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	c := mustLoadYAML(t, withRequired(t, `
port: 8080
timeouts:
  request: 45s
  upload: 2h
permsfilter:
  - alice
  - bob
irods:
  host: irods.example.org
  port: 1247
  usetrash: false
  adminusers:
    - rods
`))

	if c.Port != 8080 {
		t.Errorf("port = %d, want 8080", c.Port)
	}
	if c.Timeouts.Request != 45*time.Second {
		t.Errorf("request timeout = %s, want 45s", c.Timeouts.Request)
	}
	if c.Timeouts.Upload != 2*time.Hour {
		t.Errorf("upload timeout = %s, want 2h", c.Timeouts.Upload)
	}
	if c.IRODS.UseTrash {
		t.Error("use_trash should be false when set false; a bool default must not win")
	}
	// A configured list replaces the default rather than appending to it.
	if got := strings.Join(c.PermsFilter, ","); got != "alice,bob" {
		t.Errorf("perms filter = %q, want %q", got, "alice,bob")
	}
	if got := strings.Join(c.IRODS.AdminUsers, ","); got != "rods" {
		t.Errorf("admin users = %q, want %q", got, "rods")
	}
}

// TestEnvOverride covers the secrets path: a password must be settable from the
// environment so it never has to appear in a file or on the command line.
func TestEnvOverride(t *testing.T) {
	t.Setenv("DATAINFOTEST_IRODS_PASSWORD", "from-the-environment")
	c := mustLoadYAML(t, requiredYAML)
	if c.IRODS.Password != "from-the-environment" {
		t.Errorf("irods password = %q, want it to come from the environment", c.IRODS.Password)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	_, err := loadYAML(t, `
port: 0
maxpaths: 0
irods:
  password: notprod
icat:
  password: notprod
`)
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	for _, want := range []string{
		"port must be between 1 and 65535",
		"maxpaths must be at least 1",
		"anonfiles.baseurl is required",
		"anonfiles.mappings must contain at least one",
		"kifshare.externalurl is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q; got:\n%s", want, err)
		}
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"non-absolute community data", "communitydata: relative/path", "must be an absolute iRODS path"},
		{"non-http service url", "services:\n  metadata: ftp://example.org", "must use the http or https scheme"},
		{"non-amqp broker uri", "amqp:\n  uri: http://rabbit:5672/", "must use the amqp or amqps scheme"},
		{"relative mapping key", "anonfiles:\n  mappings:\n    relative: x/", "must be an absolute iRODS path"},
		{"out of range irods port", "irods:\n  port: 70000", "irods.port must be between 1 and 65535"},
		{"empty icat password", "icat:\n  password: \"\"", "icat.password must not be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadYAML(t, withRequired(t, tt.yaml))
			if err == nil {
				t.Fatal("expected validation to fail")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error is missing %q; got:\n%s", tt.want, err)
			}
		})
	}
}

func TestICATConnectionString(t *testing.T) {
	c := mustLoadYAML(t, withRequired(t, `
icat:
  host: priest.example.org
  port: 5432
  user: icat_reader
  password: "p@ss word/&"
  database: ICAT
`))
	got := c.ICATConnectionString()
	want := "postgres://icat_reader:p%40ss%20word%2F&@priest.example.org:5432/ICAT?sslmode=disable"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestConfiguredListReplacesDefault guards a subtle mapstructure behaviour. Without
// ZeroFields, decoding a shorter list over a longer default merges them element by
// element, so a one-entry permsfilter would silently keep "rodsadmin" from the default.
func TestConfiguredListReplacesDefault(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		got  func(*Config) []string
		want string
	}{
		{
			name: "shorter than the default",
			yaml: "permsfilter:\n  - onlyone\n",
			got:  func(c *Config) []string { return c.PermsFilter },
			want: "onlyone",
		},
		{
			name: "longer than the default",
			yaml: "permsfilter:\n  - a\n  - b\n  - c\n",
			got:  func(c *Config) []string { return c.PermsFilter },
			want: "a,b,c",
		},
		{
			name: "nested list, shorter than the default",
			yaml: "irods:\n  adminusers:\n    - solo\n",
			got:  func(c *Config) []string { return c.IRODS.AdminUsers },
			want: "solo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustLoadYAML(t, withRequired(t, tt.yaml))
			if got := strings.Join(tt.got(c), ","); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLegacyMapMasksSecrets pins the GET /admin/config behaviour, including the one it
// gets wrong. See docs/deferred-fixes.md entry 5.
func TestLegacyMapMasksSecrets(t *testing.T) {
	c := mustLoadYAML(t, withRequired(t, `
amqp:
  uri: amqp://ipc:hunter2@rabbit:5672/%2Fqa
irods:
  user: rods
  password: supersecret
icat:
  user: icat_reader
  password: alsosecret
`))
	m := c.LegacyMap()

	masked := []string{
		"data-info.irods.user",
		"data-info.irods.password",
		"data-info.icat.user",
		"data-info.icat.password",
	}
	for _, k := range masked {
		if m[k] != maskedValue {
			t.Errorf("%s = %q, want it masked", k, m[k])
		}
	}

	// Reproduced wart: the key does not match any mask filter, so the broker password
	// is returned in the clear, exactly as the Clojure service does.
	if got := m["data-info.amqp.uri"]; got != "amqp://ipc:hunter2@rabbit:5672/%2Fqa" {
		t.Errorf("data-info.amqp.uri = %q, want it unmasked to match the Clojure service", got)
	}
}

// TestLegacyMapValuesAreStrings covers the properties-file shape callers diff against:
// numbers and booleans came back quoted.
func TestLegacyMapValuesAreStrings(t *testing.T) {
	m := mustLoadYAML(t, requiredYAML).LegacyMap()

	tests := []struct{ key, want string }{
		{"data-info.port", "60000"},
		{"data-info.icat.port", "5432"},
		{"data-info.irods.use-trash", "true"},
		{"data-info.irods.max-retries", "10"},
		{"data-info.irods.retry-sleep", "1000"},
		{"data-info.jetty.max-idle-time", "200000"},
		{"data-info.jetty.upload-idle-time", "3600000"},
		{"data-info.max-paths-in-request", "1000"},
		{"data-info.amqp.exchange.durable", "true"},
		{"data-info.amqp.exchange.auto-delete", "false"},
		{"data-info.perms-filter", "rods,rodsadmin"},
		{"data-info.anon-files-mappings", `{"/iplant/home/":"cyverse/home/"}`},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := m[tt.key]; got != tt.want {
				t.Errorf("%s = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}
