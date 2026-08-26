package mediatype

import "testing"

// TestOfNameMatchesTheReference pins the values measured against the running Clojure
// service. Several look wrong -- a .vcf is variant-call data, not a vCard -- and that is the
// point: they are the wire contract, and a "correction" here would be a silent behaviour
// change for callers that switch on the type.
func TestOfNameMatchesTheReference(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/zone/home/u/notes.txt", "text/plain"},
		{"/zone/home/u/run.log", "text/x-log"},
		{"/zone/home/u/page.html", "text/html"},
		{"/zone/home/u/page.htm", "text/html"},
		{"/zone/home/u/data.json", "application/json"},
		{"/zone/home/u/doc.xml", "application/xml"},
		{"/zone/home/u/paper.pdf", "application/pdf"},
		{"/zone/home/u/table.csv", "text/csv"},
		{"/zone/home/u/table.tsv", "text/tab-separated-values"},
		{"/zone/home/u/conf.yml", "text/x-yaml"},
		{"/zone/home/u/conf.yaml", "text/x-yaml"},
		{"/zone/home/u/plot.png", "image/png"},
		{"/zone/home/u/photo.jpg", "image/jpeg"},
		{"/zone/home/u/photo.jpeg", "image/jpeg"},
		{"/zone/home/u/anim.gif", "image/gif"},
		{"/zone/home/u/icon.svg", "image/svg+xml"},
		{"/zone/home/u/archive.gz", "application/gzip"},
		{"/zone/home/u/archive.zip", "application/zip"},
		{"/zone/home/u/archive.tar", "application/x-tar"},
		{"/zone/home/u/script.sh", "application/x-sh"},
		{"/zone/home/u/script.py", "text/x-python"},
		{"/zone/home/u/script.pl", "text/x-perl"},
		{"/zone/home/u/analysis.R", "text/x-rsrc"},
		{"/zone/home/u/report.Rmd", "text/plain"},
		{"/zone/home/u/notebook.ipynb", "text/plain"},
		{"/zone/home/u/reads.bam", "application/gzip"},
		{"/zone/home/u/calls.vcf", "text/x-vcard"},
		{"/zone/home/u/genes.gff3", "text/plain"},
		{"/zone/home/u/reads.fastq", "text/plain"},
		{"/zone/home/u/tree.newick", "text/plain"},

		// A name Tika cannot read is where this diverges: the Clojure service opens the
		// object and finds text, and there is nothing in a name-only table that could.
		{"/zone/home/u/README", Default},
		{"/zone/home/u/data.unknownext", Default},

		// Case and trailing slashes are not part of the answer.
		{"/zone/home/u/NOTES.TXT", "text/plain"},
		{"/zone/home/u/dir.txt/", "text/plain"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := OfName(tc.path); got != tc.want {
				t.Errorf("OfName(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
