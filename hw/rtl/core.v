// core.v — 単一サイクル RV64IM コア (データパスの組み立て)
//
// 1 クロックの間に 1 命令を最後まで処理する。クロックの立ち上がりエッジで
// 「PC の更新」「rd への書き戻し」「メモリへの書き込み」が一斉に起き、次の命令に進む。
// エッジとエッジの間は、すべて組み合わせ回路で値が流れていくだけ:
//
//   pc ─▶ 命令メモリ ─▶ inst ─▶ decoder ─▶ 制御信号
//                          │           └▶ rs1/rs2 番号 ─▶ regfile ─▶ rs1_val / rs2_val
//                          └▶ immgen ─▶ imm
//   rs1_val / pc / 0 ─┐
//   rs2_val / imm   ──┴▶ ALU ─▶ alu_y ─┬▶ (アドレス) LSU ─▶ データバス ─▶ RAM / コンソール
//                                     ├▶ (飛び先) next_pc
//                                     └▶ wb_mux ─▶ regfile の書き込みポート
//
// 止まる (stall) のは 2 つの場合だけ:
//   - データバスの応答待ち (d_valid = 1 なのに d_ready = 0)
//   - ecall の処理待ち (ecall = 1 なのに sys_done = 0)
// stall 中は PC もレジスタも書き換えない (= 同じ命令をもう 1 サイクル続ける)。
//
// 対応する emu のコード: emu/rv/cpu.go の Step (fetch → decode → execute → PC 更新)
`include "defs.vh"

module core (
    input  wire        clk,
    input  wire        rst,
    input  wire [63:0] reset_pc,

    // ---- 命令フェッチ用のポート (RAM の 2 つ目の読み出し口に直結, 待ちなし) ----
    output wire [63:0] i_addr,
    input  wire [31:0] i_data,

    // ---- データバス (valid / ready ハンドシェイク) ----
    output wire        d_valid,    // コア → バス: 「この番地にアクセスしたい」
    output wire        d_we,       // 1 = 書き込み, 0 = 読み出し
    output wire [63:0] d_addr,
    output wire [63:0] d_wdata,
    output wire [7:0]  d_wstrb,
    input  wire [63:0] d_rdata,
    input  wire        d_ready,    // バス → コア: 「終わった (読み出しなら d_rdata が有効)」

    // ---- システムコール (OS が無いのでテストベンチが応答する) ----
    output wire        ecall_o,    // 今 ecall を実行中
    input  wire        sys_done,   // テストベンチ: 処理が終わった
    input  wire        sys_we,     // テストベンチ: a0 に戻り値を書く (exit では書かない)
    input  wire [63:0] sys_ret,    // テストベンチ: 戻り値

    // ---- 観測用 (テストベンチのコミットトレース・エラー検出) ----
    // 本物の CPU には無いが、検証のために「完了した命令が何をしたか」を外に出す
    // (RISC-V Formal Interface (RVFI) と同じ考え方)。パイプライン版も同じポートを持つ。
    output wire        commit,     // このサイクルの終わりで命令が完了する
    output wire [63:0] c_pc,       // 完了する命令の pc
    output wire [31:0] c_inst,     //               命令語
    output wire [4:0]  c_rd,       //               書き込むレジスタ (書かないなら 0)
    output wire [63:0] c_val,      //               書き込む値
    output wire [63:0] c_saddr,    //               ストアの番地
    output wire [3:0]  c_ssize,    //               ストアのバイト数 (ストアでなければ 0)
    output wire [63:0] c_sdata,    //               ストアの値
    output wire        illegal_o,
    output wire        misaligned_o
);
    // ================= フェッチ =================
    wire [63:0] pc;
    wire [63:0] next_pc;
    wire        stall;
    pc_reg u_pc (.clk(clk), .rst(rst), .reset_pc(reset_pc), .stall(stall), .next_pc(next_pc), .pc(pc));

    assign i_addr = pc;
    wire [31:0] inst = i_data;

    // ================= デコード =================
    wire [4:0] rd, rs1, rs2;
    wire [2:0] funct3;
    wire reg_we, b_sel, is_word, is_muldiv, mem_read, mem_write, branch, jump, jalr, ecall, illegal;
    wire [1:0] a_sel, wb_sel;
    wire [3:0] alu_op;
    wire [2:0] imm_sel;
    decoder u_dec (
        .inst(inst), .rd(rd), .rs1(rs1), .rs2(rs2), .funct3(funct3),
        .reg_we(reg_we), .a_sel(a_sel), .b_sel(b_sel), .alu_op(alu_op), .is_word(is_word),
        .is_muldiv(is_muldiv), .imm_sel(imm_sel), .wb_sel(wb_sel), .mem_read(mem_read),
        .mem_write(mem_write), .branch(branch), .jump(jump), .jalr(jalr), .ecall(ecall),
        .illegal(illegal));

    wire [63:0] imm;
    immgen u_imm (.inst(inst), .imm_sel(imm_sel), .imm(imm));

    // ================= レジスタファイル =================
    wire [63:0] rs1_val, rs2_val;
    wire        rf_we;
    wire [4:0]  rf_rd;
    wire [63:0] rf_wdata;
    regfile u_rf (.clk(clk), .rs1(rs1), .rs2(rs2), .rs1_val(rs1_val), .rs2_val(rs2_val),
                  .we(rf_we), .rd(rf_rd), .rd_val(rf_wdata));

    // ================= 実行 (ALU・乗除算器・分岐比較器) =================
    wire [63:0] alu_a = (a_sel == `A_PC)   ? pc :
                        (a_sel == `A_ZERO) ? 64'd0 : rs1_val;
    wire [63:0] alu_b = (b_sel == `B_IMM)  ? imm : rs2_val;

    wire [63:0] alu_y;
    alu u_alu (.a(alu_a), .b(alu_b), .alu_op(alu_op), .is_word(is_word), .y(alu_y));

    // 乗除算器には M 拡張の命令のときだけ値を入れる (それ以外は 0 に固定)。
    // 「使わない回路の入力を動かさない」= オペランドアイソレーション。実機では消費電力が減り、
    // シミュレーションでも巨大な乗除算を毎サイクル計算し直さずに済むので速くなる。
    wire [63:0] md_a = is_muldiv ? rs1_val : 64'd0;
    wire [63:0] md_b = is_muldiv ? rs2_val : 64'd0;
    wire [2:0]  md_f = is_muldiv ? funct3  : 3'd0;
    wire        md_w = is_muldiv & is_word;
    wire [63:0] md_y;
    muldiv u_md (.a(md_a), .b(md_b), .funct3(md_f), .is_word(md_w), .y(md_y));

    wire br_taken;
    branch_unit u_br (.a(rs1_val), .b(rs2_val), .funct3(funct3), .taken(br_taken));

    // 演算結果: M 拡張なら乗除算器、それ以外は ALU
    wire [63:0] ex_y = is_muldiv ? md_y : alu_y;

    // ================= メモリアクセス =================
    wire [63:0] load_val;
    wire        misaligned;
    lsu u_lsu (.addr(alu_y), .funct3(funct3), .store_val(rs2_val),
               .wdata(d_wdata), .wstrb(d_wstrb), .rdata(d_rdata), .load_val(load_val),
               .misaligned(misaligned));

    assign d_valid = (mem_read | mem_write) & !rst;
    assign d_we    = mem_write;
    assign d_addr  = alu_y;

    // ================= 次の PC =================
    // 分岐・ジャンプの飛び先は ALU が計算済み (pc + imm、jalr は rs1 + imm)。
    // jalr だけは bit0 を 0 にする決まり。
    wire [63:0] pc_plus4 = pc + 64'd4;
    wire        redirect = jump | (branch & br_taken);
    assign next_pc = redirect ? {alu_y[63:1], alu_y[0] & ~jalr} : pc_plus4;

    // ================= 書き戻し =================
    reg [63:0] wb_val;
    always @(*) begin
        case (wb_sel)
            `WB_ALU: wb_val = ex_y;
            `WB_MEM: wb_val = load_val;
            `WB_PC4: wb_val = pc_plus4;
            default: wb_val = sys_ret;    // `WB_SYS
        endcase
    end

    // ストール: バスの応答待ち、または ecall の処理待ち
    assign stall = (d_valid & !d_ready) | (ecall & !sys_done);

    assign rf_rd    = ecall ? 5'd10 : rd;                     // ecall の戻り値は a0 (x10) へ
    assign rf_we    = reg_we & !stall & !rst & (!ecall | sys_we);
    assign rf_wdata = wb_val;

    assign ecall_o      = ecall & !rst;
    assign illegal_o    = illegal & !rst;
    assign misaligned_o = misaligned & d_valid;

    // ================= 観測用ポート =================
    assign commit  = !stall & !rst;
    assign c_pc    = pc;
    assign c_inst  = inst;
    assign c_rd    = (rf_we && rf_rd != 5'd0) ? rf_rd : 5'd0;
    assign c_val   = (c_rd != 5'd0) ? rf_wdata : 64'd0;
    assign c_ssize = mem_write ? (4'd1 << funct3[1:0]) : 4'd0;
    assign c_saddr = mem_write ? alu_y : 64'd0;
    assign c_sdata = !mem_write       ? 64'd0 :
                     funct3[1:0] == 0 ? {56'd0, rs2_val[7:0]} :
                     funct3[1:0] == 1 ? {48'd0, rs2_val[15:0]} :
                     funct3[1:0] == 2 ? {32'd0, rs2_val[31:0]} : rs2_val;
endmodule
