/*
Copyright © 2024 Gareth Watts <gareth@omnipotent.net>

Mostly written by an LLM.
*/
package ast

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/ryanuber/go-glob"
)

// Match holds a matching function reference located by FindFuncDefinitions.
type Match struct {
	Pkg        string
	RecvType   string
	RecvName   string
	FuncName   string
	Filename   string
	OffsetLine int
	OffsetCol  int
}

// MethodName returns the pretty-printed  name of a method or function call.
func (m Match) MethodName() string {
	if m.RecvType != "" {
		if m.RecvName != "" {
			return fmt.Sprintf("(%s %s) %s(...)", m.RecvName, m.RecvType, m.FuncName)
		} else {
			return fmt.Sprintf("(%s) %s(...)", m.RecvType, m.FuncName)
		}
	}
	return fmt.Sprintf("%s(...)", m.FuncName)
}

// ASTProcessor handles parsing source files and extracting method calls.
type ASTProcessor struct {
	fset    *token.FileSet
	fileMap map[string]*ast.File // Cache parsed files
}

// NewASTProcessor creates a new ASTProcessor.
func NewASTProcessor() *ASTProcessor {
	return &ASTProcessor{
		fset:    token.NewFileSet(),
		fileMap: make(map[string]*ast.File),
	}
}

// ParseFile parses a Go source file and caches the AST.
func (a *ASTProcessor) ParseFile(filePath string) error {
	if _, exists := a.fileMap[filePath]; exists {
		return nil // File already parsed
	}

	// Read the file content
	src, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("error reading file %s: %v", filePath, err)
	}

	// Parse the file
	file, err := parser.ParseFile(a.fset, filePath, src, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("error parsing file %s: %v", filePath, err)
	}

	a.fileMap[filePath] = file
	return nil
}

// ExtractFullCall extracts the call expression corresponding to the reference.
func (a *ASTProcessor) ExtractFullCall(filePath string, line, character int) (string, error) {
	// Ensure the file is parsed
	if err := a.ParseFile(filePath); err != nil {
		return "", err
	}

	file := a.fileMap[filePath]
	src, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("error reading file %s: %v", filePath, err)
	}

	// Get the position in token.Pos
	position := a.getPosition(filePath, line, character)
	if position == token.NoPos {
		return "", fmt.Errorf("invalid position")
	}

	var targetCallExpr *ast.CallExpr

	// Walk the AST to find the innermost CallExpr containing the position
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}

		if ce, ok := n.(*ast.CallExpr); ok {
			if ce.Pos() <= position && position <= ce.End() {
				targetCallExpr = ce
				// Continue traversing to find deeper CallExprs
				return true
			}
		}
		return true
	})

	if targetCallExpr == nil {
		return "", fmt.Errorf("no corresponding call expression found")
	}

	// Extract the source code corresponding to the targetCallExpr
	startOffset := a.fset.Position(targetCallExpr.Pos()).Offset
	endOffset := a.fset.Position(targetCallExpr.End()).Offset
	if startOffset < 0 || endOffset > len(src) || startOffset >= endOffset {
		return "", fmt.Errorf("invalid call expression positions")
	}

	snippet := string(src[startOffset:endOffset])
	return snippet, nil
}

// GetEnclosingFunctionName finds the name and receiver type of the function/method
// containing the given position.
func (a *ASTProcessor) GetEnclosingFunctionName(filePath string, line, character int) (functionName, receiverType, receiverName string, err error) {
	// Ensure the file is parsed
	if err := a.ParseFile(filePath); err != nil {
		return "", "", "", err
	}

	file := a.fileMap[filePath]

	// Get the position in token.Pos
	position := a.getPosition(filePath, line, character)
	if position == token.NoPos {
		return "", "", "", fmt.Errorf("invalid position")
	}

	// Find the enclosing function or method
	var found bool

	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || found {
			return false
		}
		if n.Pos() <= position && position <= n.End() {
			switch fn := n.(type) {
			case *ast.FuncDecl:
				functionName = fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					// Get the receiver type
					receiverType = exprToString(fn.Recv.List[0].Type)
					if len(fn.Recv.List[0].Names) > 0 {
						receiverName = fn.Recv.List[0].Names[0].Name
					} else {
						receiverName = "" // Anonymous receiver
					}
				}
				found = true
				return false
			case *ast.FuncLit:
				functionName = "anonymous function"
				receiverType = ""
				found = true
				return false
			}
		}
		return true
	})

	if !found {
		functionName = "global scope"
		receiverType = ""
	}

	return functionName, receiverType, receiverName, nil
}

