package versioning

import (
	"fmt"
	"strings"
)

// Diff line kinds.
const (
	diffContext = ' '
	diffDelete  = '-'
	diffInsert  = '+'
)

// maxDiffD bounds the Myers edit distance we are willing to explore. Memory for
// the trace is O(maxDiffD^2), so beyond this we degrade to a whole-block
// replacement, which is correct but not minimal.
const maxDiffD = 1000

// maxDiffLines bounds the combined line count. Very large files are reported as
// a truncated whole-block replacement rather than spending unbounded time.
const maxDiffLines = 400000

// defaultContext is the number of unchanged lines shown around each hunk.
const defaultContext = 3

// diffOp is one step of an edit script.
type diffOp struct {
	kind byte
	text string
}

// diffLine is a positioned line ready for hunk assembly.
type diffLine struct {
	kind  byte
	text  string
	oldNo int
	newNo int
}

// splitLines splits text into lines, normalising CRLF and dropping the trailing
// empty element produced by a final newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// diffLines computes an edit script between two line slices.
//
// It trims the common prefix and suffix first, which keeps the Myers search
// small for the typical "a few edits in a big file" case. The bool reports
// whether the result degraded to a whole-block replacement.
func diffLines(a, b []string) ([]diffOp, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	ops := make([]diffOp, 0, len(a)+len(b))
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{kind: diffContext, text: a[i]})
	}

	midA := a[pre : len(a)-suf]
	midB := b[pre : len(b)-suf]

	degraded := false
	if len(midA)+len(midB) > maxDiffLines {
		degraded = true
	} else {
		mid, ok := myersDiff(midA, midB)
		if ok {
			ops = append(ops, mid...)
		} else {
			degraded = true
		}
	}
	if degraded {
		for _, line := range midA {
			ops = append(ops, diffOp{kind: diffDelete, text: line})
		}
		for _, line := range midB {
			ops = append(ops, diffOp{kind: diffInsert, text: line})
		}
	}

	for i := len(a) - suf; i < len(a); i++ {
		ops = append(ops, diffOp{kind: diffContext, text: a[i]})
	}
	return ops, degraded
}

// myersDiff runs the classic Myers O(ND) algorithm with trace backtracking.
// The bool result is false when the edit distance exceeds maxDiffD.
func myersDiff(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	switch {
	case n == 0 && m == 0:
		return nil, true
	case n == 0:
		ops := make([]diffOp, 0, m)
		for _, line := range b {
			ops = append(ops, diffOp{kind: diffInsert, text: line})
		}
		return ops, true
	case m == 0:
		ops := make([]diffOp, 0, n)
		for _, line := range a {
			ops = append(ops, diffOp{kind: diffDelete, text: line})
		}
		return ops, true
	}

	limit := n + m
	if limit > maxDiffD {
		limit = maxDiffD
	}
	offset := limit
	v := make([]int, 2*limit+1)
	trace := make([][]int, 0, limit+1)

	found := -1
	for d := 0; d <= limit && found < 0; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return nil, false
	}

	// Walk the trace backwards, emitting edits in reverse.
	rev := make([]diffOp, 0, n+m)
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && prev[offset+k-1] < prev[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[offset+prevK]
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			rev = append(rev, diffOp{kind: diffContext, text: a[x-1]})
			x--
			y--
		}
		switch {
		case x > prevX:
			rev = append(rev, diffOp{kind: diffDelete, text: a[x-1]})
			x--
		case y > prevY:
			rev = append(rev, diffOp{kind: diffInsert, text: b[y-1]})
			y--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, diffOp{kind: diffContext, text: a[x-1]})
		x--
		y--
	}
	for ; x > 0; x-- {
		rev = append(rev, diffOp{kind: diffDelete, text: a[x-1]})
	}
	for ; y > 0; y-- {
		rev = append(rev, diffOp{kind: diffInsert, text: b[y-1]})
	}

	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}

// positionLines assigns old/new line numbers to an edit script.
func positionLines(ops []diffOp) []diffLine {
	lines := make([]diffLine, 0, len(ops))
	oldNo, newNo := 1, 1
	for _, op := range ops {
		switch op.kind {
		case diffDelete:
			lines = append(lines, diffLine{kind: diffDelete, text: op.text, oldNo: oldNo})
			oldNo++
		case diffInsert:
			lines = append(lines, diffLine{kind: diffInsert, text: op.text, newNo: newNo})
			newNo++
		default:
			lines = append(lines, diffLine{kind: diffContext, text: op.text, oldNo: oldNo, newNo: newNo})
			oldNo++
			newNo++
		}
	}
	return lines
}

// countChanges tallies additions and deletions in an edit script.
func countChanges(ops []diffOp) (additions, deletions int) {
	for _, op := range ops {
		switch op.kind {
		case diffInsert:
			additions++
		case diffDelete:
			deletions++
		}
	}
	return additions, deletions
}

// diffRange describes one hunk's extent within the positioned line list.
type diffRange struct {
	start int
	end   int
}

