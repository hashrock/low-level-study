package rv

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// マシンの組み立て。2 種類ある:
//
//  1. ユーザモードランナー (NewUserMachine): Linux の静的 ELF を U モードで実行し、
//     ecall はホスト側 (syscall.go) で処理する。カーネルは存在しない。
//  2. システムモード (NewVirtMachine): QEMU virt マシン相当。M モードで 0x80000000 から起動し、
//     ゲストのカーネルがトラップを処理する。
//
// どちらも *Machine を返す。実行・状態の取得・逆アセンブルなどは api.go を参照。

// Machine は CPU とバス・デバイスをまとめた 1 台の計算機。
type Machine struct {
	CPU   *CPU
	Bus   *Bus
	UART  *UART    // システムモードのみ (ユーザモードでは nil)
	CLINT *CLINT   // システムモードのみ
	Proc  *Process // ユーザモードのみ (Stdout/Stderr/Verbose などを後から変えてよい)

	Symbols Symbols // ELF のシンボル表 (無ければ空)
	Entry   uint64  // ELF のエントリアドレス

	// OnTrap はトラップ・復帰 (mret/sret)・システムコール (ユーザモード) のたびに呼ばれる。
	OnTrap func(ev TrapEvent)

	traps      []TrapEvent // 直近のトラップ (リングバッファ)
	trapHead   int
	trapCount  uint64 // これまでのトラップ総数
	stopOnTrap bool
	trapped    bool

	realtimeIdle bool
	idleBudget   uint64
	idle         bool
}

const trapLogSize = 256

// ---- ユーザモード ----

const userStackSize = 8 << 20

// NewUserMachine は Linux 用の静的 RV64 ELF をロードし、Linux と同じ形のスタック
// (argc/argv/envp/auxv) を作る。argv は argv[0] を含む引数列 (空なら {"a.out"})。
// stdin が nil なら read は 0 (EOF) を返す。stderr も stdout に書かれる (m.Proc.Stderr で変更可)。
//
//	メモリ配置 (RAM は 0 番地から):
//	  [ELF セグメント][brk ヒープ 32MiB →][mmap 領域 →]......[← スタック 8MiB]
func NewUserMachine(elfData []byte, argv []string, stdout io.Writer, stdin io.Reader) (*Machine, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	if len(argv) == 0 {
		argv = []string{"a.out"}
	}
	proc := &Process{Stdin: stdin, Stdout: stdout, Stderr: stdout}
	f, info, err := ParseELF(elfData)
	if err != nil {
		return nil, err
	}

	brkStart := pageUp(info.MaxAddr)
	ramSize := brkStart + 128<<20
	bus := NewBus(0, ramSize)
	if err := LoadELF(f, info, bus); err != nil {
		return nil, err
	}
	proc.brkStart, proc.brk = brkStart, brkStart
	proc.mmapNext = brkStart + 32<<20
	proc.mmapLimit = ramSize - userStackSize

	c := NewCPU(bus)
	c.Priv = PrivU
	c.UserOnly = true
	c.PC = info.Entry
	c.X[2] = setupStack(bus, ramSize, argv, info)

	m := &Machine{CPU: c, Bus: bus, Proc: proc, Symbols: readSymbols(f), Entry: info.Entry}
	c.OnTrap = m.recordTrap
	c.Syscall = func(c *CPU) {
		pc, a7 := c.PC, c.X[17]
		proc.Handle(c)
		m.recordTrap(&TrapEvent{Seq: c.Steps, Kind: "syscall", Cause: excEcallU, From: PrivU, To: PrivU,
			PC: pc, Target: pc + 4, A7: a7})
	}
	return m, nil
}

