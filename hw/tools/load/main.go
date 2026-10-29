// load: .s / .c をビルドして、Verilog シミュレーション用のファイルを作る。
//
//	go run ./tools/load -o build/fib ../asm/examples/fib.s
//	  → build/fib.elf   ELF (emu でもそのまま動く)
//	    build/fib.hex   $readmemh 用の RAM イメージ (プログラム + 初期スタック)
//	    build/fib.args  テストベンチに渡す plusargs (+entry= +sp= +brk=)
//
// -wave-sym 名前 を付けると、そのシンボルのアドレスを +wave_pc= として .args に足す
// (make wave で「その関数に最初に来たところ」の前後だけ VCD に記録するため)。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"rvhw/loader"
)

func main() {
	out := flag.String("o", "build/a", "出力ファイル名 (拡張子なし)")
	argv := flag.String("argv", "", "プログラムに渡す引数 (カンマ区切り, argv[0] は自動)")
	waveSym := flag.String("wave-sym", "", "VCD の記録を始めるシンボル (例: fib_iter)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: load [-o build/name] [-argv a,b] [-wave-sym sym] file.s|file.c ...")
		os.Exit(2)
	}
	elfData, err := loader.BuildFiles(flag.Args())
	check(err)
	args := []string{"prog"}
	if *argv != "" {
		args = append(args, strings.Split(*argv, ",")...)
	}
	im, err := loader.Load(elfData, args)
	check(err)
	check(os.WriteFile(*out+".elf", elfData, 0o755))
	check(im.WriteHex(*out + ".hex"))
	plus := im.PlusArgs()
	if *waveSym != "" {
		addr, ok := loader.Symbols(elfData)[*waveSym]
		if !ok {
			check(fmt.Errorf("symbol %q not found", *waveSym))
		}
		plus = append(plus, fmt.Sprintf("+wave_pc=%x", addr))
	}
	check(os.WriteFile(*out+".args", []byte(strings.Join(plus, " ")+"\n"), 0o644))
	fmt.Fprintf(os.Stderr, "%s: entry=0x%x sp=0x%x brk=0x%x\n", *out, im.Entry, im.SP, im.BrkStart)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(1)
	}
}
