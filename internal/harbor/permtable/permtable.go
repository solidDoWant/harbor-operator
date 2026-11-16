// Package permtable derives the Harbor permissions each custom resource kind needs from the annotations on the
// internal/harbor client methods. A method's doc comment carries one directive line per permission:
//
//	//harbor:permission <scope> <resource> <action> [user-credentials|content-deletion]
//
// The scope is "system", "project" or "own" (the scope of the kind's own robots: project for a project robot, system
// for a system robot). A method that needs no permission carries "//harbor:permission none". The optional last word
// "user-credentials" marks a permission only needed when the connection's credentials are a user's: robot credentials
// replace robots instead. The optional last word "content-deletion" marks a permission only needed when a project on the
// connection is deleted with its contents.
package permtable

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const directive = "//harbor:permission "

// Entry is one required permission of one kind.
type Entry struct {
	Kind     string
	Scope    string
	Resource string
	Action   string
	// UserCredentialsOnly is true when robot credentials do not need the permission.
	UserCredentialsOnly bool
	// ContentDeletionOnly is true when only deleting a project with its contents needs the permission.
	ContentDeletionOnly bool
}

// Annotation is one permission a client method needs. Scope is "own" until a kind resolves it.
type Annotation struct {
	Scope               string
	Resource            string
	Action              string
	UserCredentialsOnly bool
	ContentDeletionOnly bool
}

// Methods maps each exported *Client method to its annotations. A method with "none" maps to an empty slice.
type Methods map[string][]Annotation

// KindFiles lists, for each kind, the controller files holding its Harbor calls, and the scope of its own robots.
var KindFiles = []struct {
	Kind     string
	OwnScope string
	Files    []string
}{
	{Kind: "HarborProject", OwnScope: "project", Files: []string{"harborproject_controller.go", "harborproject_deletion.go", "harborproject_contents.go"}},
	{Kind: "HarborProjectRobot", OwnScope: "project", Files: []string{"harborprojectrobot_controller.go", "robot_lifecycle.go"}},
	{Kind: "HarborRetentionPolicy", OwnScope: "project", Files: []string{"harborretentionpolicy_controller.go"}},
	{Kind: "HarborImmutabilityPolicy", OwnScope: "project", Files: []string{"harborimmutabilitypolicy_controller.go"}},
	{Kind: "HarborRegistry", OwnScope: "system", Files: []string{"harborregistry_controller.go", "harborregistry_deletion.go"}},
	{Kind: "HarborSystemRobot", OwnScope: "system", Files: []string{"harborsystemrobot_controller.go", "robot_lifecycle.go"}},
}

// ParseMethods reads the annotations from the non-test Go files of the directory.
func ParseMethods(dir string) (Methods, error) {
	files, err := parseDir(dir)
	if err != nil {
		return nil, err
	}

	methods := Methods{}

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !isClientReceiver(fn.Recv) {
				continue
			}

			annotations, annotated, err := parseDirectives(fn)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fn.Name.Name, err)
			}

			if annotated {
				methods[fn.Name.Name] = annotations
			}
		}
	}

	return methods, nil
}

// ClientMethods returns the names of every exported *Client method in the directory, annotated or not.
func ClientMethods(dir string) ([]string, error) {
	files, err := parseDir(dir)
	if err != nil {
		return nil, err
	}

	var names []string

	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.IsExported() && isClientReceiver(fn.Recv) {
				names = append(names, fn.Name.Name)
			}
		}
	}

	sort.Strings(names)

	return names, nil
}

func parseDir(dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fileSet := token.NewFileSet()

	var files []*ast.File

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fileSet, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}

		files = append(files, file)
	}

	return files, nil
}

func isClientReceiver(recv *ast.FieldList) bool {
	if len(recv.List) != 1 {
		return false
	}

	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	ident, ok := star.X.(*ast.Ident)

	return ok && ident.Name == "Client"
}

func parseDirectives(fn *ast.FuncDecl) ([]Annotation, bool, error) {
	if fn.Doc == nil {
		return nil, false, nil
	}

	var (
		annotations []Annotation
		annotated   bool
	)

	for _, comment := range fn.Doc.List {
		text, found := strings.CutPrefix(comment.Text, directive)
		if !found {
			continue
		}

		annotated = true
		fields := strings.Fields(text)

		if len(fields) == 1 && fields[0] == "none" {
			continue
		}

		if len(fields) < 3 || len(fields) > 4 {
			return nil, false, fmt.Errorf("bad directive %q", comment.Text)
		}

		if fields[0] != "system" && fields[0] != "project" && fields[0] != "own" {
			return nil, false, fmt.Errorf("unknown scope in %q", comment.Text)
		}

		annotation := Annotation{Scope: fields[0], Resource: fields[1], Action: fields[2]}

		if len(fields) == 4 {
			switch fields[3] {
			case "user-credentials":
				annotation.UserCredentialsOnly = true
			case "content-deletion":
				annotation.ContentDeletionOnly = true
			default:
				return nil, false, fmt.Errorf("unknown qualifier in %q", comment.Text)
			}
		}

		annotations = append(annotations, annotation)
	}

	return annotations, annotated, nil
}

// CalledMethods returns the *Client method names the Go file calls, by selector name.
func CalledMethods(path string, clientMethods []string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}

	known := map[string]bool{}
	for _, name := range clientMethods {
		known[name] = true
	}

	found := map[string]bool{}

	ast.Inspect(file, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok && known[selector.Sel.Name] {
			found[selector.Sel.Name] = true
		}

		return true
	})

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}

	sort.Strings(names)

	return names, nil
}

// Build returns the table: for each kind, the union of the annotations of the client methods its files call. Every
// called method must be annotated.
func Build(harborDir, controllerDir string) ([]Entry, error) {
	clientMethods, err := ClientMethods(harborDir)
	if err != nil {
		return nil, err
	}

	methods, err := ParseMethods(harborDir)
	if err != nil {
		return nil, err
	}

	var table []Entry

	for _, kind := range KindFiles {
		seen := map[Entry]bool{}

		for _, file := range kind.Files {
			called, err := CalledMethods(filepath.Join(controllerDir, file), clientMethods)
			if err != nil {
				return nil, err
			}

			for _, name := range called {
				annotations, ok := methods[name]
				if !ok {
					return nil, fmt.Errorf("%s calls Client.%s, which has no //harbor:permission annotation", file, name)
				}

				for _, annotation := range annotations {
					scope := annotation.Scope
					if scope == "own" {
						scope = kind.OwnScope
					}

					entry := Entry{Kind: kind.Kind, Scope: scope, Resource: annotation.Resource, Action: annotation.Action, UserCredentialsOnly: annotation.UserCredentialsOnly, ContentDeletionOnly: annotation.ContentDeletionOnly}
					if !seen[entry] {
						seen[entry] = true

						table = append(table, entry)
					}
				}
			}
		}
	}

	sortTable(table)

	return table, nil
}

func sortTable(table []Entry) {
	order := map[string]int{}
	for index, kind := range KindFiles {
		order[kind.Kind] = index
	}

	sort.SliceStable(table, func(i, j int) bool {
		a, b := table[i], table[j]

		switch {
		case a.Kind != b.Kind:
			return order[a.Kind] < order[b.Kind]
		case a.Scope != b.Scope:
			return a.Scope > b.Scope // system before project
		case a.Resource != b.Resource:
			return a.Resource < b.Resource
		default:
			return a.Action < b.Action
		}
	})
}
