package minirt

// module.go — a tiny CommonJS-style require() for local files.
//
// require("./lib.js") resolves relative to the *calling* module's directory,
// is gated by --allow-read like any other file access, and is cached by
// canonical path (so a symlink and its target are the same module).
// No node_modules lookup, no JSON, no ES modules — deliberately minimal.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
)

const moduleWrapperHead = "(function (exports, require, module, __filename, __dirname) {"
const moduleWrapperTail = "\n})"

func (r *Runtime) installRequire() {
	cwd, _ := r.fs.Getwd()
	_ = r.vm.Set("require", r.makeRequire(cwd))
}

func (r *Runtime) makeRequire(dir string) func(string) goja.Value {
	return func(spec string) goja.Value {
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") && !filepath.IsAbs(spec) {
			panic(r.vm.NewTypeError(fmt.Sprintf("require(%q): only relative or absolute file paths are supported", spec)))
		}
		p := spec
		if !filepath.IsAbs(p) {
			p = dir + string(filepath.Separator) + p
		}
		if filepath.Ext(p) == "" {
			p += ".js"
		}
		real, err := r.checkRead(p)
		if err != nil {
			r.throw(err)
		}
		if m, ok := r.modules[real]; ok {
			return m.Get("exports")
		}
		src, err := r.fs.ReadFile(real, MaxReadBytes)
		if err != nil {
			r.throw(err)
		}
		prog, err := goja.Compile(real, moduleWrapperHead+src+moduleWrapperTail, false)
		if err != nil {
			panic(r.vm.NewGoError(err))
		}
		fnVal, err := r.vm.RunProgram(prog)
		if err != nil {
			panic(err) // re-raise the JS exception as-is
		}
		fn, _ := goja.AssertFunction(fnVal)
		module := r.vm.NewObject()
		exports := r.vm.NewObject()
		_ = module.Set("exports", exports)
		r.modules[real] = module // cache before running: allows require cycles
		d := filepath.Dir(real)
		if _, err := fn(goja.Undefined(), exports, r.vm.ToValue(r.makeRequire(d)),
			module, r.vm.ToValue(real), r.vm.ToValue(d)); err != nil {
			delete(r.modules, real)
			panic(err)
		}
		return module.Get("exports")
	}
}
