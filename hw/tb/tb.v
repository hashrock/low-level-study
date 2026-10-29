// tb.v — テストベンチ: クロックを作り、soc を動かし、「OS の代わり」をする
//
// ここはハードウェアではない (シミュレーション専用のプログラム)。役割は 4 つ:
//   1. クロックとリセットを作る。リセット直後に sp (x2) に初期スタックの番地を入れる
//      (本物なら exec したカーネルがやること)
//   2. コミットトレース: 命令が完了するたびに pc・命令語・書き込んだレジスタと値・ストアを 1 行出す
//      (+trace=ファイル)。Go の lockstep テストがこれを emu と 1 命令ずつ突き合わせる
//   3. システムコール: コアが ecall 信号を出したら、レジスタとメモリを
//      階層参照 (dut.core.u_rf.regs[17] のような「回路の中を直接覗く」書き方) で読んで
//      Linux の write / exit / brk を処理し、戻り値を sys_ret ポートで返す。
//      emu のユーザモード (emu/rv/syscall.go) と同じことを Verilog でやっている
//   4. コンソール (MMIO) の tx_valid を見て文字を表示する、VCD 波形を記録する
//
// plusargs:
//   +hex=FILE (ram.v が読む) +entry=HEX +sp=HEX +brk=HEX   … tools/load が .args に書く
//   +trace=FILE         コミットトレースを書く
//   +wait=N             RAM のデータポートの待ちサイクル数 (0..15)
//   +vcd=FILE           波形を記録する。記録範囲は下の 3 つで決める
//   +wave_pc=HEX        pc がこの値になったら記録開始 (省略時はリセット直後から)
//   +wave_len=N         記録するサイクル数 (省略時 200)。記録が終わったらシミュレーションも終了
//   +max_cycles=N       これを超えたら打ち切り (省略時 100,000,000)
`timescale 1ns/1ps

module tb;
    reg clk = 0;
    reg rst = 1;
    always #5 clk = ~clk;          // 10ns 周期 = 100MHz のつもり

    reg  [63:0] entry, sp_init, brk_start, brk_cur, brk_limit, wave_pc;
    reg  [3:0]  ram_wait;
    reg         sys_done = 0, sys_we = 0;
    reg  [63:0] sys_ret = 0;

    wire ecall, tx_valid, commit, illegal, misaligned, bus_error;
    wire [7:0] tx_data;
    wire [63:0] c_pc, c_val, c_saddr, c_sdata;
    wire [31:0] c_inst;
    wire [4:0]  c_rd;
    wire [3:0]  c_ssize;

    soc dut (
        .clk(clk), .rst(rst), .reset_pc(entry), .ram_wait(ram_wait),
        .ecall(ecall), .sys_done(sys_done), .sys_we(sys_we), .sys_ret(sys_ret),
        .tx_valid(tx_valid), .tx_data(tx_data),
        .commit(commit), .c_pc(c_pc), .c_inst(c_inst), .c_rd(c_rd), .c_val(c_val),
        .c_saddr(c_saddr), .c_ssize(c_ssize), .c_sdata(c_sdata),
        .illegal(illegal), .misaligned(misaligned), .bus_error(bus_error));

    // ---- 波形で見やすいように、よく使うレジスタを wire に引き出しておく ----
    // (VCD にはメモリ配列 (regs[]) の中身が出ないので)
    wire [63:0] x_ra = dut.core.u_rf.regs[1];
    wire [63:0] x_sp = dut.core.u_rf.regs[2];
    wire [63:0] x_a0 = dut.core.u_rf.regs[10];
    wire [63:0] x_a1 = dut.core.u_rf.regs[11];
    wire [63:0] x_a2 = dut.core.u_rf.regs[12];
    wire [63:0] x_a7 = dut.core.u_rf.regs[17];
    wire [63:0] pc   = dut.core.pc;     // パイプライン版では IF ステージの pc

    integer trace_fd = 0;
    reg [1023:0] fname;
    reg [63:0] cycles = 0, instret = 0, max_cycles, wave_len, wave_end;
    reg        wave_on = 0, has_vcd = 0, has_wave_pc = 0;

    initial begin
        if (!$value$plusargs("entry=%h", entry)) begin
            $display("tb: +entry= がありません (tools/load の .args を渡す)");
            $finish(0);
        end
        if (!$value$plusargs("sp=%h", sp_init))     sp_init = 64'h10_0000;
        if (!$value$plusargs("brk=%h", brk_start))  brk_start = 64'h8_0000;
        if (!$value$plusargs("wait=%d", ram_wait))  ram_wait = 0;
        if (!$value$plusargs("max_cycles=%d", max_cycles)) max_cycles = 100_000_000;
        if (!$value$plusargs("wave_len=%d", wave_len))     wave_len = 200;
        has_wave_pc = $value$plusargs("wave_pc=%h", wave_pc);
        brk_cur   = brk_start;
        brk_limit = sp_init - 64'h1_0000;   // スタック用に 64KiB 残す
        if ($value$plusargs("trace=%s", fname)) trace_fd = $fopen(fname, "w");
        if ($value$plusargs("vcd=%s", fname)) begin
            has_vcd = 1;
            $dumpfile(fname);
            $dumpvars(0, tb);
            $dumpoff;
        end
        // リセット: 最初の立ち上がりエッジで pc <= entry
        @(negedge clk);
        dut.core.u_rf.regs[2] = sp_init;      // カーネルの代わりに sp を設定
        @(posedge clk);
        @(negedge clk);
        rst = 0;
        if (has_vcd && !has_wave_pc) start_wave;
    end

    task start_wave;
        begin
            wave_on  = 1;
            wave_end = cycles + wave_len;
            $dumpon;
            $display("tb: VCD 記録開始 cycle=%0d pc=%h", cycles, pc);
        end
    endtask

    // ---- メモリを 1 バイト読む (階層参照で RAM の配列を直接見る。システムコール用) ----
    function [7:0] mem_byte(input [63:0] a);
        reg [63:0] w;
        begin
            w = dut.u_ram.mem[a[19:3]];
            mem_byte = w[a[2:0]*8 +: 8];
        end
    endfunction

    // ================= システムコール (クロックの立ち下がりで処理) =================
    // 立ち下がりで sys_done を出しておけば、次の立ち上がりで ecall が完了し、a0 に書き戻される。
    reg [63:0] a0, a1, a2, a7, i;
    reg        exiting = 0;
    reg [31:0] exit_code;
    // sys_done は立ち上がりエッジで 0 に戻す (ecall が完了したら次の ecall はまた処理が要る)。
    // ecall がほかの理由で止まることは無い (単一サイクル版: ecall はバスを使わない、
    // パイプライン版: ecall は前の命令が全部抜けてから EX に入る) ので、二重に処理されることはない。
    always @(negedge clk) begin
        if (!rst && ecall && !sys_done) begin
            a0 = dut.core.u_rf.regs[10];
            a1 = dut.core.u_rf.regs[11];
            a2 = dut.core.u_rf.regs[12];
            a7 = dut.core.u_rf.regs[17];
            sys_done <= 1;
            sys_we   <= 1;
            case (a7)
                64: begin                                   // write(fd, buf, len)
                    if (a0 == 1 || a0 == 2) begin
                        for (i = 0; i < a2; i = i + 1) $write("%c", mem_byte(a1 + i));
                        sys_ret <= a2;
                    end else
                        sys_ret <= -64'd9;                  // -EBADF
                end
                63: sys_ret <= -64'd9;                      // read: 標準入力は無い (-EBADF)
                93, 94: begin                               // exit(code)
                    exiting   = 1;
                    exit_code = a0[31:0];
                    sys_we   <= 0;                          // exit は a0 を書き換えない
                end
                214: begin                                  // brk(addr): 範囲内なら動かす。現在位置を返す
                    if (a0 >= brk_start && a0 < brk_limit) brk_cur = a0;
                    sys_ret <= brk_cur;
                end
                default: sys_ret <= -64'd38;                // -ENOSYS
            endcase
        end
    end

    // ================= 立ち上がりエッジ: コミットの記録と終了判定 =================
    // always @(posedge clk) の中で読む値は「エッジの直前」の値 (書き戻しはノンブロッキングなので
    // まだ反映されていない)。つまり、このサイクルで完了する命令の情報がそのまま読める。
    always @(posedge clk) begin
        sys_done <= 0; sys_we <= 0;
        if (!rst) begin
            cycles = cycles + 1;
            if (tx_valid) $write("%c", tx_data);     // MMIO コンソールの出力 (先に出す: 直後に exit しても失われない)
            if (illegal)    fail("illegal instruction");
            if (misaligned) fail("misaligned access (8 バイト境界をまたぐ)");
            if (bus_error)  fail("bus error (何もつながっていない番地)");
            if (commit) begin
                instret = instret + 1;
                // C pc 命令語 rd 値 ストア番地 ストアサイズ ストア値
                if (trace_fd != 0)
                    $fwrite(trace_fd, "C %h %h %0d %h %h %0d %h\n",
                        c_pc, c_inst, c_rd, c_val, c_saddr, c_ssize, c_sdata);
                if (c_inst == 32'h00000073 && exiting) finish_sim;
            end
            if (has_vcd && !wave_on && has_wave_pc && pc == wave_pc) start_wave;
            if (wave_on && cycles >= wave_end) begin
                $dumpoff;
                $display("\ntb: VCD 記録終了 cycle=%0d (ここでシミュレーションも止める)", cycles);
                $finish(0);
            end
            if (cycles >= max_cycles) fail("max_cycles を超えた");
        end
    end

    task finish_sim;
        begin
            $fflush;
            // 最後の行は Go 側のテストと Makefile が読む (標準エラー出力へ)
            $fdisplay(32'h8000_0002, "[hw] exit=%0d cycles=%0d instret=%0d", $signed(exit_code), cycles, instret);
            if (trace_fd != 0) $fclose(trace_fd);
            $finish(0);
        end
    endtask

    task fail(input [8*64-1:0] msg);
        begin
            $fflush;
            $fdisplay(32'h8000_0002, "[hw] ERROR %0s at pc=%h cycle=%0d (最後に完了した命令: pc=%h %h)", msg, pc, cycles, c_pc, c_inst);
            if (trace_fd != 0) $fclose(trace_fd);
            $finish(0);
        end
    endtask
endmodule
