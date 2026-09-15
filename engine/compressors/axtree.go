package compressors

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/JuliusBrussee/caveman/engine/safety"
)

const a11yType = "a11y"

var a11yStateKeys = map[string]bool{
	"disabled": true,
	"checked":  true,
	"expanded": true,
	"selected": true,
	"focused":  true,
	"editable": true,
}

// NewAXTree returns the forced-only CDP Accessibility.getFullAXTree compressor.
// It emits a compact, deterministic indented view with uid handles while CCR
// keeps the raw AX payload byte-exact. Detect never routes here; callers must
// force Options.Type = "a11y".
func NewAXTree() Compressor { return axTreeCompressor{} }

type axTreeCompressor struct{}

func (axTreeCompressor) ContentType() string       { return a11yType }
func (axTreeCompressor) SafetyClass() safety.Class { return safety.S4 }

func (c axTreeCompressor) Compress(input []byte) ([]byte, bool) {
	out, _, ok := c.CompressWithMetadata(input, "")
	return out, ok
}

func (axTreeCompressor) CompressWithMetadata(input []byte, query string) ([]byte, Metadata, bool) {
	nodes, ok := parseAXTree(input)
	if !ok {
		return nil, Metadata{}, false
	}
	records, uidMap := curateAXTree(nodes)
	if len(records) == 0 {
		return nil, Metadata{}, false
	}
	records = focusAXRecords(records, query)
	uidMap = visibleUIDTargets(records, uidMap)
	uidJSON, err := json.Marshal(map[string]map[string]axUIDTarget{"uids": uidMap})
	if err != nil {
		return nil, Metadata{}, false
	}
	return renderAXRecords(records), Metadata{Method: a11yType, LosslessToModel: metadataBool(false), RecoveryMetadata: uidJSON}, true
}

type axTreeEnvelope struct {
	Nodes []axNode `json:"nodes"`
}

type axNode struct {
	NodeID            string       `json:"nodeId"`
	ParentID          string       `json:"parentId"`
	Ignored           bool         `json:"ignored"`
	Role              axValue      `json:"role"`
	Name              axValue      `json:"name"`
	Value             axValue      `json:"value"`
	Properties        []axProperty `json:"properties"`
	ChildIDs          []string     `json:"childIds"`
	BackendDOMNodeID  int          `json:"backendDOMNodeId"`
	BackendDOMNodeID2 int          `json:"backendDOMNodeID"`
	FrameID           string       `json:"frameId"`
	ChromeFrameID     string       `json:"frameID"`
}

type axValue struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type axProperty struct {
	Name  string  `json:"name"`
	Value axValue `json:"value"`
}

type axRecord struct {
	Depth int
	UID   string
	Role  string
	Name  string
	Value string
	State map[string]bool
}

type axUIDTarget struct {
	BackendDOMNodeID int    `json:"backendDOMNodeId"`
	FrameID          string `json:"frameId,omitempty"`
	NodeID           string `json:"nodeId,omitempty"`
}

func parseAXTree(input []byte) ([]axNode, bool) {
	if len(bytes.TrimSpace(input)) == 0 || !json.Valid(input) {
		return nil, false
	}
	var nodes []axNode
	if err := json.Unmarshal(input, &nodes); err == nil && len(nodes) > 0 {
		return nodes, validAXTree(nodes)
	}
	var env axTreeEnvelope
	if err := json.Unmarshal(input, &env); err == nil && len(env.Nodes) > 0 {
		return env.Nodes, validAXTree(env.Nodes)
	}
	return nil, false
}

func validAXTree(nodes []axNode) bool {
	seen := make(map[string]bool, len(nodes))
	nonIgnored := 0
	for _, n := range nodes {
		if n.NodeID == "" || seen[n.NodeID] {
			return false
		}
		seen[n.NodeID] = true
		if n.Ignored {
			continue
		}
		nonIgnored++
		if compactAXString(n.Role.Value) == "" {
			return false
		}
	}
	if nonIgnored == 0 {
		return false
	}
	// A childId that resolves to no node in this payload is NOT a malformed
	// tree: Accessibility.getFullAXTree returns one frame at a time, so an
	// <iframe> node references a child document node that legitimately lives in
	// another frame's response. Treat such a childId as a leaf boundary (walk
	// skips unknown ids) rather than rejecting the whole tree — over-returning a
	// usable uid map is safe; refusing to compress the page is not.
	return true
}

