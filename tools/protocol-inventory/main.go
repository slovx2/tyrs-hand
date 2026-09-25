// 协议调用清单从 Go AST 与 Hub 分类导出，不把源代码文本搜索当成测试覆盖。
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/slovx2/tyrs-hand/internal/appserverhub"
)

type usage struct {
	Method string `json:"method"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Source string `json:"source"`
}

func main() {
	var usages []usage
	known := map[string]bool{}
	for _, name := range []string{"ServerRequest", "ServerNotification", "ClientNotification"} {
		data, err := os.ReadFile(filepath.Join("protocol/codex-app-server/0.147.0/json-schema", name+".json"))
		if err != nil {
			panic(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(data, &schema); err != nil {
			panic(err)
		}
		variants, _ := schema["oneOf"].([]any)
		for _, variant := range variants {
			properties, _ := variant.(map[string]any)["properties"].(map[string]any)
			method, _ := properties["method"].(map[string]any)
			values, _ := method["enum"].([]any)
			for _, value := range values {
				known[value.(string)] = true
			}
		}
	}
	for method := range appserverhub.ClassifiedMethods() {
		usages = append(usages, usage{Method: method, File: "internal/appserverhub/methods.go", Source: "hub"})
	}
	for _, root := range []string{"internal", "mobile"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			positions := token.NewFileSet()
			source, err := parser.ParseFile(positions, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(source, func(node ast.Node) bool {
				if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					value, _ := strconv.Unquote(literal.Value)
					if known[value] {
						usages = append(usages, usage{Method: value, File: path, Line: positions.Position(literal.Pos()).Line, Source: "go-event"})
					}
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "Call", "Notify", "Request":
				default:
					return true
				}
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					if err != nil {
						panic(err)
					}
					if value == "initialize" || strings.Contains(value, "/") {
						usages = append(usages, usage{Method: value, File: path, Line: positions.Position(literal.Pos()).Line, Source: "go-call"})
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			panic(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(usages); err != nil {
		panic(err)
	}
}
