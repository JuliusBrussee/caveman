// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	factMinLen    = 3
	factMaxLen    = 120
	factMaxScan   = 262144
	factMaxChunk  = 512
	factMaxSeen   = 2048
	factMaxTokens = 64
	factMaxURLs   = 8
)

type factPattern struct {
	re           *regexp.Regexp
	capture      int
	requireDigit bool
}

var factPatterns = []factPattern{
	{re: regexp.MustCompile(`\bhttps?://[^\s)"'<>]+`)},
	{re: regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)},
	{re: regexp.MustCompile(`(?:[\w@~+-]+)?(?:/[\w.@+-]+)+\.[A-Za-z]\w{0,8}\b`)},
	{re: regexp.MustCompile(`/[\w.@+-]+(?:/[\w.@+-]+)+/?`)},
	// Go regexp has no lookahead; this ports (?=[0-9a-f]*\d) by matching then verifying contains digit.
	{re: regexp.MustCompile(`\b[0-9a-f]{7,40}\b`), requireDigit: true},
	{re: regexp.MustCompile(`\bv?\d+\.\d+(?:\.\d+)?(?:[-+][\w.]+)?\b`)},
	{re: regexp.MustCompile(`(?:^|[^\w-])(--?[A-Za-z][\w-]+)`), capture: 1},
	{re: regexp.MustCompile(`\b\d[\d,_]{3,}\b`)},
	{re: regexp.MustCompile(`\b\d+\.\d+\b`)},
	{re: regexp.MustCompile(`\b[A-Z][A-Z0-9]{2,}(?:_[A-Z0-9]+)+\b`)},
	{re: regexp.MustCompile(`\b[A-Z][A-Z0-9]+(?:-[A-Z0-9]+)+\b`), requireDigit: true},
}

var (
	shapeUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	shapeHex    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	shapeConst  = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,}(?:_[A-Z0-9]+)+$`)
	shapeTicket = regexp.MustCompile(`^[A-Z][A-Z0-9]+(?:-[A-Z0-9]+)+$`)
	shapeFlag   = regexp.MustCompile(`^--?[A-Za-z][\w-]+$`)
	shapeNum    = regexp.MustCompile(`^\d[\d,_]*$|^\d+\.\d+$`)
	shapeURL    = regexp.MustCompile(`^https?://`)
)

type FactSheetEntry struct {
	Token string
	Count int
}

func ExtractFactSheetTokens(text string) []string {
	entries := ExtractFactSheetEntries(text)
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Token
	}
	return out
}

func ExtractFactSheetEntries(text string) []FactSheetEntry {
	scan := text
	if len(scan) > factMaxScan {
		scan = scan[:factMaxScan]
	}
	counts := make(map[string]int)
	for _, chunk := range regexp.MustCompile(`\s+`).Split(scan, -1) {
		if len(chunk) < factMinLen || len(chunk) > factMaxChunk {
			continue
		}
		spanSeen := make(map[string]struct{})
		for _, pat := range factPatterns {
			matches := pat.re.FindAllStringSubmatchIndex(chunk, -1)
			for _, m := range matches {
				start, end := m[0], m[1]
				if pat.capture > 0 && len(m) > pat.capture*2+1 && m[pat.capture*2] >= 0 {
					start, end = m[pat.capture*2], m[pat.capture*2+1]
				}
				tok := strings.TrimRight(chunk[start:end], ".,;:!?")
				if len(tok) < factMinLen || len(tok) > factMaxLen {
					continue
				}
				if pat.requireDigit && !containsDigit(tok) {
					continue
				}
				key := chunkKey(start, tok)
				if _, ok := spanSeen[key]; ok {
					continue
				}
				spanSeen[key] = struct{}{}
				counts[tok]++
			}
		}
		if len(counts) >= factMaxSeen {
			break
		}
	}
	return rankFactEntries(counts)
}

