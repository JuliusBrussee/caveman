//go:build !cgo

package repointel

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

func symbolParserBasis() string { return "go-ast-plus-conservative-declaration-patterns" }

func scanSymbols(_ context.Context, path, language string, raw []byte) []Symbol {
	if language == "go" {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, raw, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}
		var out []Symbol
		ast.Inspect(file, func(node ast.Node) bool {
			var name, kind string
			switch value := node.(type) {
			case *ast.FuncDecl:
				name, kind = value.Name.Name, "function"
				if value.Recv != nil {
					kind = "method"
				}
			case *ast.TypeSpec:
				name, kind = value.Name.Name, "type"
			}
			if name != "" {
				out = append(out, Symbol{
					Name: name, Kind: kind,
					LineStart: fset.Position(node.Pos()).Line,
					LineEnd:   fset.Position(node.End()).Line,
				})
			}
			return len(out) < 500
		})
		return out
	}
	pattern := regexp.MustCompile("(?m)^\\s*(?:export\\s+|pub\\s+)?(?:async\\s+)?(class|interface|trait|struct|enum|type|function|def|fn)\\s+([A-Za-z_][A-Za-z0-9_]*)")
	var out []Symbol
	for _, match := range pattern.FindAllSubmatchIndex(raw, 500) {
		before := raw[:match[0]]
		line := 1 + strings.Count(string(before), "\n")
		out = append(out, Symbol{
			Name: string(raw[match[4]:match[5]]), Kind: string(raw[match[2]:match[3]]),
			LineStart: line, LineEnd: line,
		})
	}
	return out
}