// setupStack はスタック先頭から下に向かって引数文字列と各種ポインタを積む。
//
//	sp -> argc
//	      argv[0] ... argv[n-1], NULL
//	      envp: NULL
//	      auxv: (AT_PHDR, x) (AT_PHENT, 56) (AT_PHNUM, n) (AT_PAGESZ, 4096) (AT_ENTRY, e)
//	            (AT_RANDOM, ptr) (AT_NULL, 0)
//	      ... 文字列の実体
func setupStack(bus *Bus, top uint64, args []string, info *LoadedELF) uint64 {
	sp := top
	push := func(data []byte) uint64 {
		sp -= uint64(len(data))
		bus.WriteBytes(sp, data)
		return sp
	}
	random := push([]byte("rvemu-random-16b")) // AT_RANDOM 用の 16 バイト
	var argv []uint64
	for _, a := range args {
		argv = append(argv, push(append([]byte(a), 0)))
	}

	words := []uint64{uint64(len(args))}
	words = append(words, argv...)
	words = append(words, 0) // argv 終端
	words = append(words, 0) // envp 終端 (環境変数なし)
	words = append(words, 3, info.Phdr, 4, 56, 5, info.Phnum, 6, pageSize, 9, info.Entry, 25, random, 0, 0)

	sp -= uint64(8 * len(words))
	sp &^= 15 // ABI: sp は 16 バイト境界
	for i, w := range words {
		bus.Store(sp+uint64(8*i), 8, w)
	}
	return sp
}

// ---- システムモード (QEMU virt 相当) ----

// メモリマップ
const (
	TestDevBase    = 0x0010_0000 // sifive_test (電源断)
	CLINTBase      = clintBase
	UARTBase       = uartBase
	RAMBase        = 0x8000_0000
	DefaultRAMSize = 128 << 20

	testDevBase = TestDevBase
	ramBase     = RAMBase
)

// VirtOptions はシステムモードのマシンの設定。ゼロ値で QEMU virt と同じ (RAM 128MiB)。
type VirtOptions struct {
	RAMSize uint64 // RAM の大きさ (0 なら 128MiB)。ブラウザでは小さくするとメモリを節約できる
}

// NewVirtMachine は QEMU `-machine virt -bios none` 相当のマシンを作り、カーネルをロードする。
// kernel は ELF (PT_LOAD を物理アドレスに置く) か、0x80000000 に置く生のバイナリ。
// UART の送信データは stdout に書かれる。stdin が nil なら入力は m.SendInput で注入する。
func NewVirtMachine(kernel []byte, stdout io.Writer, stdin io.Reader, opt *VirtOptions) (*Machine, error) {
	size := uint64(DefaultRAMSize)
	if opt != nil && opt.RAMSize > 0 {
		size = opt.RAMSize
	}
	bus := NewBus(ramBase, size)
	cpu := NewCPU(bus)
	m := &Machine{CPU: cpu, Bus: bus, UART: NewUART(stdin, stdout), CLINT: NewCLINT()}
	cpu.Clint = m.CLINT
	cpu.OnTrap = m.recordTrap
	cpu.IdleLimit = m.idleLimit
	bus.Map(testDevBase, 0x1000, &TestDevice{cpu})
	bus.Map(clintBase, clintSize, m.CLINT)
	bus.Map(uartBase, uartSize, m.UART)

	f, info, err := ParseELF(kernel)
	if err == nil {
		if err := LoadELF(f, info, bus); err != nil {
			return nil, err
		}
		cpu.PC = info.Entry
		m.Symbols = readSymbols(f)
	} else {
		// ELF でなければ生のバイナリとして 0x80000000 に置く
		if len(kernel) < 4 || binary.LittleEndian.Uint32(kernel) == 0x464c457f {
			return nil, err // ELF だが壊れている
		}
		if !bus.WriteBytes(ramBase, kernel) {
			return nil, errors.New("kernel image too large")
		}
		cpu.PC = ramBase
	}
	m.Entry = cpu.PC
	// QEMU と同じく a0 = hartid, a1 = デバイスツリーのアドレス (ここでは無し)
	cpu.X[10], cpu.X[11] = 0, 0
	return m, nil
}

// ---- 実行ループ ----

// ErrStepLimit は Run の命令数上限に達したことを表す (CLI の --max-insts 用)。
var ErrStepLimit = errors.New("instruction limit reached")

// Step は 1 命令 (またはトラップの受付 1 回) を実行する。止まっていれば何もしない。
func (m *Machine) Step() {
	if !m.CPU.Halted {
		m.CPU.Step()
	}
}

