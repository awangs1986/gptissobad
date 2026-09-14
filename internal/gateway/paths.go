package gateway

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const pathTokenFmt = "__CLP_%d__"

var (
	winPathRe = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s"'<>]+`)
	uncPathRe = regexp.MustCompile(`\\\\[^\s"'<>]+`)
	nixPathRe = regexp.MustCompile(`(^|[\s"'=(>:])(/[^\s"'<>]+)`)
)

func hasHanOutsidePaths(s string) bool {
	masked, _ := maskHanPaths(s)
	return hasHan(masked)
}

func maskHanPaths(s string) (string, []string) {
	spans := hanPathSpans(s)
	if len(spans) == 0 {
		return s, nil
	}
	var b strings.Builder
	var held []string
	last := 0
	for i, sp := range spans {
		b.WriteString(s[last:sp[0]])
		held = append(held, s[sp[0]:sp[1]])
		fmt.Fprintf(&b, pathTokenFmt, i)
		last = sp[1]
	}
	b.WriteString(s[last:])
	return b.String(), held
}

func unmaskHanPaths(s string, held []string) (string, bool) {
	for i, path := range held {
		tok := fmt.Sprintf(pathTokenFmt, i)
		if !strings.Contains(s, tok) {
			return s, false
		}
		s = strings.Replace(s, tok, path, 1)
	}
	return s, true
}

func hanPathSpans(s string) [][2]int {
	var spans [][2]int
	add := func(start, end int) {
		if start < 0 || end > len(s) || start >= end {
			return
		}
		if hasHan(s[start:end]) {
			spans = append(spans, [2]int{start, end})
		}
	}
	for _, m := range winPathRe.FindAllStringIndex(s, -1) {
		add(m[0], m[1])
	}
	for _, m := range uncPathRe.FindAllStringIndex(s, -1) {
		add(m[0], m[1])
	}
	for _, m := range nixPathRe.FindAllStringSubmatchIndex(s, -1) {
		if len(m) >= 6 {
			add(m[4], m[5])
		}
	}
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i][0] == spans[j][0] {
			return spans[i][1] > spans[j][1]
		}
		return spans[i][0] < spans[j][0]
	})
	merged := [][2]int{spans[0]}
	for _, sp := range spans[1:] {
		last := &merged[len(merged)-1]
		if sp[0] <= last[1] {
			if sp[1] > last[1] {
				last[1] = sp[1]
			}
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}
