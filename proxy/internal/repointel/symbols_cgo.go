//go:build cgo

package repointel

import (
	"context"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

func symbolParserBasis() string { return "tree-sitter-cgo" }

func scanSymbols(ctx context.Context, _ string, language string, raw []byte) []Symbol {
	grammar := symbolLanguage(language)
	if grammar == nil {
		return nil
	}
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(grammar)
	tree, err := parser.ParseCtx(ctx, nil, raw)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	var out []Symbol
	collectSymbols(tree.RootNode(), raw, &out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LineStart == out[j].LineStart {
			return out[i].Name < out[j].Name
		}
		return out[i].LineStart < out[j].LineStart
	})
	if len(out) > 500 {
		out = out[:500]
	}
	return out
}

func symbolLanguage(language string) *sitter.Language {
	switch language {
	case "go":
		return golang.GetLanguage()
	case "python":
		return python.GetLanguage()
	case "typescript":
		return typescript.GetLanguage()
	case "javascript":
		return javascript.GetLanguage()
	case "rust":
		return rust.GetLanguage()
	case "java":
		return java.GetLanguage()
	case "c":
		return c.GetLanguage()
	case "cpp":
		return cpp.GetLanguage()
	default:
		return nil
	}
}

var symbolKinds = map[string]string{
	"function_declaration": "function", "function_definition": "function", "function_item": "function",
	"method_declaration": "method", "method_definition": "method",
	"class_declaration": "class", "class_definition": "class",
	"interface_declaration": "interface", "trait_item": "trait",
	"type_declaration": "type", "type_alias_declaration": "type",
	"struct_item": "struct", "enum_item": "enum",
}

func collectSymbols(node *sitter.Node, raw []byte, out *[]Symbol) {
	if node == nil || len(*out) >= 500 {
		return
	}
	if kind, ok := symbolKinds[node.Type()]; ok {
		name := node.ChildByFieldName("name")
		if name == nil {
			name = firstIdentifier(node)
		}
		if name != nil {
			value := strings.TrimSpace(name.Content(raw))
			if value != "" && len(value) <= 256 {
				*out = append(*out, Symbol{
					Name: value, Kind: kind,
					LineStart: int(node.StartPoint().Row) + 1,
					LineEnd:   int(node.EndPoint().Row) + 1,
				})
			}
		}
	}
	for i := uint32(0); i < node.NamedChildCount(); i++ {
		collectSymbols(node.NamedChild(int(i)), raw, out)
	}
}

func firstIdentifier(node *sitter.Node) *sitter.Node {
	if node == nil {
		return nil
	}
	switch node.Type() {
	case "identifier", "type_identifier", "field_identifier":
		return node
	}
	for i := uint32(0); i < node.NamedChildCount(); i++ {
		if found := firstIdentifier(node.NamedChild(int(i))); found != nil {
			return found
		}
	}
	return nil
}
