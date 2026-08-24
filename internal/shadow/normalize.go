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
	"net/http"
	"regexp"
	"sort"
	"strconv"
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

	// CompareHeaders names the response headers that are contract. Each is compared
	// exactly; see compareHeaders for why this is not an expected-difference table.
	CompareHeaders map[string]bool

	// IgnoreHeaders names the headers that differ by HTTP stack and carry nothing a
	// caller acts on. A header on neither list is reported, so the two together are a
	// classification of everything either service sends rather than a filter.
	IgnoreHeaders map[string]bool
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

	// instancePattern matches the pod name a service stamps into an async task's detail.
	// Both spellings of the deployment appear, so the prefix is matched loosely rather than
	// pinned to either name.
	instancePattern = regexp.MustCompile(`\[data-info[a-z-]*-[a-z0-9]+-[a-z0-9]+\]`)

	// trashSuffixPattern matches the random suffix appended when something is moved to the
	// trash, keeping the path in front of it. Anchored to a trash path and to the closing
	// quote so it cannot rewrite an ordinary filename that happens to end in a short
	// extension.
	trashSuffixPattern = regexp.MustCompile(`(/trash/[^"]*)\.[A-Za-z0-9]{7}"`)

	// schemaReasonPattern matches the reason attached to a schema-validation failure,
	// whether it is rendered as a string or as a nested object.
	//
	// The string branch has to allow escaped quotes. These reasons quote the offending
	// value, so they routinely contain them, and a pattern that stopped at the first
	// escaped quote would leave a mangled remainder that no longer parses -- reporting
	// "not JSON" instead of a real comparison.
	schemaReasonPattern = regexp.MustCompile(`"reason":\s*(\{[^}]*\}|"(?:[^"\\]|\\.)*")`)
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
	replacements := []Replacement{}

	// An empty run id would compile to a pattern matching at every position, so every
	// body would come back interleaved with the placeholder and fail to parse -- turning
	// every case into "not JSON" rather than a real comparison.
	if runID != "" {
		replacements = append(replacements, Replacement{regexp.MustCompile(regexp.QuoteMeta(runID)), "{RUN}"})
	}

	return &Normalizer{
		Replacements: append(replacements,
			Replacement{regexp.MustCompile(`/(A|B)/`), "/{SIDE}/"},
			Replacement{uuidPattern, "{UUID}"},
			Replacement{millisPattern, canonicalMillis},

			// The two services necessarily answer on different ports, and the status
			// endpoint reports its own address. Comparing that would only ever measure
			// how the harness was wired.
			Replacement{hostPortPattern, "http://{HOST}"},

			// A schema-validation failure renders prismatic/schema's internal
			// explanation on the reference side, which has no Go equivalent and is
			// diagnostic text rather than contract. The error_code and the status are
			// still compared exactly, and those are what callers branch on.
			Replacement{schemaReasonPattern, `"reason":"{SCHEMA}"`},

			// An async task's detail names the pod that ran it. Each service correctly
			// reports its own, so comparing them would only ever measure that the two are
			// different deployments -- which is the premise of the run, not a finding.
			Replacement{instancePattern, "[{INSTANCE}]"},

			// Moving something to the trash appends a random suffix so that two deletes
			// of the same name do not collide. It differs per call by design, on one
			// service as much as between two.
			Replacement{trashSuffixPattern, `${1}.{TRASHSUFFIX}"`},
		),
		// Arrays whose order is the answer rather than incidental. A listing's order is
		// exactly what a sort-field request asks for, so sorting it here would hide the
		// bug the case exists to catch. A task's status trail is the same: terrain's move
		// poller reads it in order to show progress, so two services reporting the same
		// statuses in different orders is a difference, not a tie.
		// Observed on 2026-08-24 by probing both services in QA across service info, a
		// bulk stat, a folder listing, a 400, a 500 and an unrecognised route. The
		// reference emitted Content-Length, Content-Type, Date and Server; this service
		// emitted the same minus Server, plus Transfer-Encoding. Replace this list from a
		// fresh discovery run rather than extending it by guess.
		CompareHeaders: map[string]bool{
			// Governs how every caller parses the body, and on a download it comes from
			// the media-type table -- the most likely to differ and the most likely to
			// matter.
			"Content-Type": true,
			// The download filename, in both of its spellings.
			"Content-Disposition": true,
			// Invisible in a compared body, since both sides decode to the same bytes,
			// but it changes what a caller that streams rather than buffers receives.
			"Content-Encoding": true,
			"Location":         true,
		},
		IgnoreHeaders: map[string]bool{
			"Date":   true,
			"Server": true,
			// Derived from a body that is already compared exactly, so it can only
			// restate that result or add chunked-versus-not noise.
			"Content-Length":    true,
			"Transfer-Encoding": true,
			// Transport, not payload.
			"Connection": true,
			"Keep-Alive": true,
		},
		PreserveOrder: map[string]bool{
			"files":    true,
			"folders":  true,
			"paths":    true,
			"statuses": true,
		},
	}
}

