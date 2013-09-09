// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file tests types.Check by using it to
// typecheck the standard library and tests.

package types

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/ioutil"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var verbose = flag.Bool("types.v", false, "verbose mode")

var (
	pkgCount int // number of packages processed
	start    = time.Now()
)

func TestStdlib(t *testing.T) {
	walkDirs(t, filepath.Join(runtime.GOROOT(), "src/pkg"))
	if *verbose {
		fmt.Println(pkgCount, "packages typechecked in", time.Since(start))
	}
}

func testTestDir(t *testing.T, path string, ignore ...string) {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}

	excluded := make(map[string]bool)
	for _, filename := range ignore {
		excluded[filename] = true
	}

	fset := token.NewFileSet()
	for _, f := range files {
		// filter directory contents
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || excluded[f.Name()] {
			continue
		}

		// parse file
		filename := filepath.Join(path, f.Name())
		// TODO(gri) The parser loses comments when bailing out early,
		//           and then we don't see the errorcheck command for
		//           some files. Use parser.AllErrors for now. Fix this.
		file, err := parser.ParseFile(fset, filename, nil, parser.ParseComments|parser.AllErrors)

		// check per-file instructions
		// For now we only check some cases.
		expectErrors := false
		if len(file.Comments) > 0 {
			if group := file.Comments[0]; len(group.List) > 0 {
				cmd := strings.TrimSpace(group.List[0].Text[2:]) // 2: ignore // or /* of comment
				switch cmd {
				case "skip", "compiledir":
					continue
				case "errorcheck":
					expectErrors = true
				}
			}
		}

		// type-check file if it parsed cleanly
		if err == nil {
			_, err = Check(filename, fset, []*ast.File{file})
		}

		if expectErrors {
			if err == nil {
				t.Errorf("expected errors but found none in %s", filename)
			}
		} else {
			if err != nil {
				t.Error(err)
			}
		}
	}
}

func TestStdtest(t *testing.T) {
	testTestDir(t, filepath.Join(runtime.GOROOT(), "test"),
		"cmplxdivide.go",       // also needs file cmplxdivide1.go - ignore
		"goto.go", "label1.go", // TODO(gri) implement missing label checks
		"mapnan.go", "sigchld.go", // don't work on Windows; testTestDir should consult build tags
		"sizeof.go", "switch.go", // TODO(gri) tone down duplicate checking in expr switches
		"typeswitch2.go", // TODO(gri) implement duplicate checking in type switches
	)
}

func TestStdfixed(t *testing.T) {
	testTestDir(t, filepath.Join(runtime.GOROOT(), "test", "fixedbugs"),
		"bug050.go", "bug088.go", "bug106.go", // TODO(gri) parser loses comments when bailing out early
		"bug222.go", "bug282.go", "bug306.go", // TODO(gri) parser loses comments when bailing out early
		"issue4776.go",                        // TODO(gri) parser loses comments when bailing out early
		"bug136.go", "bug179.go", "bug344.go", // TODO(gri) implement missing label checks
		"bug251.go",                           // TODO(gri) incorrect cycle checks for interface types
		"bug165.go",                           // TODO(gri) isComparable not working for incomplete struct type
		"bug176.go",                           // TODO(gri) composite literal array index must be non-negative constant
		"bug200.go",                           // TODO(gri) complete duplicate checking in expr switches
		"bug223.go", "bug413.go", "bug459.go", // TODO(gri) complete initialization checks
		"bug248.go", "bug302.go", "bug369.go", // complex test instructions - ignore
		"bug250.go",    // TODO(gri) fix recursive interfaces
		"bug326.go",    // TODO(gri) assignment doesn't guard against len(rhs) == 0
		"bug373.go",    // TODO(gri) implement use checks
		"bug376.go",    // TODO(gri) built-ins must be called (no built-in function expressions)
		"issue3924.go", // TODO(gri) && and || produce bool result (not untyped bool)
		"issue4847.go", // TODO(gri) initialization cycle error not found
	)
}

func TestStdken(t *testing.T) {
	testTestDir(t, filepath.Join(runtime.GOROOT(), "test", "ken"))
}

// Package paths of excluded packages.
var excluded = map[string]bool{
	"builtin": true,
}

// typecheck typechecks the given package files.
func typecheck(t *testing.T, path string, filenames []string) {
	fset := token.NewFileSet()

	// parse package files
	var files []*ast.File
	for _, filename := range filenames {
		file, err := parser.ParseFile(fset, filename, nil, parser.AllErrors)
		if err != nil {
			// the parser error may be a list of individual errors; report them all
			if list, ok := err.(scanner.ErrorList); ok {
				for _, err := range list {
					t.Error(err)
				}
				return
			}
			t.Error(err)
			return
		}

		if *verbose {
			if len(files) == 0 {
				fmt.Println("package", file.Name.Name)
			}
			fmt.Println("\t", filename)
		}

		files = append(files, file)
	}

	// typecheck package files
	var conf Config
	conf.Error = func(err error) { t.Error(err) }
	conf.Check(path, fset, files, nil)
	pkgCount++
}

// pkgfiles returns the list of package files for the given directory.
func pkgfiles(t *testing.T, dir string) []string {
	ctxt := build.Default
	ctxt.CgoEnabled = false
	pkg, err := ctxt.ImportDir(dir, 0)
	if err != nil {
		if _, nogo := err.(*build.NoGoError); !nogo {
			t.Error(err)
		}
		return nil
	}
	if excluded[pkg.ImportPath] {
		return nil
	}
	var filenames []string
	for _, name := range pkg.GoFiles {
		filenames = append(filenames, filepath.Join(pkg.Dir, name))
	}
	for _, name := range pkg.TestGoFiles {
		filenames = append(filenames, filepath.Join(pkg.Dir, name))
	}
	return filenames
}

// Note: Could use filepath.Walk instead of walkDirs but that wouldn't
//       necessarily be shorter or clearer after adding the code to
//       terminate early for -short tests.

func walkDirs(t *testing.T, dir string) {
	// limit run time for short tests
	if testing.Short() && time.Since(start) >= 750*time.Millisecond {
		return
	}

	fis, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Error(err)
		return
	}

	// typecheck package in directory
	if files := pkgfiles(t, dir); files != nil {
		typecheck(t, dir, files)
	}

	// traverse subdirectories, but don't walk into testdata
	for _, fi := range fis {
		if fi.IsDir() && fi.Name() != "testdata" {
			walkDirs(t, filepath.Join(dir, fi.Name()))
		}
	}
}