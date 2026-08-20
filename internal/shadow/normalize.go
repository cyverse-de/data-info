// Package shadow compares this service's responses against the Clojure service it replaces.
//
// The port is verified by diffing the two services' answers to the same request, so this is
// where "the same" is defined. Everything that legitimately differs between two runs --
// generated ids, timestamps, the side of a paired fixture a request touched -- is
// canonicalised; everything else is compared exactly.
//
// Statuses are compared exactly, with no allowance for expected differences. That is
// deliberate and it is what preserving the error contract bug-for-bug buys: every difference
// is a real defect, so there is nothing to argue about at review time and no way for a
// regression to hide behind an entry in an exceptions list.
package shadow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Normalizer canonicalises a response so two runs can be compared.
type Normalizer struct {
	// Replacements are applied to the raw body before it is parsed, in order.
	Replacements []Replacement

	// PreserveOrder names JSON keys whose arrays carry meaning in their order and must
	// not be sorted. A paged listing is the example that matters: its order is precisely
	// what a sort-field request is asking for, so sorting it would hide the bug.
	PreserveOrder map[string]bool
}

// Replacement rewrites part of a response.
type Replacement struct {
	Pattern *regexp.Regexp
	With    string
}

var (
	// uuidPattern matches a data id in any position.
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

	// millisPattern matches an epoch-milliseconds timestamp, which differs per run.
	millisPattern = regexp.MustCompile(`\b1[0-9]{12}\b`)
)

var (
	// hostPortPattern matches the service's own address in a reported URL.
	hostPortPattern = regexp.MustCompile(`http://[^/"]+`)

	// schemaReasonPattern matches the reason attached to a schema-validation failure,
	// whether it is rendered as a string or as a nested object.
	schemaReasonPattern = regexp.MustCompile(`"reason":\s*(\{[^}]*\}|"[^"]*")`)
)

// canonicalMillis is what a timestamp is rewritten to.
//
// A number, not a token: timestamps appear in number position, and substituting a bare
// placeholder there would produce invalid JSON. Some responses stringify their numbers, and
// this works in that position too.
const canonicalMillis = "1000000000000"

// NewNormalizer returns a normaliser for a run.
//
// runID and the paired-fixture side are canonicalised so that two subtrees built for the
// same case compare equal.
func NewNormalizer(runID string) *Normalizer {
	return &Normalizer{
		Replacements: []Replacement{
			{regexp.MustCompile(regexp.QuoteMeta(runID)), "{RUN}"},
			{regexp.MustCompile(`/(A|B)/`), "/{SIDE}/"},
			{uuidPattern, "{UUID}"},
			{millisPattern, canonicalMillis},

			// The two services necessarily answer on different ports, and the status
			// endpoint reports its own address. Comparing that would only ever measure
			// how the harness was wired.
			{hostPortPattern, "http://{HOST}"},

			// A schema-validation failure renders prismatic/schema's internal
			// explanation on the reference side, which has no Go equivalent and is
			// diagnostic text rather than contract. The error_code and the status are
			// still compared exactly, and those are what callers branch on.
			{schemaReasonPattern, `"reason":"{SCHEMA}"`},
		},
		PreserveOrder: map[string]bool{"files": true, "folders": true, "paths": true},
	}
}

// Response is one service's answer.
type Response struct {
	Status int
	Body   []byte
}

// Diff is one difference between two responses.
type Diff struct {
	Kind   string
	Detail string
}

// Compare returns the differences between the reference service's response and ours.
func (n *Normalizer) Compare(reference, candidate Response) []Diff {
	var diffs []Diff

	if reference.Status != candidate.Status {
		diffs = append(diffs, Diff{
			Kind:   "status",
			Detail: fmt.Sprintf("reference %d, candidate %d", reference.Status, candidate.Status),
		})
	}

	refBody, refErr := n.canonical(reference.Body)
	candBody, candErr := n.canonical(candidate.Body)

	switch {
	case refErr != nil && candErr != nil:
		// Neither is JSON; compare the bytes, which is right for a download.
		if !equalBytes(reference.Body, candidate.Body) {
			diffs = append(diffs, Diff{Kind: "body", Detail: "non-JSON bodies differ"})
		}
		return diffs
	case refErr != nil:
		return append(diffs, Diff{Kind: "body", Detail: "reference body is not JSON but the candidate's is"})
	case candErr != nil:
		return append(diffs, Diff{Kind: "body", Detail: "candidate body is not JSON but the reference's is"})
	}

	if refBody != candBody {
		diffs = append(diffs, Diff{
			Kind:   "body",
			Detail: firstDifference(refBody, candBody),
		})
	}

	return diffs
}

// canonical rewrites a body and re-encodes it with sorted keys, so that two responses
// differing only in key order or in a generated value compare equal.
func (n *Normalizer) canonical(body []byte) (string, error) {
	rewritten := body
	for _, r := range n.Replacements {
		rewritten = r.Pattern.ReplaceAll(rewritten, []byte(r.With))
	}

	var parsed any
	if err := json.Unmarshal(rewritten, &parsed); err != nil {
		return "", err
	}

	normalized := n.normalizeValue("", parsed)

	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// normalizeValue sorts arrays whose order carries no meaning, leaving alone those whose
// order is the thing under test.
func (n *Normalizer) normalizeValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			out[k] = n.normalizeValue(k, v)
		}
		return out

	case []any:
		out := make([]any, 0, len(typed))
		for _, v := range typed {
			out = append(out, n.normalizeValue(key, v))
		}
		if n.PreserveOrder[key] {
			return out
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]) < fmt.Sprint(out[j])
		})
		return out

	default:
		return value
	}
}

// firstDifference reports where two canonical bodies diverge, so a failure names the line
// rather than dumping both documents.
func firstDifference(a, b string) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")

	for i := 0; i < len(aLines) && i < len(bLines); i++ {
		if aLines[i] != bLines[i] {
			return fmt.Sprintf("line %d: reference %q, candidate %q",
				i+1, strings.TrimSpace(aLines[i]), strings.TrimSpace(bLines[i]))
		}
	}

	if len(aLines) != len(bLines) {
		return fmt.Sprintf("reference has %d lines, candidate %d", len(aLines), len(bLines))
	}
	return "bodies differ"
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