// buildHunks groups changed lines into hunks, merging changes that are closer
// together than twice the context size.
func buildHunks(lines []diffLine, context int) []diffRange {
	changed := make([]int, 0, len(lines))
	for i, line := range lines {
		if line.kind != diffContext {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	hunks := make([]diffRange, 0, 4)
	start := max(0, changed[0]-context)
	for i := 0; i < len(changed); i++ {
		last := i == len(changed)-1
		if !last && changed[i+1]-changed[i] <= 2*context+1 {
			continue
		}
		end := min(len(lines), changed[i]+context+1)
		hunks = append(hunks, diffRange{start: start, end: end})
		if !last {
			start = max(0, changed[i+1]-context)
		}
	}
	return hunks
}

// hunkSpan returns the "@@ -oldStart,oldCount +newStart,newCount @@" numbers for
// a hunk. When a side has no lines in the hunk (pure insert or pure delete),
// git anchors the range on the preceding line with a zero count.
func hunkSpan(lines []diffLine, r diffRange, wantOld bool) (int, int) {
	first, count := 0, 0
	for i := r.start; i < r.end; i++ {
		n := lines[i].newNo
		if wantOld {
			n = lines[i].oldNo
		}
		if n > 0 {
			if first == 0 {
				first = n
			}
			count++
		}
	}
	if first > 0 {
		return first, count
	}
	for i := r.start - 1; i >= 0; i-- {
		n := lines[i].newNo
		if wantOld {
			n = lines[i].oldNo
		}
		if n > 0 {
			return n, 0
		}
	}
	return 0, 0
}

// unifiedDiffBody renders hunks against a path in git's unified diff shape.
func unifiedDiffBody(path string, lines []diffLine, hunks []diffRange, context int) string {
	var b strings.Builder
	for _, hunk := range hunks {
		oldStart, oldCount := hunkSpan(lines, hunk, true)
		newStart, newCount := hunkSpan(lines, hunk, false)
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for i := hunk.start; i < hunk.end; i++ {
			b.WriteByte(lines[i].kind)
			b.WriteString(lines[i].text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// shortHash returns the first 7 characters of a hash, matching git's display
// convention. A zero hash renders as git's all-zero placeholder.
func shortHash(hash string) string {
	if hash == "" {
		return "0000000"
	}
	if len(hash) < 7 {
		return hash
	}
	return hash[:7]
}

// buildUnifiedDiff assembles a complete git-compatible unified diff for one
// path. The web parser in web/src/components/gitDiffModel.ts understands this
// exact shape, so no frontend changes are needed.
func buildUnifiedDiff(path, status, oldHash, newHash string, oldContent, newContent []byte, context int) DiffResult {
	result := DiffResult{
		Path:    path,
		Status:  status,
		OldSize: int64(len(oldContent)),
		NewSize: int64(len(newContent)),
	}

	oldBinary := IsBinaryPayload(oldContent)
	newBinary := IsBinaryPayload(newContent)
	if oldBinary || newBinary {
		result.Binary = true
		switch {
		case status == StatusAdded:
			result.Message = fmt.Sprintf("Binary file added (%s)", humanBytes(int64(len(newContent))))
		case status == StatusDeleted:
			result.Message = fmt.Sprintf("Binary file deleted (%s)", humanBytes(int64(len(oldContent))))
		default:
			result.Message = fmt.Sprintf("Binary file changed (%s → %s)",
				humanBytes(int64(len(oldContent))), humanBytes(int64(len(newContent))))
		}
		return result
	}

	oldLines := splitLines(string(oldContent))
	newLines := splitLines(string(newContent))
	ops, degraded := diffLines(oldLines, newLines)
	result.Additions, result.Deletions = countChanges(ops)
	result.Truncated = degraded

	if result.Additions == 0 && result.Deletions == 0 {
		return result
	}

	lines := positionLines(ops)
	hunks := buildHunks(lines, context)

	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	switch status {
	case StatusAdded:
		b.WriteString("new file mode 100644\n")
		fmt.Fprintf(&b, "index %s..%s\n", "0000000", shortHash(newHash))
		b.WriteString("--- /dev/null\n")
		fmt.Fprintf(&b, "+++ b/%s\n", path)
	case StatusDeleted:
		b.WriteString("deleted file mode 100644\n")
		fmt.Fprintf(&b, "index %s..%s\n", shortHash(oldHash), "0000000")
		fmt.Fprintf(&b, "--- a/%s\n", path)
		b.WriteString("+++ /dev/null\n")
	default:
		fmt.Fprintf(&b, "index %s..%s 100644\n", shortHash(oldHash), shortHash(newHash))
		fmt.Fprintf(&b, "--- a/%s\n", path)
		fmt.Fprintf(&b, "+++ b/%s\n", path)
	}
	if degraded {
		b.WriteString("# diff truncated: too many changes, showing whole-file replacement\n")
	}
	b.WriteString(unifiedDiffBody(path, lines, hunks, context))
	result.Content = b.String()
	return result
}

// summarizeChanges fills addition/deletion counts for a manifest comparison
// without materialising the full diff text.
func summarizeChanges(storeDir string, items []ChangeItem) []ChangeItem {
	for i := range items {
		item := &items[i]
		if item.Binary || item.Status == StatusDeleted || item.Status == StatusAdded {
			// Additions/deletions for new and removed files are their line
			// counts; binary files report none.
			if item.Binary {
				continue
			}
		}
		var oldContent, newContent []byte
		var err error
		if item.OldHash != "" {
			if oldContent, err = readBlob(storeDir, item.OldHash); err != nil {
				continue
			}
		}
		if item.NewHash != "" {
			if newContent, err = readBlob(storeDir, item.NewHash); err != nil {
				continue
			}
		}
		if IsBinaryPayload(oldContent) || IsBinaryPayload(newContent) {
			item.Binary = true
			continue
		}
		ops, _ := diffLines(splitLines(string(oldContent)), splitLines(string(newContent)))
		item.Additions, item.Deletions = countChanges(ops)
	}
	return items
}
