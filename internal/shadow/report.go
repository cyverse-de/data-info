package shadow

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/cyverse-de/data-info/internal/apierror"
)

// Report renders results, and reports whether everything that ran matched.
//
// Skipped cases are listed rather than counted silently. A harness that quietly drops cases
// reads as "everything passed" when it means "everything I bothered to run passed".
func Report(w io.Writer, results []Result) (bool, error) {
	out := &errWriter{w: w}
	var passed, failed, skipped int

	for _, res := range results {
		switch {
		case res.Err != nil:
			failed++
			out.printf("ERROR   %s: %v\n", res.Case.ID, res.Err)
		case res.Skipped != "":
			skipped++
			out.printf("SKIP    %s: %s\n", res.Case.ID, res.Skipped)
		case len(res.Diffs) > 0:
			failed++
			out.printf("DIFF    %s\n", res.Case.ID)
			for _, d := range res.Diffs {
				out.printf("          %s: %s\n", d.Kind, d.Detail)
			}
		default:
			passed++
			out.printf("ok      %s\n", res.Case.ID)
		}
	}

	out.printf("\n%d matched, %d differed, %d skipped\n", passed, failed, skipped)
	if skipped > 0 {
		out.printf("note: skipped cases were not compared; the run is not evidence about them\n")
	}

	reportCodeCoverage(out, results)

	return failed == 0, out.err
}

// reportCodeCoverage names the error codes no case elicited.
//
// Cutover asks that every code data-info can return be exercised at least once, and the
// only honest way to know is to look at what a run actually produced -- a case cannot
// declare the code it expects, because the harness compares the two services against each
// other rather than against a fixture. A code missing here means either no case reaches it
// or the case that used to has drifted onto a different failure.
//
// Reported, never fatal. Coverage is a property of the catalog, and failing a run over it
// would confuse "the two services disagree" with "we have not written that case yet".
func reportCodeCoverage(out *errWriter, results []Result) {
	seen := map[string]bool{}
	for _, res := range results {
		if res.Code != "" {
			seen[res.Code] = true
		}
	}

	var missing []string
	for _, c := range apierror.EmittedCodes() {
		if !seen[string(c)] {
			missing = append(missing, string(c))
		}
	}
	sort.Strings(missing)

	total := len(apierror.EmittedCodes())
	out.printf("\nerror codes elicited: %d of %d\n", total-len(missing), total)
	if len(missing) > 0 {
		out.printf("not elicited by any case: %s\n", strings.Join(missing, ", "))
	}
}

// errWriter records the first write failure so a report does not have to check every line.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