func curateAXTree(nodes []axNode) ([]axRecord, map[string]axUIDTarget) {
	byID := make(map[string]axNode, len(nodes))
	childRefs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.NodeID != "" {
			byID[n.NodeID] = n
		}
		for _, id := range n.ChildIDs {
			childRefs[id] = true
		}
	}

	roots := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.NodeID != "" && !childRefs[n.NodeID] {
			roots = append(roots, n.NodeID)
		}
	}
	if len(roots) == 0 {
		for _, n := range nodes {
			if n.NodeID != "" {
				roots = append(roots, n.NodeID)
			}
		}
	}

	visited := make(map[string]bool, len(nodes))
	out := make([]axRecord, 0, len(nodes))
	uidMap := make(map[string]axUIDTarget, len(nodes))
	seenUIDs := make(map[string]int, len(nodes))
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if id == "" || visited[id] {
			return
		}
		n, ok := byID[id]
		if !ok {
			return
		}
		visited[id] = true
		if n.Ignored {
			for _, child := range n.ChildIDs {
				walk(child, depth)
			}
			return
		}

		rec := normalizeAXNode(n, depth)
		if shouldExposeAXUID(rec, n) {
			base := axUIDBase(n)
			rec.UID = uniqueAXUID(base, seenUIDs)
			uidMap[rec.UID] = axUIDTarget{
				BackendDOMNodeID: backendID(n),
				FrameID:          frameID(n),
				NodeID:           n.NodeID,
			}
		}
		drop := droppableGeneric(rec, n)
		if !drop {
			out = append(out, rec)
			depth++
		}
		for _, child := range n.ChildIDs {
			walk(child, depth)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	for _, n := range nodes {
		if n.NodeID != "" && !visited[n.NodeID] {
			walk(n.NodeID, 0)
		}
	}
	out = pruneDuplicateAXText(out)
	return out, visibleUIDTargets(out, uidMap)
}

func normalizeAXNode(n axNode, depth int) axRecord {
	rec := axRecord{
		Depth: depth,
		Role:  compactAXString(n.Role.Value),
		Name:  compactAXString(n.Name.Value),
		Value: compactAXString(n.Value.Value),
	}
	state := axState(n)
	if len(state) > 0 {
		rec.State = state
	}
	return rec
}

