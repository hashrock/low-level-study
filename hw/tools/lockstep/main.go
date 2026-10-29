// lockstep: 1 つのプログラムを Verilog (Icarus) と emu で 1 命令ずつ突き合わせて実行する。
//
//	go run ./tools/lockstep ../cc/examples/fib.c
//	go run ./tools/lockstep -wait 2 -pipe tests/loadstore.s
//
// 食い違ったら、最初に違った命令と直前のコミットを表示して終了コード 1 で終わる。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rvhw/sim"
)

func main() {
	wait := flag.Int("wait", 0, "RAM の待ちサイクル数")
	pipe := flag.Bool("pipe", false, "5 段パイプライン版のコアを使う")
	argv := flag.String("argv", "", "プログラムに渡す引数 (カンマ区切り)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: lockstep [-wait N] [-pipe] [-argv a,b] file.s|file.c ...")
		os.Exit(2)
	}
	dir, err := os.MkdirTemp("", "rvhw")
	check(err)
	defer os.RemoveAll(dir)
	top := "single"
	if *pipe {
		top = "pipe"
	}
	vvp := filepath.Join(dir, "sim.vvp")
	check(sim.Compile(top, vvp))
	args := []string{"prog"}
	if *argv != "" {
		args = append(args, strings.Split(*argv, ",")...)
	}
	r, err := sim.Lockstep(sim.Options{VVP: vvp, Files: flag.Args(), Argv: args, Wait: *wait, WorkDir: dir})
	if r != nil {
		fmt.Print(r.Stdout)
	}
	check(err)
	fmt.Fprintf(os.Stderr, "lockstep OK: %d instructions match emu (cycles=%d, CPI=%.3f, exit=%d)\n",
		r.Instret, r.Cycles, float64(r.Cycles)/float64(r.Instret), r.ExitCode)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
