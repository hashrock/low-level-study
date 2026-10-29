# hw/ — 自作 RISC-V CPU (Verilog) のビルド・実行・テスト・波形
#
#   make build                          シミュレータ (build/sim.vvp, build/pipe.vvp) を作る
#   make run  PROG="../cc/examples/fib.c"          実行 (出力とサイクル数)
#   make run  PROG="../asm/examples/main.s ../asm/examples/lib.s" WAIT=2
#   make run  PROG=... CORE=pipe                   5 段パイプライン版で実行
#   make wave PROG=tests/alu.s                     build/wave.vcd (リセット直後から LEN サイクル)
#   make wave PROG=../asm/examples/fib.s SYM=fib_iter LEN=60   fib_iter に来たところから
#   make lockstep PROG=../cc/examples/sieve.c      emu と 1 命令ずつ突き合わせる
#   make test                           全プログラム + lockstep (fib.c など長いものも含む)
#   make test-short                     長いもの (qsort.c, fib.c) を飛ばす

PROG ?= ../asm/examples/hello.s
WAIT ?= 0
LEN  ?= 200
SYM  ?=
CORE ?= single
ARGV ?=

SHELL    := /bin/bash
.SHELLFLAGS := -o pipefail -c

IVERILOG = iverilog -g2005 -Wall -Wno-timescale -s tb -I rtl -I tb
RTL      = $(wildcard rtl/*.v) $(wildcard rtl/*.vh)
B        = build

ifeq ($(CORE),pipe)
VVP = $(B)/pipe.vvp
else
VVP = $(B)/sim.vvp
endif

.PHONY: build run wave lockstep test test-short clean

build: $(B)/sim.vvp $(B)/pipe.vvp

$(B)/sim.vvp: tb/tb.v $(RTL)
	@mkdir -p $(B)
	$(IVERILOG) -o $@ tb/tb.v $(filter %.v,$(RTL))

$(B)/pipe.vvp: tb/tb_pipe.v tb/tb.v $(RTL)
	@mkdir -p $(B)
	$(IVERILOG) -o $@ tb/tb_pipe.v $(filter %.v,$(RTL))

# プログラムを ELF → hex (+ entry / sp / brk) にする
$(B)/prog.hex: FORCE
	@mkdir -p $(B)
	go run ./tools/load -o $(B)/prog $(if $(ARGV),-argv "$(ARGV)") $(if $(SYM),-wave-sym $(SYM)) $(PROG)
FORCE:

run: $(VVP) $(B)/prog.hex
	vvp -n $(VVP) +hex=$(B)/prog.hex $$(cat $(B)/prog.args) +wait=$(WAIT)

wave: $(VVP) $(B)/prog.hex
	vvp -n $(VVP) +hex=$(B)/prog.hex $$(cat $(B)/prog.args) +wait=$(WAIT) \
	    +vcd=$(B)/wave.vcd +wave_len=$(LEN) +trace=$(B)/wave.trace
	@echo "→ $(B)/wave.vcd (gtkwave $(B)/wave.vcd、または https://app.surfer-project.org/ に読み込む)"

lockstep:
	go run ./tools/lockstep -wait $(WAIT) $(if $(filter pipe,$(CORE)),-pipe) $(if $(ARGV),-argv "$(ARGV)") $(PROG)

test:
	go vet ./...
	go test -count=1 -v -timeout 30m ./... 2>&1 | grep -v '^=== \(RUN\|PAUSE\|CONT\)'

test-short:
	go vet ./...
	go test -count=1 -short -v ./... 2>&1 | grep -v '^=== \(RUN\|PAUSE\|CONT\)'

clean:
	rm -rf $(B)
