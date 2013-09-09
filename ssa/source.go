// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

// This file defines utilities for working with source positions
// or source-level named entities ("objects").

// TODO(adonovan): test that {Value,Instruction}.Pos() positions match
// the originating syntax, as specified.

import (
	"go/ast"
	"go/token"

	"code.google.com/p/go.tools/go/types"
)

// EnclosingFunction returns the function that contains the syntax
// node denoted by path.
//
// Syntax associated with package-level variable specifications is
// enclosed by the package's init() function.
//
// Returns nil if not found; reasons might include:
//    - the node is not enclosed by any function.
//    - the node is within an anonymous function (FuncLit) and
//      its SSA function has not been created yet (pkg.BuildPackage()
//      has not yet been called).
//
func EnclosingFunction(pkg *Package, path []ast.Node) *Function {
	// Start with package-level function...
	fn := findEnclosingPackageLevelFunction(pkg, path)
	if fn == nil {
		return nil // not in any function
	}

	// ...then walk down the nested anonymous functions.
	n := len(path)
outer:
	for i := range path {
		if lit, ok := path[n-1-i].(*ast.FuncLit); ok {
			for _, anon := range fn.AnonFuncs {
				if anon.Pos() == lit.Type.Func {
					fn = anon
					continue outer
				}
			}
			// SSA function not found:
			// - package not yet built, or maybe
			// - builder skipped FuncLit in dead block
			//   (in principle; but currently the Builder
			//   generates even dead FuncLits).
			return nil
		}
	}
	return fn
}

// HasEnclosingFunction returns true if the AST node denoted by path
// is contained within the declaration of some function or
// package-level variable.
//
// Unlike EnclosingFunction, the behaviour of this function does not
// depend on whether SSA code for pkg has been built, so it can be
// used to quickly reject check inputs that will cause
// EnclosingFunction to fail, prior to SSA building.
//
func HasEnclosingFunction(pkg *Package, path []ast.Node) bool {
	return findEnclosingPackageLevelFunction(pkg, path) != nil
}

// findEnclosingPackageLevelFunction returns the Function
// corresponding to the package-level function enclosing path.
//
func findEnclosingPackageLevelFunction(pkg *Package, path []ast.Node) *Function {
	if n := len(path); n >= 2 { // [... {Gen,Func}Decl File]
		switch decl := path[n-2].(type) {
		case *ast.GenDecl:
			if decl.Tok == token.VAR && n >= 3 {
				// Package-level 'var' initializer.
				return pkg.init
			}

		case *ast.FuncDecl:
			if decl.Recv == nil && decl.Name.Name == "init" {
				// Explicit init() function.
				return pkg.init
			}
			// Declared function/method.
			return findNamedFunc(pkg, decl.Name.NamePos)
		}
	}
	return nil // not in any function
}

// findNamedFunc returns the named function whose FuncDecl.Ident is at
// position pos.
//
func findNamedFunc(pkg *Package, pos token.Pos) *Function {
	// Look at all package members and method sets of named types.
	// Not very efficient.
	for _, mem := range pkg.Members {
		switch mem := mem.(type) {
		case *Function:
			if mem.Pos() == pos {
				return mem
			}
		case *Type:
			mset := types.NewPointer(mem.Type()).MethodSet()
			for i, n := 0, mset.Len(); i < n; i++ {
				// Don't call Program.Method: avoid creating wrappers.
				obj := mset.At(i).Obj().(*types.Func)
				if obj.Pos() == pos {
					return pkg.values[obj].(*Function)
				}
			}
		}
	}
	return nil
}

// ValueForExpr returns the SSA Value that corresponds to non-constant
// expression e.
//
// It returns nil if no value was found, e.g.
//    - the expression is not lexically contained within f;
//    - f was not built with debug information; or
//    - e is a constant expression.  (For efficiency, no debug
//      information is stored for constants. Use
//      importer.PackageInfo.ValueOf(e) instead.)
//    - the value was optimised away.
//
// The types of e and the result are equal (modulo "untyped" bools
// resulting from comparisons) and they have equal "pointerness".
//
// (Tip: to find the ssa.Value given a source position, use
// importer.PathEnclosingInterval to locate the ast.Node, then
// EnclosingFunction to locate the Function, then ValueForExpr to find
// the ssa.Value.)
//
func (f *Function) ValueForExpr(e ast.Expr) Value {
	if f.debugInfo() { // (opt)
		e = unparen(e)
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				if ref, ok := instr.(*DebugRef); ok {
					if ref.Expr == e {
						return ref.X
					}
				}
			}
		}
	}
	return nil
}

