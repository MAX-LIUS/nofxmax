package trader

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestStopToleranceIsDecidedBeforeOwnershipLog pins the 2026-07-28 SKHYNIXUSDT
// semantic contradiction. The extra-protective-stop tolerance branch flips
// ownership.State from "degraded" back to "protected"/Verified, but it used to
// sit BELOW the 🧭 log. So SKHYNIXUSDT printed `state=degraded verified=false`
// for 465 consecutive cycles and then, on the very next line, `✅ exchange
// protection verified`. Both lines were individually correct — they just belonged
// to two different moments — and the reader saw only a contradiction.
//
// This is asserted structurally rather than behaviorally because the defect IS
// the statement order: a behavioral test on the returned ownership value passes
// either way, since the flip happens in both versions. Only the position of the
// log call relative to the mutation distinguishes them.
func TestStopToleranceIsDecidedBeforeOwnershipLog(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "protection_reconciler.go", nil, 0)
	if err != nil {
		t.Fatalf("parse protection_reconciler.go: %v", err)
	}

	tolerateLine, logLine := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		switch {
		case strings.Contains(lit.Value, "preserving extra protective stop orders"):
			tolerateLine = fset.Position(lit.Pos()).Line
		case strings.Contains(lit.Value, "Protection ownership:"):
			logLine = fset.Position(lit.Pos()).Line
		}
		return true
	})

	if tolerateLine == 0 {
		t.Fatal("could not locate the stop-tolerance branch; if it was renamed, update this test rather than deleting it")
	}
	if logLine == 0 {
		t.Fatal("could not locate the 🧭 ownership log line")
	}
	if tolerateLine > logLine {
		t.Fatalf("stop tolerance (line %d) runs AFTER the ownership log (line %d): the log will print the pre-tolerance state, so a tolerated position reads as degraded on one line and verified on the next",
			tolerateLine, logLine)
	}
}

// TestOwnershipLogReportsToleratedStopCount guards the second half of the fix:
// once the tolerance runs first, the log's unexpectedSL is the POST-tolerance 0,
// which on its own hides that anything was tolerated at all. The log must carry
// the pre-tolerance count too, otherwise "unexpectedSL=0" and the neighbouring 🛡
// line (which prints the pre-tolerance count) read as contradicting each other.
func TestOwnershipLogReportsToleratedStopCount(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "protection_reconciler.go", nil, 0)
	if err != nil {
		t.Fatalf("parse protection_reconciler.go: %v", err)
	}

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || !strings.Contains(lit.Value, "Protection ownership:") {
			return true
		}
		// The marker is built into a variable that the format string interpolates,
		// so assert on the surrounding function body containing its construction.
		found = strings.Contains(lit.Value, "unexpectedTP=%d%s")
		return false
	})

	if !found {
		t.Fatal("🧭 log must interpolate the stopTolerated marker right after unexpectedTP, so a tolerated (N→0) flip is visible in the same line as the zero it produced")
	}
}