// Run は最大 maxSteps ステップ (0 なら無制限) 実行する。次のときは途中で戻る:
// マシンが止まった (exited=true, code=終了コード, err=異常終了の理由)、
// RunUntilTrap 中にトラップが起きた、リアルタイムアイドルで待ち時間を使い切った (m.Idle())。
// 1 ステップ = 1 命令または 1 回のトラップ受付。
func (m *Machine) Run(maxSteps uint64) (exited bool, code int, err error) {
	c := m.CPU
	m.idle, m.trapped = false, false
	for n := uint64(0); !c.Halted && (maxSteps == 0 || n < maxSteps); n++ {
		c.Step()
		if m.idle || (m.stopOnTrap && m.trapped) {
			break
		}
	}
	return c.Halted, c.ExitCode, c.Err
}

// RunUntilTrap は次のトラップ (例外・割り込み・mret/sret・システムコール) が起きるまで実行する。
// trapped はトラップで止まったかどうか (false なら上限・停止・アイドル)。
func (m *Machine) RunUntilTrap(maxSteps uint64) (trapped bool) {
	m.stopOnTrap = true
	defer func() { m.stopOnTrap = false }()
	m.Run(maxSteps)
	return m.trapped
}

// Exited はマシンが止まった (電源断・exit・異常終了) かどうか。
func (m *Machine) Exited() bool { return m.CPU.Halted }

// SetRealtimeIdle を true にすると、WFI で待つときに mtime を自由に早送りせず、
// SetIdleBudget で与えた分だけ進める。使い切ると Run は途中で戻り、m.Idle() が true になる。
// (ブラウザで実時間に合わせるため: 毎フレーム「経過時間 × 10MHz」を足す)
func (m *Machine) SetRealtimeIdle(on bool) { m.realtimeIdle = on }

// SetIdleBudget は次の Run で WFI が早送りしてよい tick 数 (10MHz なら 1ms = 10000) を決める。
func (m *Machine) SetIdleBudget(ticks uint64) { m.idleBudget = ticks }

// Idle は直前の Run が「WFI で割り込み待ち」のために止まったかどうか。
func (m *Machine) Idle() bool { return m.idle }

func (m *Machine) idleLimit(want uint64) uint64 {
	if !m.realtimeIdle {
		return want
	}
	n := min(want, m.idleBudget)
	m.idleBudget -= n
	if n < want {
		m.idle = true
	}
	return n
}

func (m *Machine) recordTrap(ev *TrapEvent) {
	if m.traps == nil {
		m.traps = make([]TrapEvent, 0, trapLogSize)
	}
	if len(m.traps) < trapLogSize {
		m.traps = append(m.traps, *ev)
	} else {
		m.traps[m.trapHead] = *ev
		m.trapHead = (m.trapHead + 1) % trapLogSize
	}
	m.trapCount++
	m.trapped = true
	if m.OnTrap != nil {
		m.OnTrap(*ev)
	}
}

// Traps は直近のトラップ (最大 256 件) を古い順に返す。
func (m *Machine) Traps() []TrapEvent {
	out := make([]TrapEvent, 0, len(m.traps))
	out = append(out, m.traps[m.trapHead:]...)
	return append(out, m.traps[:m.trapHead]...)
}

// TrapCount はこれまでに起きたトラップの総数。
func (m *Machine) TrapCount() uint64 { return m.trapCount }

// DumpRegs はレジスタを人間向けに書き出す (CLI の異常終了時用)。
func (c *CPU) DumpRegs(w io.Writer) {
	fmt.Fprintf(w, "pc=%016x priv=%c steps=%d\n", c.PC, "USHM"[c.Priv], c.Steps)
	for i := 0; i < 32; i++ {
		fmt.Fprintf(w, "%-4s=%016x", regNames[i], c.X[i])
		if i%4 == 3 {
			fmt.Fprintln(w)
		} else {
			fmt.Fprint(w, "  ")
		}
	}
	fmt.Fprintf(w, "mstatus=%x mcause=%x mepc=%x mtval=%x\n",
		c.csr[csrMstatus], c.csr[csrMcause], c.csr[csrMepc], c.csr[csrMtval])
}