// FindFuncDefinitions locates the position of all supplied exported functions or methods
// within a package.  funcPattern is one or more globs.
// methods can be specified as `TypeName.MethodName`
func (a *ASTProcessor) FindFuncDefinitions(pkgPath string, funcPattern ...string) (matches []Match, err error) {
	pkg, err := build.Import(pkgPath, "", 0)
	if err != nil {
		return nil, err
	}
	for _, file := range pkg.GoFiles {
		fullPath := filepath.Join(pkg.Dir, file)
		if err := a.ParseFile(fullPath); err != nil {
			return nil, err
		}
		node := a.fileMap[fullPath]
		for _, decl := range node.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				// Check if it's an exported function
				funcName := decl.Name.Name
				if !ast.IsExported(funcName) {
					continue
				}
				if !a.isFuncMatch(decl, funcPattern...) {
					continue
				}

				pos := a.fset.Position(decl.Name.NamePos)
				recvType, recvName := extractRecvType(decl)
				match := Match{
					Pkg:      pkgPath,
					RecvType: recvType,
					RecvName: recvName,
					//RecvType:   extractRecvType(decl),
					FuncName:   decl.Name.Name,
					Filename:   pos.Filename,
					OffsetLine: pos.Line,
					OffsetCol:  pos.Column,
				}
				matches = append(matches, match)
			}
		}
	}
	return matches, err
}

func (a *ASTProcessor) isFuncMatch(node *ast.FuncDecl, funcPatterns ...string) bool {
	recvType, _ := extractRecvType(node)
	recvType = strings.TrimPrefix(recvType, "*")
	funcName := node.Name.Name
	for _, pattern := range funcPatterns {
		matchFunc := pattern
		if structName, methodName, found := strings.Cut(pattern, "."); found {
			if !glob.Glob(structName, recvType) {
				continue
			}
			matchFunc = methodName
		}

		if glob.Glob(matchFunc, funcName) {
			return true // match
		}
	}
	return false
}

// getPosition converts line and character to token.Pos
func (a *ASTProcessor) getPosition(filePath string, line, character int) token.Pos {
	file := a.fset.File(a.fileMap[filePath].Pos())
	if file == nil {
		return token.NoPos
	}
	if line < 1 || line > file.LineCount() {
		return token.NoPos
	}
	lineStart := file.LineStart(line)
	return lineStart + token.Pos(character-1)
}

func extractRecvType(decl *ast.FuncDecl) (recvType, recvName string) {
	recv := decl.Recv
	if recv == nil || len(recv.List) != 1 {
		return "", ""
	}
	recvType = exprToString(recv.List[0].Type)
	if len(recv.List[0].Names) > 0 {
		recvName = recv.List[0].Names[0].Name
	}
	return recvType, recvName
}

// exprToString converts an expression to its string representation.
func exprToString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return "*" + exprToString(e.X)
	case *ast.SelectorExpr:
		return exprToString(e.X) + "." + e.Sel.Name
	case *ast.IndexExpr:
		return exprToString(e.X) + "[" + exprToString(e.Index) + "]"
	case *ast.IndexListExpr:
		var indices []string
		for _, index := range e.Indices {
			indices = append(indices, exprToString(index))
		}
		return exprToString(e.X) + "[" + strings.Join(indices, ", ") + "]"
	case *ast.ArrayType:
		return "[]" + exprToString(e.Elt)
	case *ast.MapType:
		return "map[" + exprToString(e.Key) + "]" + exprToString(e.Value)
	case *ast.FuncType:
		return "func"
	case *ast.InterfaceType:
		return "interface"
	case *ast.StructType:
		return "struct"
	default:
		return fmt.Sprintf("%T", expr)
	}
}

// Format re-formats the supplied source code.
func Format(source string) string {
	fset := token.NewFileSet()
	expr, err := parser.ParseExprFrom(fset, "", []byte(source), 0)
	if err != nil {
		return source
	}
	var buf bytes.Buffer
	prt := &printer.Config{
		Mode:     printer.UseSpaces,
		Tabwidth: 4,
	}
	prt.Fprint(&buf, fset, expr)
	return buf.String()
}