// --- Lookup functions for source-level named entities (types.Objects) ---

// Package returns the SSA Package corresponding to the specified
// type-checker package object.
// It returns nil if no such SSA package has been created.
//
func (prog *Program) Package(obj *types.Package) *Package {
	return prog.packages[obj]
}

// packageLevelValue returns the package-level value corresponding to
// the specified named object, which may be a package-level const
// (*Const), var (*Global) or func (*Function) of some package in
// prog.  It returns nil if the object is not found.
//
func (prog *Program) packageLevelValue(obj types.Object) Value {
	if pkg, ok := prog.packages[obj.Pkg()]; ok {
		return pkg.values[obj]
	}
	return nil
}

// FuncValue returns the SSA Value denoted by the source-level named
// function obj.  The result may be a *Function or a *Builtin, or nil
// if not found.
//
func (prog *Program) FuncValue(obj *types.Func) Value {
	// Universal built-in?
	if v, ok := prog.builtins[obj]; ok {
		return v
	}
	// Package-level function or declared method?
	if v := prog.packageLevelValue(obj); v != nil {
		return v
	}
	// Interface method wrapper?
	meth := recvType(obj).MethodSet().Lookup(obj.Pkg(), obj.Name())
	return prog.Method(meth)
}

// ConstValue returns the SSA Value denoted by the source-level named
// constant obj.  The result may be a *Const, or nil if not found.
//
func (prog *Program) ConstValue(obj *types.Const) *Const {
	// TODO(adonovan): opt: share (don't reallocate)
	// Consts for const objects and constant ast.Exprs.

	// Universal constant? {true,false,nil}
	if obj.Parent() == types.Universe {
		return NewConst(obj.Val(), obj.Type())
	}
	// Package-level named constant?
	if v := prog.packageLevelValue(obj); v != nil {
		return v.(*Const)
	}
	return NewConst(obj.Val(), obj.Type())
}

// VarValue returns the SSA Value that corresponds to a specific
// identifier denoting the source-level named variable obj.
//
// VarValue returns nil if a local variable was not found, perhaps
// because its package was not built, the debug information was not
// requested during SSA construction, or the value was optimized away.
//
// ref is the path to an ast.Ident (e.g. from PathEnclosingInterval),
// and that ident must resolve to obj.
//
// pkg is the package enclosing the reference.  (A reference to a var
// may result in code, so we need to know where to find that code.)
//
// The Value of a defining (as opposed to referring) identifier is the
// value assigned to it in its definition.
//
// In many cases where the identifier appears in an lvalue context,
// the resulting Value is the var's address, not its value.
// For example, x in all these examples:
//    x.y = 0
//    x[0] = 0
//    _ = x[:]
//    x = X{}
//    _ = &x
//    x.method()    (iff method is on &x)
// and all package-level vars.  (This situation can be detected by
// comparing the types of the Var and Value.)
//
func (prog *Program) VarValue(obj *types.Var, pkg *Package, ref []ast.Node) Value {
	id := ref[0].(*ast.Ident)

	// Package-level variable?
	if v := prog.packageLevelValue(obj); v != nil {
		return v.(*Global)
	}

	// Must be a function-local variable.
	// (e.g. local, parameter, or field selection e.f)
	fn := EnclosingFunction(pkg, ref)
	if fn == nil {
		return nil // e.g. SSA not built
	}

	// Defining ident of a parameter?
	if id.Pos() == obj.Pos() {
		for _, param := range fn.Params {
			if param.Object() == obj {
				return param
			}
		}
	}

	// Other ident?
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if ref, ok := instr.(*DebugRef); ok {
				if ref.Pos() == id.Pos() {
					return ref.X
				}
			}
		}
	}

	return nil // e.g. debug info not requested, or var optimized away
}