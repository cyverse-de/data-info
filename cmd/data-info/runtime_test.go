package main

import (
	"io"
	"io/fs"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func TestParseCgroupLimit(t *testing.T) {
	tests := []struct {
		contents string
		want     int64
		wantOK   bool
	}{
		{"2147483648\n", 2147483648, true},
		{"max\n", 0, false},
		{"9223372036854771712\n", 0, false}, // cgroup v1's "no limit"
		{"0", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.contents, func(t *testing.T) {
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
	// Reserve a free loopback port, then hand it to the profiler.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}

	discard := logrus.New()
	discard.SetOutput(io.Discard)
	startProfiler(addr, logrus.NewEntry(discard))

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/debug/pprof/heap?debug=1")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				t.Fatalf("reading the profile: %v", readErr)
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "heap profile") {
				t.Fatalf("status %d, body %.200q", resp.StatusCode, body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the profiler never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
