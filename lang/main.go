package main

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"minilang/minilang"
)

// runSource はソースを 字句解析 → 構文解析 → コンパイル → VM 実行 する。
func runSource(src string, out io.Writer, dis bool) error {
	prog, err := minilang.Parse(src)
	if err != nil {
		return err
	}
	proto, err := minilang.NewCompiler(false).Compile(prog)
	if err != nil {
		return err
	}
	if dis {
		minilang.Disassemble(proto, out)
		return nil
	}
	_, err = minilang.NewVM(out).Run(proto)
	return err
}

func main() {
	args := os.Args[1:]
	dis := len(args) > 0 && args[0] == "-dis"
	if dis {
		args = args[1:]
	}
	if len(args) == 0 {
		repl()
		return
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := runSource(string(src), os.Stdout, dis); err != nil {
		fmt.Fprintf(os.Stderr, "%s:%s\n", args[0], err)
		os.Exit(1)
	}
}

func repl() {
	c := minilang.NewCompiler(true)
	vm := minilang.NewVM(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	fmt.Println("minilang REPL (Ctrl-D で終了)")
	buf := ""
	for {
		if buf == "" {
			fmt.Print("> ")
		} else {
			fmt.Print("... ")
		}
		if !sc.Scan() {
			fmt.Println()
			return
		}
		buf += sc.Text() + "\n"
		prog, err := minilang.Parse(buf)
		if le, ok := err.(*minilang.LangError); ok && le.AtEOF {
			continue // 入力が途中なので次の行を読む
		}
		buf = ""
		if err == nil {
			var proto *minilang.Proto
			if proto, err = c.Compile(prog); err == nil {
				var v minilang.Value
				if v, err = vm.Run(proto); err == nil && v != nil {
					fmt.Println(minilang.Format(v, true))
				}
			}
		}
		if err != nil {
			fmt.Println(err)
		}
	}
}