func axState(n axNode) map[string]bool {
	out := map[string]bool{}
	for _, p := range n.Properties {
		if !a11yStateKeys[p.Name] {
			continue
		}
		v, ok := axStateBool(p.Name, p.Value.Value)
		if ok {
			out[p.Name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func droppableGeneric(rec axRecord, n axNode) bool {
	role := strings.ToLower(rec.Role)
	// InlineTextBox repeats its semantic parent's text at layout-fragment
	// granularity. It is never an action target and is the largest source of AX
	// snapshot duplication. LabelText is likewise only a wrapper when empty.
	if role == "inlinetextbox" {
		return true
	}
	if role == "labeltext" && rec.Name == "" && rec.Value == "" && len(rec.State) == 0 {
		return true
	}
	if role != "generic" && role != "presentational" && role != "none" {
		return false
	}
	if rec.Name != "" || rec.Value != "" {
		return false
	}
	if len(rec.State) > 0 && !(len(rec.State) == 1 && rec.State["editable"]) {
		return false
	}
	for _, p := range n.Properties {
		if p.Name == "focusable" || p.Name == "clickable" {
			if v, ok := axLooseBool(p.Value.Value); ok && v {
				return false
			}
		}
	}
	return true
}

func backendID(n axNode) int {
	id := n.BackendDOMNodeID
	if id == 0 {
		id = n.BackendDOMNodeID2
	}
	return id
}

func frameID(n axNode) string {
	frame := n.FrameID
	if frame == "" {
		frame = n.ChromeFrameID
	}
	return frame
}

func axUIDBase(n axNode) string {
	id := backendID(n)
	if id <= 0 {
		return ""
	}
	// Backend node ids are unique inside the single CDP target supported by the
	// Phase-1 driver. Frame ids remain in recovery metadata for future OOPIF
	// stitching; embedding a base64 frame id in every visible uid spent dozens of
	// tokens while the action driver ignored it.
	return "u" + strconv.FormatInt(int64(id), 36)
}

func shouldExposeAXUID(rec axRecord, n axNode) bool {
	if backendID(n) <= 0 {
		return false
	}
	switch strings.ToLower(rec.Role) {
	case "rootwebarea", "webarea":
		return false
	case "button", "checkbox", "combobox", "link", "menuitem", "menuitemcheckbox",
		"menuitemradio", "option", "radio", "searchbox", "slider", "spinbutton",
		"switch", "tab", "textbox", "treeitem":
		return true
	}
	for _, p := range n.Properties {
		if p.Name == "focusable" || p.Name == "clickable" {
			if v, ok := axLooseBool(p.Value.Value); ok && v {
				return true
			}
		}
	}
	switch strings.ToLower(rec.Role) {
	case "main", "navigation", "region", "group", "generic", "none", "presentational",
		"heading", "paragraph", "statictext", "inlinetextbox", "labeltext", "list",
		"listitem", "table", "row", "cell", "rowheader", "columnheader", "grid", "tree",
		"directory", "separator", "toolbar", "banner", "complementary", "contentinfo",
		"article", "figure", "img", "image", "form", "status", "alert", "log", "timer",
		"tooltip", "document":
		return false
	default:
		// Unknown roles stay visible and actionable. Omitting their handle would
		// silently turn a custom control into read-only text.
		return true
	}
}

func pruneDuplicateAXText(records []axRecord) []axRecord {
	semanticText := make(map[string]bool)
	for _, rec := range records {
		role := strings.ToLower(rec.Role)
		if role == "statictext" || role == "inlinetextbox" {
			continue
		}
		for _, value := range []string{rec.Name, rec.Value} {
			if key := strings.ToLower(strings.TrimSpace(value)); key != "" {
				semanticText[key] = true
			}
		}
	}
	out := make([]axRecord, 0, len(records))
	for _, rec := range records {
		if strings.EqualFold(rec.Role, "StaticText") {
			key := strings.ToLower(strings.TrimSpace(rec.Name))
			if key != "" && semanticText[key] {
				continue
			}
			// Layout-derived state on static text duplicates the owning control.
			rec.State = nil
		}
		out = append(out, rec)
	}
	return out
}

func focusAXRecords(records []axRecord, query string) []axRecord {
	terms := axQueryTerms(query)
	if len(terms) == 0 || len(records) == 0 {
		return records
	}
	scores := make([]int, len(records))
	maxScore := 0
	for i, rec := range records {
		haystack := strings.ToLower(rec.Role + " " + rec.Name + " " + rec.Value + " " + strings.Join(axStateLabels(rec.State), " "))
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				scores[i]++
			}
		}
		if scores[i] > maxScore {
			maxScore = scores[i]
		}
	}
	if maxScore == 0 {
		root := records[0]
		root.Depth = 0
		return []axRecord{root, {Depth: 1, Role: "note", Name: "no accessible match"}}
	}

	keep := make([]bool, len(records))
	matches := 0
	// Highest-coverage matches win the bounded result, but distinct task terms
	// may name distinct controls ("Email Plan Save"). Fill remaining slots from
	// lower scores instead of returning only the single record with most terms.
	minScore := 1
	if maxScore == len(terms) {
		// At least one node covers the whole query. Partial matches on common
		// fragments (every ORD-* row matching "ord") are noise, not another intent.
		minScore = maxScore
	}
	for wantedScore := maxScore; wantedScore >= minScore && matches < 12; wantedScore-- {
		for i, score := range scores {
			if score != wantedScore || matches >= 12 {
				continue
			}
			keep[i] = true
			matches++
			wantDepth := records[i].Depth - 1
			for j := i - 1; j >= 0 && wantDepth >= 0; j-- {
				if records[j].Depth == wantDepth {
					keep[j] = true
					wantDepth--
				}
			}
		}
	}

	// A match inside a row or list item answers a lookup only together with
	// its sibling cells — an order id without its customer/amount cells reads
	// as "data unavailable". Keep the whole line item, not just the matching
	// cell. Subtree size is bounded by real row width, not by the match cap.
	for i := range records {
		if !keep[i] {
			continue
		}
		item := -1
		if records[i].Role == "row" || records[i].Role == "listitem" {
			item = i
		}
		wantDepth := records[i].Depth - 1
		for j := i; j >= 0 && item == -1 && wantDepth >= 0; j-- {
			if records[j].Depth != wantDepth {
				continue
			}
			if records[j].Role == "row" || records[j].Role == "listitem" {
				item = j
				break
			}
			wantDepth--
		}
		if item == -1 {
			continue
		}
		for j := item; j < len(records) && (j == item || records[j].Depth > records[item].Depth); j++ {
			keep[j] = true
		}
	}

	out := make([]axRecord, 0, matches*2)
	keptDepth := make(map[int]bool)
	for i, rec := range records {
		for depth := range keptDepth {
			if depth >= rec.Depth {
				delete(keptDepth, depth)
			}
		}
		if !keep[i] {
			continue
		}
		depth := 0
		for ancestor := 0; ancestor < rec.Depth; ancestor++ {
			if keptDepth[ancestor] {
				depth++
			}
		}
		rec.Depth = depth
		out = append(out, rec)
		keptDepth[records[i].Depth] = true
	}
	return out
}

func axQueryTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func visibleUIDTargets(records []axRecord, all map[string]axUIDTarget) map[string]axUIDTarget {
	out := make(map[string]axUIDTarget)
	for _, rec := range records {
		if rec.UID == "" {
			continue
		}
		if target, ok := all[rec.UID]; ok {
			out[rec.UID] = target
		}
	}
	return out
}

func renderAXRecords(records []axRecord) []byte {
	var b bytes.Buffer
	for i, rec := range records {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", rec.Depth))
		if rec.UID != "" {
			b.WriteByte('[')
			b.WriteString(rec.UID)
			b.WriteString("] ")
		}
		b.WriteString(compactAXRole(rec.Role))
		if rec.Name != "" {
			b.WriteByte(' ')
			b.WriteString(strconv.Quote(rec.Name))
		}
		if rec.Value != "" && rec.Value != rec.Name {
			b.WriteString(" = ")
			b.WriteString(strconv.Quote(rec.Value))
		}
		if states := axStateLabels(rec.State); len(states) > 0 {
			b.WriteString(" {")
			b.WriteString(strings.Join(states, ","))
			b.WriteByte('}')
		}
	}
	return b.Bytes()
}

func compactAXRole(role string) string {
	switch strings.ToLower(role) {
	case "rootwebarea", "webarea":
		return "page"
	case "statictext":
		return "text"
	case "labeltext":
		return "label"
	default:
		return strings.ToLower(role)
	}
}

func axStateLabels(state map[string]bool) []string {
	if len(state) == 0 {
		return nil
	}
	out := make([]string, 0, len(state))
	if state["disabled"] {
		out = append(out, "disabled")
	}
	if checked, ok := state["checked"]; ok {
		if checked {
			out = append(out, "checked")
		} else {
			out = append(out, "unchecked")
		}
	}
	if expanded, ok := state["expanded"]; ok {
		if expanded {
			out = append(out, "expanded")
		} else {
			out = append(out, "collapsed")
		}
	}
	if state["selected"] {
		out = append(out, "selected")
	}
	if state["focused"] {
		out = append(out, "focused")
	}
	if state["editable"] {
		out = append(out, "editable")
	}
	return out
}

func uniqueAXUID(base string, seen map[string]int) string {
	seen[base]++
	if seen[base] == 1 {
		return base
	}
	return base + "_" + strconv.Itoa(seen[base])
}

func compactAXString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.Join(strings.Fields(x), " ")
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}
}

func axStateBool(key string, v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no", "":
			return false, true
		case "mixed":
			return key == "checked", key == "checked"
		case "plaintext", "richtext":
			return key == "editable", key == "editable"
		default:
			return false, false
		}
	case float64:
		switch key {
		case "disabled", "checked", "expanded", "selected", "focused":
			return x != 0, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func axLooseBool(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no", "":
			return false, true
		default:
			return false, false
		}
	case float64:
		return x != 0, true
	default:
		return false, false
	}
}