func chunkKey(start int, tok string) string {
	return string(rune(start)) + "\x00" + tok
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func rankFactEntries(counts map[string]int) []FactSheetEntry {
	ordered := make([]string, 0, len(counts))
	for tok := range counts {
		ordered = append(ordered, tok)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	var specific []string
	for _, tok := range ordered {
		contained := false
		for _, kept := range specific {
			if strings.Contains(kept, tok) {
				contained = true
				break
			}
		}
		if !contained {
			specific = append(specific, tok)
		}
	}
	sort.Slice(specific, func(i, j int) bool {
		ti, tj := priorityTier(specific[i]), priorityTier(specific[j])
		if ti != tj {
			return ti < tj
		}
		if len(specific[i]) != len(specific[j]) {
			return len(specific[i]) > len(specific[j])
		}
		return specific[i] < specific[j]
	})
	kept := make([]FactSheetEntry, 0, min(len(specific), factMaxTokens))
	urls := 0
	for _, tok := range specific {
		if len(kept) >= factMaxTokens {
			break
		}
		tier := priorityTier(tok)
		if tier == 2 {
			if urls >= factMaxURLs {
				continue
			}
			urls++
		}
		kept = append(kept, FactSheetEntry{Token: tok, Count: counts[tok]})
	}
	return kept
}

func priorityTier(tok string) int {
	if (shapeHex.MatchString(tok) && containsDigit(tok)) ||
		shapeUUID.MatchString(tok) ||
		shapeConst.MatchString(tok) ||
		(shapeTicket.MatchString(tok) && containsDigit(tok)) ||
		shapeFlag.MatchString(tok) ||
		shapeNum.MatchString(tok) {
		return 0
	}
	if shapeURL.MatchString(tok) {
		return 2
	}
	return 1
}

func ExtractFactSheetEntriesAllPages(text string, charsPerPage int) (kept []FactSheetEntry, dropped int) {
	if charsPerPage <= 0 {
		charsPerPage = DenseContentCharsPerImage
	}
	counts := make(map[string]int)
	var all []string
	pageCount := max(1, int(mathCeilDiv(len(text), charsPerPage)))
	for i := 0; i < pageCount; i++ {
		start := i * charsPerPage
		end := min(len(text), start+charsPerPage)
		for _, entry := range ExtractFactSheetEntries(text[start:end]) {
			if _, ok := counts[entry.Token]; !ok {
				all = append(all, entry.Token)
			}
			counts[entry.Token] += entry.Count
		}
	}
	ranked := make([]string, len(all))
	copy(ranked, all)
	sort.Slice(ranked, func(i, j int) bool {
		ti, tj := priorityTier(ranked[i]), priorityTier(ranked[j])
		if ti != tj {
			return ti < tj
		}
		if len(ranked[i]) != len(ranked[j]) {
			return len(ranked[i]) > len(ranked[j])
		}
		return ranked[i] < ranked[j]
	})
	urls := 0
	for _, tok := range ranked {
		if len(kept) >= factMaxTokens {
			break
		}
		tier := priorityTier(tok)
		if tier == 2 {
			if urls >= factMaxURLs {
				continue
			}
			urls++
		}
		kept = append(kept, FactSheetEntry{Token: tok, Count: counts[tok]})
	}
	return kept, len(all) - len(kept)
}

func mathCeilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

const factOpen = "[Exact identifiers from the rendered context above (paths, ids, versions, numbers) — quote these verbatim instead of transcribing them from the image: "
const factOpenCounts = "[Exact identifiers from the rendered context above (paths, ids, versions, numbers) — quote these verbatim instead of transcribing them from the image; ×N marks a token that occurs N times within the imaged content: "

func FactSheetTextFromEntries(entries []FactSheetEntry) string {
	if len(entries) == 0 {
		return ""
	}
	anyRepeat := false
	for _, entry := range entries {
		if entry.Count >= 2 {
			anyRepeat = true
			break
		}
	}
	parts := make([]string, len(entries))
	for i, entry := range entries {
		parts[i] = entry.Token
		if entry.Count >= 2 {
			parts[i] += " ×" + strconvItoa(entry.Count)
		}
	}
	open := factOpen
	if anyRepeat {
		open = factOpenCounts
	}
	return open + strings.Join(parts, " · ") + "]"
}

func FactSheetText(source string, maxChars int) string {
	if maxChars > 0 && len(source) > maxChars {
		source = source[:maxChars]
	}
	return FactSheetTextFromEntries(ExtractFactSheetEntries(source))
}

func strconvItoa(n int) string {
	return commaFreeInt(n)
}

func commaFreeInt(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
