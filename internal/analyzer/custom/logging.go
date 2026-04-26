package custom

import (
	"go/ast"
	"go/token"
	"strconv"

	"golang.org/x/tools/go/analysis"
)

const slogErrorWithoutErrName = "slog_error_without_err"
const bannedDirectOutputName = "banned_direct_output"
const hotLoopInfoLogName = "hot_loop_info_log"
const missingBoundaryLogName = "missing_boundary_log"

var SlogErrorWithoutErrAnalyzer = &analysis.Analyzer{
	Name: slogErrorWithoutErrName,
	Doc:  "reports slog.Error calls that do not include an err field",
	Run:  runSlogErrorWithoutErr,
}

var BannedDirectOutputAnalyzer = &analysis.Analyzer{
	Name: bannedDirectOutputName,
	Doc:  "reports direct stdout and stderr printing in production code",
	Run:  runBannedDirectOutput,
}

var HotLoopInfoLogAnalyzer = &analysis.Analyzer{
	Name: hotLoopInfoLogName,
	Doc:  "reports info-level logging inside loops",
	Run:  runHotLoopInfoLog,
}

var MissingBoundaryLogAnalyzer = &analysis.Analyzer{
	Name: missingBoundaryLogName,
	Doc:  "reports exported functions with error returns that appear to perform side effects without logging",
	Run:  runMissingBoundaryLog,
}

func runSlogErrorWithoutErr(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selectorName(call.Fun) != "Error" || selectorReceiver(call.Fun) != "slog" {
				return true
			}
			for _, arg := range call.Args[1:] {
				if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					name, err := strconv.Unquote(lit.Value)
					if err == nil && name == "err" {
						return true
					}
				}
			}
			pass.Report(analysis.Diagnostic{
				Pos:      call.Pos(),
				End:      call.End(),
				Category: slogErrorWithoutErrName,
				Message:  "slog.Error call has no \"err\" field",
			})
			return true
		})
	}
	return nil, nil
}

func runBannedDirectOutput(pass *analysis.Pass) (any, error) {
	banned := map[string]bool{
		"fmt.Print":   true,
		"fmt.Printf":  true,
		"fmt.Println": true,
		"log.Print":   true,
		"log.Printf":  true,
		"log.Println": true,
		"log.Fatal":   true,
		"log.Fatalf":  true,
		"log.Fatalln": true,
		"log.Panic":   true,
		"log.Panicf":  true,
		"log.Panicln": true,
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !banned[callName(call.Fun)] {
				return true
			}
			pass.Report(analysis.Diagnostic{
				Pos:      call.Pos(),
				End:      call.End(),
				Category: bannedDirectOutputName,
				Message:  "direct fmt/log output bypasses structured logging",
			})
			return true
		})
	}
	return nil, nil
}

func runHotLoopInfoLog(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch loop := node.(type) {
			case *ast.ForStmt:
				checkLoopForInfoLogs(pass, loop.Body)
			case *ast.RangeStmt:
				checkLoopForInfoLogs(pass, loop.Body)
			}
			return true
		})
	}
	return nil, nil
}

func runMissingBoundaryLog(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil || !fn.Name.IsExported() {
				continue
			}
			if !returnsError(fn) || !containsSideEffect(fn.Body) || containsStructuredLog(fn.Body) {
				continue
			}
			pass.Report(analysis.Diagnostic{
				Pos:      fn.Pos(),
				End:      fn.End(),
				Category: missingBoundaryLogName,
				Message:  "exported side-effecting function returning an error has no structured boundary log",
			})
		}
	}
	return nil, nil
}

func checkLoopForInfoLogs(pass *analysis.Pass, body *ast.BlockStmt) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call.Fun) != "Info" {
			return true
		}
		recv := selectorReceiver(call.Fun)
		if recv != "slog" && recv != "log" {
			return true
		}
		pass.Report(analysis.Diagnostic{
			Pos:      call.Pos(),
			End:      call.End(),
			Category: hotLoopInfoLogName,
			Message:  "info-level logging inside a loop may create excessive log volume",
		})
		return true
	})
}

func selectorName(expr ast.Expr) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

func selectorReceiver(expr ast.Expr) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func callName(expr ast.Expr) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name + "." + sel.Sel.Name
}

func returnsError(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, result := range fn.Type.Results.List {
		ident, ok := result.Type.(*ast.Ident)
		if ok && ident.Name == "error" {
			return true
		}
	}
	return false
}

func containsSideEffect(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if name == "" {
			return true
		}
		if name == "os.WriteFile" || name == "os.Remove" || name == "exec.Command" || name == "http.Get" || name == "http.Post" {
			found = true
			return false
		}
		if selector := selectorName(call.Fun); selector == "Exec" || selector == "Query" || selector == "Do" || selector == "Open" || selector == "Create" {
			found = true
			return false
		}
		return true
	})
	return found
}

func containsStructuredLog(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector := selectorName(call.Fun)
		recv := selectorReceiver(call.Fun)
		if recv == "slog" && (selector == "Info" || selector == "Warn" || selector == "Error" || selector == "Debug") {
			found = true
			return false
		}
		return true
	})
	return found
}
