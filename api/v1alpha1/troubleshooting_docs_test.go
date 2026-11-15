package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conditionReasons returns the value of every constant named Reason* in common_types.go.
func conditionReasons(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "common_types.go", nil, 0)
	require.NoError(t, err)

	var reasons []string

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}

		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for index, name := range value.Names {
				if !strings.HasPrefix(name.Name, "Reason") {
					continue
				}

				literal, ok := value.Values[index].(*ast.BasicLit)
				require.True(t, ok, "%s must be a string literal", name.Name)

				unquoted, err := strconv.Unquote(literal.Value)
				require.NoError(t, err)

				reasons = append(reasons, unquoted)
			}
		}
	}

	return reasons
}

func TestTroubleshootingGuideCoversEveryConditionReason(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile("../../docs/admin/troubleshooting.md")
	require.NoError(t, err)

	reasons := conditionReasons(t)
	require.NotEmpty(t, reasons)

	for _, reason := range reasons {
		assert.Contains(t, string(page), "\n### `"+reason+"`\n", "troubleshooting.md has no entry for reason %s", reason)
	}
}
