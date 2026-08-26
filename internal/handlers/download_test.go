package handlers

import (
	"net/http"
	"testing"

	"github.com/cyverse-de/data-info/internal/apierror"
	"github.com/cyverse-de/data-info/internal/icat"
)

// TestContentDisposition covers the header a browser reads the download's filename from.
//
// Both spellings have to be right: filename* carries the real name and filename= is what a
// client that does not understand it falls back to, so a character the fallback cannot carry
// has to be replaced rather than dropped -- a bare quote there would end the parameter early.
func TestContentDisposition(t *testing.T) {
	tests := []struct {
		name       string
		attachment bool
		filename   string
		want       string
	}{
		{
			name: "plain ascii inline", filename: "report.csv",
			want: `inline; filename="report.csv"; filename*=UTF-8''report.csv`,
		},
		{
			name: "plain ascii as an attachment", attachment: true, filename: "report.csv",
			want: `attachment; filename="report.csv"; filename*=UTF-8''report.csv`,
		},
		{
			name: "a space is %20 rather than a plus", filename: "my report.csv",
			want: `inline; filename="my report.csv"; filename*=UTF-8''my%20report.csv`,
		},
		{
			name: "a quote cannot end the fallback early", filename: `a"b.txt`,
			want: `inline; filename="a_b.txt"; filename*=UTF-8''a%22b.txt`,
		},
		{
			name: "a backslash cannot escape out of the fallback", filename: `a\b.txt`,
			want: `inline; filename="a_b.txt"; filename*=UTF-8''a%5Cb.txt`,
		},
		{
			name: "an asterisk is encoded, which RFC 5987 requires", filename: "a*b.txt",
			want: `inline; filename="a*b.txt"; filename*=UTF-8''a%2Ab.txt`,
		},
		{
			name:     "a name above ASCII survives in filename* and is replaced in the fallback",
			filename: "résumé.txt",
			want:     `inline; filename="r_sum_.txt"; filename*=UTF-8''r%C3%A9sum%C3%A9.txt`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentDisposition(tt.attachment, tt.filename); got != tt.want {
				t.Errorf("contentDisposition(%v, %q)\n got %s\nwant %s", tt.attachment, tt.filename, got, tt.want)
			}
		})
	}
}

// TestDownloadOfAnEmptyFileNeedsNoConnection covers the short circuit that answers a
// zero-length object without opening it -- which is also the only part of the download path
// the in-memory catalog can reach, since everything else streams from iRODS.
func TestDownloadOfAnEmptyFileNeedsNoConnection(t *testing.T) {
	deps, fake := testDeps(t)
	fake.AddDataObject(testHome+"/empty.txt", 0, icat.AccessRead)
	listings := NewListings(deps)

	rec := serveRoute(t, apierror.StyleTrap, http.MethodGet, "/data/path/:zone/*",
		"/data/path/iplant/home/wregglej/empty.txt?user="+testUser+"&attachment=true",
		listings.FolderListing)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want empty", body)
	}
	want := `attachment; filename="empty.txt"; filename*=UTF-8''empty.txt`
	if got := rec.Header().Get("Content-Disposition"); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}
}
