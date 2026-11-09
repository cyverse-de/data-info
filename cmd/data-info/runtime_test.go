package main

import (
	"io"
	"io/fs"
	"net/http"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestParseCgroupLimit(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     int64
		wantOK   bool
	}{
		{"a limit", "2147483648\n", 2147483648, true},
		{"cgroup v2 unlimited", "max\n", 0, false},
		{"cgroup v1 unlimited", "9223372036854771712\n", 0, false},
		{"zero", "0", 0, false},
		{"empty", "", 0, false},
		{"garbled", "2Gi\n", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseCgroupLimit(tt.contents)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("got (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestApplyMemoryLimit(t *testing.T) {
	const gib = int64(1) << 30

	tests := []struct {
		name  string
		env   string
		files map[string]string
		want  int64 // 0 means the limit is left alone
	}{
		{"cgroup v2", "", map[string]string{"/sys/fs/cgroup/memory.max": "2147483648\n"}, 2 * gib / 100 * memoryLimitPercent},
		{"cgroup v1", "", map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824\n"}, gib / 100 * memoryLimitPercent},
		{"no container limit", "", map[string]string{"/sys/fs/cgroup/memory.max": "max\n"}, 0},
		{"unlimited v2 falls through to v1", "", map[string]string{
			"/sys/fs/cgroup/memory.max":                   "max\n",
			"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824\n",
		}, gib / 100 * memoryLimitPercent},
		{"no cgroup files", "", nil, 0},
		{"GOMEMLIMIT wins", "1GiB", map[string]string{"/sys/fs/cgroup/memory.max": "2147483648\n"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOMEMLIMIT", tt.env)

			const untouched = int64(1) << 50
			previous := debug.SetMemoryLimit(untouched)
			t.Cleanup(func() { debug.SetMemoryLimit(previous) })

			readFile := func(path string) ([]byte, error) {
				if contents, ok := tt.files[path]; ok {
					return []byte(contents), nil
				}
				return nil, fs.ErrNotExist
			}
			discard := logrus.New()
			discard.SetOutput(io.Discard)

			applyMemoryLimit(logrus.NewEntry(discard), readFile)

			want := tt.want
			if want == 0 {
				want = untouched
			}
			if got := debug.SetMemoryLimit(-1); got != want {
				t.Errorf("memory limit = %d, want %d", got, want)
			}
		})
	}
}

func TestProfilerServesHeapProfiles(t *testing.T) {
	discard := logrus.New()
	discard.SetOutput(io.Discard)
	profiler := startProfiler("127.0.0.1:0", logrus.NewEntry(discard))
	if profiler == nil {
		t.Fatal("the profiler could not bind a loopback port")
	}
	t.Cleanup(func() { _ = profiler.Close() })
	addr := profiler.Addr

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatalf("the profiler did not answer: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("reading the profile: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "heap profile") {
		t.Fatalf("status %d, body %.200q", resp.StatusCode, body)
	}
}
