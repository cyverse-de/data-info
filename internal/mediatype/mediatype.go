// Package mediatype detects a file's media type from its name alone, since reading contents
// would cost an iRODS read per path (docs/deferred-fixes.md #11). It uses its own table, not
// Go's mime package: that reads the host's mime.types, which differs between the image and a
// workstation (e.g. for .vcf), and callers compare the result.
package mediatype

import (
	"path"
	"strings"
)

// Default is what an unrecognised file reports as.
const Default = "application/octet-stream"

// extensions holds the types the Clojure service reported via Apache Tika, including ones
// that look wrong (.vcf as a vCard, .r as text/x-rsrc), because callers compare them
// (docs/deferred-fixes.md #10).
var extensions = map[string]string{
	// Text and documents.
	".txt":      "text/plain",
	".log":      "text/x-log",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".html":     "text/html",
	".htm":      "text/html",
	".xml":      "application/xml",
	".json":     "application/json",
	".pdf":      "application/pdf",
	".csv":      "text/csv",
	".tsv":      "text/tab-separated-values",
	".yml":      "text/x-yaml",
	".yaml":     "text/x-yaml",

	// Images.
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",

	// Archives.
	".gz":  "application/gzip",
	".zip": "application/zip",
	".tar": "application/x-tar",

	// Code and notebooks. Tika identified these by content, so a notebook is text/plain.
	".sh":    "application/x-sh",
	".py":    "text/x-python",
	".pl":    "text/x-perl",
	".r":     "text/x-rsrc",
	".rmd":   "text/plain",
	".ipynb": "text/plain",

	// Bioinformatics. Tika identified these by content; all but BAM read as text.
	".bam":    "application/gzip",
	".bed":    "text/plain",
	".fasta":  "text/plain",
	".fa":     "text/plain",
	".fastq":  "text/plain",
	".fq":     "text/plain",
	".gff":    "text/plain",
	".gff3":   "text/plain",
	".gtf":    "text/plain",
	".newick": "text/plain",
	".nwk":    "text/plain",
	".sam":    "text/plain",
	".vcf":    "text/x-vcard",
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
