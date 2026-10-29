// rvasm — 学習用の RISC-V (RV64IM) アセンブラ + 静的リンカ。
//
//	rvasm as  a.s -o a.o            アセンブル (ELF リロケータブル .o を出力)
//	rvasm ld  a.o b.o -o prog       リンク (静的 ELF 実行ファイルを出力)
//	rvasm build a.s b.s -o prog     アセンブル + リンクを一度に
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	inputs, out := parseArgs(os.Args[2:])
	if len(inputs) == 0 {
		usage()
	}
	var err error
	switch cmd {
	case "as":
		if len(inputs) != 1 {
			die(fmt.Errorf("as: exactly one input file expected"))
		}
		if out == "" {
			out = strings.TrimSuffix(inputs[0], filepath.Ext(inputs[0])) + ".o"
		}
		err = cmdAs(inputs[0], out)
	case "ld":
		err = cmdLd(inputs, defaultOut(out))
	case "build":
		err = cmdBuild(inputs, defaultOut(out))
	default:
		usage()
	}
	if err != nil {
		die(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  rvasm as    a.s [-o a.o]
  rvasm ld    a.o b.o ... [-o a.out]
  rvasm build a.s b.s ... [-o a.out]`)
	os.Exit(2)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "rvasm:", err)
	os.Exit(1)
}

func parseArgs(args []string) (inputs []string, out string) {
	for k := 0; k < len(args); k++ {
		if args[k] == "-o" && k+1 < len(args) {
			out = args[k+1]
			k++
		} else {
			inputs = append(inputs, args[k])
		}
	}
	return
}

func defaultOut(out string) string {
	if out == "" {
		return "a.out"
	}
	return out
}

func assembleFile(path string) (*object, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return assemble(path, string(src))
}

func cmdAs(in, out string) error {
	o, err := assembleFile(in)
	if err != nil {
		return err
	}
	b, err := writeObject(o)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

func cmdLd(inputs []string, out string) error {
	var objs []*object
	for _, path := range inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		o, err := readObject(path, data)
		if err != nil {
			return err
		}
		objs = append(objs, o)
	}
	return linkAndWrite(objs, out)
}

// cmdBuild は .s をアセンブルし、いったん .o 形式に書き出して読み直してからリンクする。
// (メモリ上の object を直接渡すこともできるが、as → ld と同じ経路を通すため)
func cmdBuild(inputs []string, out string) error {
	var objs []*object
	for _, path := range inputs {
		o, err := assembleFile(path)
		if err != nil {
			return err
		}
		b, err := writeObject(o)
		if err != nil {
			return err
		}
		if o, err = readObject(path, b); err != nil {
			return err
		}
		objs = append(objs, o)
	}
	return linkAndWrite(objs, out)
}

func linkAndWrite(objs []*object, out string) error {
	b, err := link(objs)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o755)
}
