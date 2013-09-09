// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package types declares the data types and implements
// the algorithms for type-checking of Go packages.
// Use Check and Config.Check to invoke the type-checker.
//
// Type-checking consists of several interdependent phases:
//
// Name resolution maps each identifier (ast.Ident) in the program to the
// language object (Object) it denotes.
// Use Info.Objects, Info.Implicits for the results of name resolution.
//
// Constant folding computes the exact constant value (exact.Value) for
// every expression (ast.Expr) that is a compile-time constant.
// Use Info.Values for the results of constant folding.
//
// Type inference computes the type (Type) of every expression (ast.Expr)
// and checks for compliance with the language specification.
// Use Info.Types for the results of type evaluation.
//
package types

import (
	"go/ast"
	"go/token"

	"code.google.com/p/go.tools/go/exact"
)

// Check type-checks a package and returns the resulting complete package
// object, or a nil package and the first error. The package is specified
// by a list of *ast.Files and corresponding file set, and the import path
// the package is identified with. The clean path must not be empty or dot (".").
//
// For more control over type-checking and results, use Config.Check.
func Check(path string, fset *token.FileSet, files []*ast.File) (*Package, error) {
	var conf Config
	pkg, err := conf.Check(path, fset, files, nil)
	if err != nil {
		return nil, err
	}
	return pkg, nil
}

// A Config specifies the configuration for type checking.
// The zero value for Config is a ready-to-use default configuration.
type Config struct {
	// If IgnoreFuncBodies is set, function bodies are not
	// type-checked.
	IgnoreFuncBodies bool

	// If FakeImportC is set, `import "C"` (for packages requiring Cgo)
	// and expressions with qualified identifiers referring to package
	// C are silently ignored.
	// Caution: Effects may be unpredictable - do not use casually!
	FakeImportC bool

	// If Error != nil, it is called with each error found
	// during type checking. The error strings of errors with
	// detailed position information are formatted as follows:
	// filename:line:column: message
	Error func(err error)

	// If Import != nil, it is called for each imported package.
	// Otherwise, GcImporter is called.
	// An importer resolves import paths to Package objects.
	// The imports map records the packages already imported,
	// indexed by package id (canonical import path).
	// An importer must determine the canonical import path and
	// check the map to see if it is already present in the imports map.
	// If so, the Importer can return the map entry.  Otherwise, the
	// importer should load the package data for the given path into
	// a new *Package, record pkg in the imports map, and then
	// return pkg.
	Import func(imports map[string]*Package, path string) (pkg *Package, err error)

	// If Alignof != nil, it is called to determine the alignment
	// of the given type. Otherwise DefaultAlignmentof is called.
	// Alignof must implement the alignment guarantees required by
	// the spec.
	Alignof func(Type) int64

	// If Offsetsof != nil, it is called to determine the offsets
	// of the given struct fields, in bytes. Otherwise DefaultOffsetsof
	// is called. Offsetsof must implement the offset guarantees
	// required by the spec.
	Offsetsof func(fields []*Var) []int64

	// If Sizeof != nil, it is called to determine the size of the
	// given type. Otherwise, DefaultSizeof is called. Sizeof must
	// implement the size guarantees required by the spec.
	Sizeof func(Type) int64
}

// Info holds result type information for a type-checked package.
// Only the information for which a map is provided is collected.
// If the package has type errors, the collected information may
// be incomplete.
type Info struct {
	// Types maps expressions to their types. Identifiers on the
	// lhs of declarations are collected in Objects, not Types.
	Types map[ast.Expr]Type

	// Values maps constant expressions to their values.
	Values map[ast.Expr]exact.Value

	// Objects maps identifiers to their corresponding objects (including
	// package names, dots "." of dot-imports, and blank "_" identifiers).
	// For identifiers that do not denote objects (e.g., blank identifiers
	// on the lhs of assignments, or symbolic variables t in t := x.(type)
	// of type switch headers), the corresponding objects are nil.
	// BUG(gri) Label identifiers in break, continue, or goto statements
	// are not yet mapped.
	Objects map[*ast.Ident]Object

	// Implicits maps nodes to their implicitly declared objects, if any.
	// The following node and object types may appear:
	//
	//	node               declared object
	//
	//	*ast.ImportSpec    *Package (imports w/o renames), or imported objects (dot-imports)
	//	*ast.CaseClause    type-specific *Var for each type switch case clause (incl. default)
	//      *ast.Field         anonymous struct field or parameter *Var
	//
	Implicits map[ast.Node]Object

	// Selections maps selector expressions to their corresponding selections.
	Selections map[*ast.SelectorExpr]*Selection

	// Scopes maps ast.Nodes to the scopes they define. Note that package scopes
	// are not associated with a specific node but with all files belonging to a
	// package. Thus, the package scope can be found in the type-checked package
	// object.
	//
	// The following node types may appear in Scopes:
	//
	//	*ast.File
	//	*ast.FuncType
	//	*ast.BlockStmt
	//	*ast.IfStmt
	//	*ast.SwitchStmt
	//	*ast.TypeSwitchStmt
	//	*ast.CaseClause
	//	*ast.CommClause
	//	*ast.ForStmt
	//	*ast.RangeStmt
	//
	Scopes map[ast.Node]*Scope
}

// Check type-checks a package and returns the resulting package object,
// the first error if any, and if info != nil, additional type information.
// The package is marked as complete if no errors occurred, otherwise it is
// incomplete.
//
// The package is specified by a list of *ast.Files and corresponding file
// set, and the import path the package is identified with. The clean path
// must not be empty or dot (".").
func (conf *Config) Check(path string, fset *token.FileSet, files []*ast.File, info *Info) (*Package, error) {
	pkg, err := conf.check(path, fset, files, info)
	if err == nil {
		pkg.complete = true
	}
	return pkg, err
}

// IsAssignableTo reports whether a value of type V
// is assignable to a variable of type T.
func IsAssignableTo(V, T Type) bool {
	x := operand{mode: value, typ: V}
	return x.isAssignableTo(nil, T) // config not needed for non-constant x
}

// BUG(gri): Some built-ins don't check parameters fully, yet (e.g. append).
// BUG(gri): Use of labels is only partially checked.
// BUG(gri): Unused variables and imports are not reported.
// BUG(gri): Interface vs non-interface comparisons are not correctly implemented.
// BUG(gri): Switch statements don't check duplicate cases for all types for which it is required.
// BUG(gri): Some built-ins may not be callable if in statement-context.