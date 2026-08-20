// Package mediatype detects a file's media type from its name.
//
// The Clojure service used Apache Tika, which has a far larger name table than Go's and
// also sniffed content when the name was inconclusive. Nothing in Go reproduces that
// exactly, so this is deliberately name-only, with an explicit table for the extensions the
// DE actually cares about layered over the standard library's.
//
// Divergences from Tika are expected and are tracked as a known difference rather than
// discovered in the field; content sniffing is the part most likely to disagree, so it is
// not attempted at all rather than attempted differently.
//
// The table is the whole answer. Go's mime package seeds itself from the host's
// /etc/mime.types at init, so consulting it would make the same file report one type on a
// workstation and another in a distroless image that ships no such file -- a
// deployment-dependent answer for a value callers compare.
package mediatype

import (
	"path"
	"strings"
)

// Default is what an unrecognised file reports as, matching Tika.
const Default = "application/octet-stream"

// extensions covers types the DE handles that Go's table does not, or names differently.
// Values here win over the standard library.
var extensions = map[string]string{
	".bam":      "application/octet-stream",
	".bed":      "text/plain",
	".fasta":    "text/plain",
	".fastq":    "text/plain",
	".fa":       "text/plain",
	".fq":       "text/plain",
	".gff":      "text/plain",
	".gff3":     "text/plain",
	".gtf":      "text/plain",
	".newick":   "text/plain",
	".nwk":      "text/plain",
	".sam":      "text/plain",
	".vcf":      "text/plain",
	".r":        "text/plain",
	".rmd":      "text/plain",
	".ipynb":    "application/json",
	".yml":      "text/yaml",
	".yaml":     "text/yaml",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".tsv":      "text/tab-separated-values",
	".csv":      "text/csv",
	".log":      "text/plain",
	".sh":       "application/x-sh",
	".py":       "text/x-python",
	".pl":       "text/x-perl",
}

// OfName returns the media type implied by a path's extension.
func OfName(p string) string {
	ext := strings.ToLower(path.Ext(strings.TrimRight(p, "/")))
	if ext == "" {
		return Default
	}

	if known, ok := extensions[ext]; ok {
		return known
	}

	return Default
}