// Response is one service's answer.
type Response struct {
	Status  int
	Body    []byte
	Headers http.Header
}

// Diff is one difference between two responses.
type Diff struct {
	Kind   string
	Detail string
}

// Compare returns the differences between the reference service's response and ours.
func (n *Normalizer) Compare(reference, candidate Response) []Diff {
	diffs := n.compareHeaders(reference.Headers, candidate.Headers)

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

// compareHeaders diffs the headers that are contract, and reports any header that is
// neither compared nor ignored.
//
// The classification is not an expected-difference table, and the distinction matters
// because the two look alike. CompareHeaders declares which headers are *contract*, decided
// once for the service; it does not declare which differences are tolerable. Everything on
// it is compared exactly with no per-case exceptions, so "every reported difference is a
// real defect" holds for headers exactly as it does for statuses. A per-case exemption would
// break that the same way a status exemption would.
func (n *Normalizer) compareHeaders(reference, candidate http.Header) []Diff {
	var diffs []Diff

	for name := range n.CompareHeaders {
		name = http.CanonicalHeaderKey(name)
		ref, cand := headerValue(reference, name), headerValue(candidate, name)
		if ref == cand {
			continue
		}
		diffs = append(diffs, Diff{
			Kind:   "header:" + name,
			Detail: fmt.Sprintf("reference %s, candidate %s", quoteOrAbsent(ref), quoteOrAbsent(cand)),
		})
	}

	// Anything neither compared nor ignored is an error rather than an omission. A contract
	// header nobody thought to list would otherwise be silently unchecked -- the same shape
	// of miss as a route that is registered but half implemented, which passed a route
	// audit for exactly that reason.
	//
	// The union spans both services, not just the reference. A header the *candidate*
	// emits and the reference never did is a wire change too, and a Go stack introduces
	// them readily: echo sets a content type where the reference left one off, middleware
	// adds its own. Discovering from the reference alone is blind to all of those by
	// construction.
	for _, name := range unclassifiedHeaders(n, reference, candidate) {
		diffs = append(diffs, Diff{
			Kind: "header:unclassified",
			Detail: fmt.Sprintf("%s is on neither the compare list nor the ignore list; "+
				"classify it as contract or as stack noise", name),
		})
	}

	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Kind < diffs[j].Kind })
	return diffs
}

// unclassifiedHeaders names the headers either service sent that the normalizer has no
// opinion about, in a stable order.
func unclassifiedHeaders(n *Normalizer, sides ...http.Header) []string {
	seen := map[string]bool{}
	var out []string

	for _, side := range sides {
		for name := range side {
			canonical := http.CanonicalHeaderKey(name)
			if seen[canonical] || n.CompareHeaders[canonical] || n.IgnoreHeaders[canonical] {
				continue
			}
			seen[canonical] = true
			out = append(out, canonical)
		}
	}

	sort.Strings(out)
	return out
}

// headerValue joins a header's values the way they travel, so a repeated header compares as
// one string rather than silently on its first value only.
func headerValue(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	return strings.Join(h.Values(name), ", ")
}

// quoteOrAbsent renders a header value for a report, distinguishing empty from absent --
// which for a content type is the difference between two real behaviours.
func quoteOrAbsent(value string) string {
	if value == "" {
		return "absent"
	}
	return strconv.Quote(value)
}
