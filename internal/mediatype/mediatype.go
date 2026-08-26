// Package mediatype detects a file's media type from its name.
//
// The Clojure service used Apache Tika, which detects from the name and then, whenever the
// name says application/octet-stream or text/plain, opens the object and reads its contents.
// That second step costs one iRODS read per path, and a bulk stat is allowed a thousand
// paths, so it is deliberately not reproduced: this is name-only.
//
// The table below closes the gap for everything whose name Tika finds decisive, and records
// what Tika's content sniffing settles on for the file types the DE actually handles. What
// remains different is a file whose name says nothing and whose contents are not what its
// kind usually holds -- most visibly, a file with no extension at all, which Tika reads and
// this cannot.
//
// The table is the whole answer. Go's mime package seeds itself from the host's
// /etc/mime.types at init, and that file differs everywhere: the runtime image ships
// Debian's, which is not Tika's -- it calls .vcf text/vcard where the DE has always
// reported text/x-vcard -- and a workstation's is different again. Consulting it would
// make the same file report one type in the cluster and another in a test, which is a
// deployment-dependent answer for a value callers compare.
package mediatype

import (
	"path"
	"strings"
)

// Default is what an unrecognised file reports as, matching Tika.
const Default = "application/octet-stream"

// extensions is the name table, measured against the Clojure service rather than guessed.
// Every entry here was checked by uploading a sample to the running QA service and reading
// the content-type it reported back.
//
// Several of these are wrong on their face -- Tika reads .vcf as a vCard rather than as
// variant-call data, and calls a .r script text/x-rsrc -- but they are what the DE returns
// today, so they are what this returns. See docs/deferred-fixes.md.
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

	// Code and notebooks. A notebook is JSON, but Tika reaches these by reading the
	// contents rather than the name, and it calls one text/plain.
	".sh":    "application/x-sh",
	".py":    "text/x-python",
	".pl":    "text/x-perl",
	".r":     "text/x-rsrc",
	".rmd":   "text/plain",
	".ipynb": "text/plain",

	// Bioinformatics. Tika has no name for any of these, so it falls back to reading the
	// contents -- which for all but BAM is text. The answers here are the ones real files
	// produce; a .bed holding something other than a BED file would differ.
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
