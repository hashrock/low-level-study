package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// REPL: 標準入力から ; で終わる SQL を読み、実行して表形式で表示する。
// 使い方: go run . [DBファイル]  (省略時 minidb.db)

func main() {
	path := "minidb.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	db, err := Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	// 端末から使うときだけプロンプトを出す (パイプ入力では出さない)
	st, _ := os.Stdin.Stat()
	interactive := st != nil && st.Mode()&os.ModeCharDevice != 0
	if interactive {
		fmt.Printf("minidb (%s). SQL を ; で終えて入力。.tables .schema .exit\n", path)
	}

	in := bufio.NewScanner(os.Stdin)
	var buf strings.Builder
	prompt := func() {
		if !interactive {
			return
		}
		if buf.Len() == 0 {
			fmt.Print("minidb> ")
		} else {
			fmt.Print("   ...> ")
		}
	}
	prompt()
	for in.Scan() {
		line := in.Text()
		if trimmed := strings.TrimSpace(line); buf.Len() == 0 && strings.HasPrefix(trimmed, ".") {
			if trimmed == ".exit" || trimmed == ".quit" {
				return
			}
			metaCommand(db, trimmed)
			prompt()
			continue
		}
		buf.WriteString(line + "\n")
		if strings.HasSuffix(strings.TrimSpace(buf.String()), ";") {
			results, err := db.Exec(buf.String())
			for _, r := range results {
				printResult(r)
			}
			if err != nil {
				fmt.Println("Error:", err)
			}
			buf.Reset()
		}
		prompt()
	}
}

func metaCommand(db *DB, cmd string) {
	switch cmd {
	case ".tables":
		fmt.Println(strings.Join(db.order, " "))
	case ".schema":
		for _, name := range db.order {
			t := db.tables[name]
			var cols []string
			for _, c := range t.Columns {
				s := c.Name + " " + c.Type.String()
				if c.PrimaryKey {
					s += " PRIMARY KEY"
				}
				cols = append(cols, s)
			}
			fmt.Printf("CREATE TABLE %s (%s); -- root page %d\n", t.Name, strings.Join(cols, ", "), t.Root)
		}
	default:
		fmt.Println("unknown command:", cmd)
	}
}

func printResult(r *Result) {
	if r.Columns == nil {
		fmt.Println(r.Message)
		return
	}
	widths := make([]int, len(r.Columns))
	for i, c := range r.Columns {
		widths[i] = displayWidth(c)
	}
	for _, row := range r.Rows {
		for i, v := range row {
			widths[i] = max(widths[i], displayWidth(v.String()))
		}
	}
	sep := "+"
	for _, w := range widths {
		sep += strings.Repeat("-", w+2) + "+"
	}
	line := func(cells []string) {
		s := "|"
		for i, c := range cells {
			s += " " + c + strings.Repeat(" ", widths[i]-displayWidth(c)) + " |"
		}
		fmt.Println(s)
	}
	fmt.Println(sep)
	line(r.Columns)
	fmt.Println(sep)
	for _, row := range r.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = v.String()
		}
		line(cells)
	}
	fmt.Println(sep)
	fmt.Println(r.Message)
}

// displayWidth は端末上の表示幅のおおよその値 (全角文字を 2 とみなす)。
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x1100 {
			w += 2
		} else {
			w++
		}
	}
	return w
}
