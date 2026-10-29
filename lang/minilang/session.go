package minilang

import (
	"bytes"
	"strings"
)

// Session はブラウザ版デバッガ 1 回分の状態 (ソース・コンパイル結果・VM・出力)。
// JS 側の API はこれを薄く包んだだけなので、ロジックは通常の go test で確かめられる。
type Session struct {
	Src    string
	Root   *Proto
	Protos []*Proto
	VM     *VM
	out    bytes.Buffer
	sent   int
}

// DisProto は 1 関数分の逆アセンブル結果。
type DisProto struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Params     int        `json:"params"`
	Locals     int        `json:"locals"`
	LocalNames []string   `json:"localNames"`
	FreeNames  []string   `json:"freeNames"`
	Code       []DisInstr `json:"code"`
}

type DisInstr struct {
	Line int `json:"line"`
	Col  int `json:"col"`
	Instr
}

// Compile はソースをコンパイルして Session を作る。構文・コンパイルエラーなら error。
func Compile(src string) (*Session, error) {
	prog, err := Parse(src)
	if err != nil {
		return nil, err
	}
	root, err := NewCompiler(false).Compile(prog)
	if err != nil {
		return nil, err
	}
	s := &Session{Src: src, Root: root, Protos: Protos(root)}
	s.Reset()
	return s, nil
}

// Reset は VM を作り直して最初から実行できる状態にする。
func (s *Session) Reset() {
	s.out.Reset()
	s.sent = 0
	s.VM = NewVM(&s.out)
	s.VM.Start(s.Root)
}

// Output は前回呼んでから増えた print の出力を返す。
func (s *Session) Output() string {
	b := s.out.Bytes()
	d := string(b[s.sent:])
	s.sent = len(b)
	return d
}

// AllOutput はこれまでの出力全部。
func (s *Session) AllOutput() string { return s.out.String() }

// Disassembly は全関数の逆アセンブル結果。
func (s *Session) Disassembly() []DisProto {
	out := make([]DisProto, len(s.Protos))
	for i, p := range s.Protos {
		d := DisProto{ID: i, Name: protoName(p), Params: p.NumParams, Locals: p.NumLocals, Code: []DisInstr{},
			LocalNames: trimAll(p.LocalNames), FreeNames: trimAll(p.FreeNames)}
		for ip := 0; ip < len(p.Code); {
			in := DecodeInstr(p, ip)
			d.Code = append(d.Code, DisInstr{Line: p.Pos[ip].Line, Col: p.Pos[ip].Col, Instr: in})
			ip = in.Next
		}
		out[i] = d
	}
	return out
}

func trimAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

// StepResult は Step 系の結果。
type StepResult struct {
	Status string  `json:"status"` // paused (止まった) / budget (予算切れ・続きあり) / done / error
	Steps  int     `json:"steps"`
	Out    string  `json:"out"` // 新しく増えた出力
	Error  *SError `json:"error,omitempty"`
}

// Step は mode に応じて実行を進める。
//
//	"instr": 1 命令 / "line": 1 行 (呼び出しの中には入らない) / "out": 関数から戻るまで / "run": 最後まで
//
// どれも最大 budget 命令で一旦戻る (Status "budget")。
func (s *Session) Step(mode string, budget int) StepResult {
	vm := s.VM
	stopped := true
	var err error
	if budget <= 0 {
		budget = 1
	}
	switch mode {
	case "instr":
		err = vm.Step()
	case "line":
		stopped, err = vm.StepOver(budget)
	case "out":
		stopped, err = vm.StepOut(budget)
	default: // run
		err = vm.RunFor(budget)
		stopped = vm.Done()
	}
	r := StepResult{Status: "paused", Steps: vm.Steps(), Out: s.Output()}
	switch {
	case err != nil:
		r.Status, r.Error = "error", ErrorInfo(err)
	case vm.Done():
		r.Status = "done"
	case !stopped:
		r.Status = "budget"
	}
	return r
}
