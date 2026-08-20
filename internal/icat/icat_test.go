package icat

import (
	"strings"
	"testing"
)

func TestSplitPaths(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantDir  string
		wantBase string
	}{
		{"ordinary path", "/iplant/home/wregglej/file.txt", "/iplant/home/wregglej", "file.txt"},
		{"collection", "/iplant/home/wregglej", "/iplant/home", "wregglej"},
		{"trailing slash is ignored", "/iplant/home/wregglej/", "/iplant/home", "wregglej"},
		{"zone root", "/iplant", "", "iplant"},
		{"a name containing a space", "/iplant/home/a/b c.txt", "/iplant/home/a", "b c.txt"},
		{"a name containing a quote", "/iplant/home/a/o'brien.txt", "/iplant/home/a", "o'brien.txt"},
		{"a name containing a percent", "/iplant/home/a/100%.txt", "/iplant/home/a", "100%.txt"},
		{"a name containing a hash", "/iplant/home/a/c#4.txt", "/iplant/home/a", "c#4.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirs, bases := splitPaths([]string{tt.path})
			if dirs[0] != tt.wantDir {
				t.Errorf("dirname = %q, want %q", dirs[0], tt.wantDir)
			}
			if bases[0] != tt.wantBase {
				t.Errorf("basename = %q, want %q", bases[0], tt.wantBase)
			}
		})
	}
}

// TestSplitPathsStaysPositional matters because the query pairs the two arrays by position.
func TestSplitPathsStaysPositional(t *testing.T) {
	paths := []string{"/z/home/a/one.txt", "/z/home/b", "/z/home/c/two.txt"}
	dirs, bases := splitPaths(paths)

	if len(dirs) != len(paths) || len(bases) != len(paths) {
		t.Fatalf("got %d dirnames and %d basenames for %d paths", len(dirs), len(bases), len(paths))
	}
	for i, p := range paths {
		rejoined := dirs[i] + "/" + bases[i]
		if rejoined != p {
			t.Errorf("path %d rejoins to %q, want %q", i, rejoined, p)
		}
	}
}

func TestPermissionOf(t *testing.T) {
	tests := []struct {
		name         string
		accessTypeID int64
		want         Permission
	}{
		{"read", AccessRead, PermissionRead},
		{"write", AccessWrite, PermissionWrite},
		{"own", AccessOwn, PermissionOwn},
		{"null", 1000, PermissionNone},

		// The DE has never exposed the levels between the named ones, and clj-jargon's
		// fmt-perm matches by equality, so these report as no permission even though
		// delete_object sits above write. Reproduced deliberately.
		{"execute", 1010, PermissionNone},
		{"read metadata", 1040, PermissionNone},
		{"create object", 1110, PermissionNone},
		{"delete object", 1130, PermissionNone},
		{"an unknown level", 9999, PermissionNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PermissionOf(tt.accessTypeID); got != tt.want {
				t.Errorf("PermissionOf(%d) = %q, want %q", tt.accessTypeID, got, tt.want)
			}
		})
	}
}

func TestTimestampConversion(t *testing.T) {
	tests := []struct {
		name string
		ts   string
		want int64
	}{
		{"an ordinary timestamp", "01712345678", 1712345678000},
		{"zero", "00000000000", 0},
		{"unparseable", "not a number", 0},
		{"empty", "", 0},

		// iRODS timestamps are whole seconds in a text column. The Clojure service parsed
		// them with a 32-bit parse in two of the three places it read them, which stops
		// working in 2038; this must not.
		{"beyond 2038", "02147483648", 2147483648000},
		{"far future", "04102444800", 4102444800000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := Row{CreateTS: tt.ts, ModifyTS: tt.ts}
			if got := row.CreatedMillis(); got != tt.want {
				t.Errorf("CreatedMillis() = %d, want %d", got, tt.want)
			}
			if got := row.ModifiedMillis(); got != tt.want {
				t.Errorf("ModifiedMillis() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestQueriesAreParameterised guards the whole reason these live in .sql files. The Clojure
// originals concatenated request values into the statement text; a single quoted literal
// creeping back in here would reopen that.
func TestQueriesAreParameterised(t *testing.T) {
	queries := map[string]string{
		"user_group_ids":  sqlUserGroupIDs,
		"get_items":       sqlGetItems,
		"perms_for_items": sqlPermsForItems,
	}

	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "$1") {
				t.Error("the query takes no parameters, which suggests values are being interpolated")
			}
			for _, line := range strings.Split(query, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "--") {
					continue
				}
				// The only string literals any of these need are the fixed AVU
				// attribute names, the two object type labels, and the separators
				// used to reassemble a path.
				for _, literal := range extractLiterals(trimmed) {
					switch literal {
					case "ipc_UUID", "ipc-filetype", "collection", "dataobject", ".*/", "/", "":
					default:
						t.Errorf("unexpected string literal %q in: %s", literal, trimmed)
					}
				}
			}
		})
	}
}

// extractLiterals returns the single-quoted literals on a line.
func extractLiterals(line string) []string {
	var out []string
	for {
		start := strings.Index(line, "'")
		if start < 0 {
			return out
		}
		rest := line[start+1:]
		end := strings.Index(rest, "'")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		line = rest[end+1:]
	}
}

func TestGetItemRejectsWrongPathCount(t *testing.T) {
	for _, paths := range [][]string{nil, {}, {"/a", "/b"}} {
		if _, err := GetItem(t.Context(), nil, ItemQuery{Paths: paths, User: "u"}); err == nil {
			t.Errorf("GetItem accepted %d paths", len(paths))
		}
	}
}
