// core_pipe.v — 5 段パイプライン RV64IM コア (IF / ID / EX / MEM / WB)
//
// 単一サイクル版 (core.v) と同じ部品 (decoder, immgen, regfile, alu, muldiv, branch_unit, lsu) を使い、
// ステージの間にレジスタ (パイプラインレジスタ) を挟んで、5 つの命令を同時に処理する。
//
//   IF : pc で命令を読む                         ─┐ fd_* (IF/ID レジスタ)
//   ID : デコード・レジスタ読み出し・ハザード検出   ─┤ de_* (ID/EX)
//   EX : ALU・乗除算・分岐の判定 (と飛び先)         ─┤ em_* (EX/MEM)
//   MEM: データバス (ロード / ストア)               ─┤ mw_* (MEM/WB)
//   WB : レジスタに書き戻す = 命令の完了 (commit)  ─┘
//
// 1 サイクルあたりの仕事は 1/5 程度になるので、本物の回路ならクロックを速くできる。
// その代わり、命令どうしの依存 (ハザード) を回路で解決する必要がある:
//
//   データハザード: 直前の命令の結果をまだレジスタに書いていないのに、次の命令が読みたい
//     → フォワーディング (バイパス): EX/MEM・MEM/WB レジスタにある「書く予定の値」を ALU の入力に直接回す
//     → ロードの直後にその値を使う場合 (load-use) だけは、値がまだ MEM ステージで読めていないので
//       1 サイクル止めて (ストール) バブル (何もしない命令) を 1 つ挟む
//   制御ハザード: 分岐が成立するかは EX で分かるので、それまでに IF/ID に入った 2 命令は間違い
//     → 「分岐しない」と予測して pc+4 を取り込み続け、外れたら 2 命令を捨てる (フラッシュ)
//
// ecall: テストベンチ (OS 役) がレジスタファイルを直接読むので、先行する命令が全部 WB を
// 抜けるまで ID で待たせてから (ドレイン) EX に進め、EX で処理してもらう。戻り値は ALU の結果と
// 同じように後ろへ流れ、後続の命令へはフォワーディングで渡る。
`include "defs.vh"

module core_pipe (
    input  wire        clk,
    input  wire        rst,
    input  wire [63:0] reset_pc,
    output wire [63:0] i_addr,
    input  wire [31:0] i_data,
    output wire        d_valid,
    output wire        d_we,
    output wire [63:0] d_addr,
    output wire [63:0] d_wdata,
    output wire [7:0]  d_wstrb,
    input  wire [63:0] d_rdata,
    input  wire        d_ready,
    output wire        ecall_o,
    input  wire        sys_done,
    input  wire        sys_we,
    input  wire [63:0] sys_ret,
    output wire        commit,
    output wire [63:0] c_pc,
    output wire [31:0] c_inst,
    output wire [4:0]  c_rd,
    output wire [63:0] c_val,
    output wire [63:0] c_saddr,
    output wire [3:0]  c_ssize,
    output wire [63:0] c_sdata,
    output wire        illegal_o,
    output wire        misaligned_o
);
    // ---- 止める・捨てる信号 (下の各ステージで決まる) ----
    wire mem_stall;        // MEM: バスの応答待ち → IF〜MEM を止め、WB にバブル
    wire ex_stall;         // EX : ecall の処理待ち → IF〜EX を止め、MEM にバブル
    wire id_stall;         // ID : load-use / ecall のドレイン → IF・ID を止め、EX にバブル
    wire redirect;         // EX : 分岐成立・ジャンプ → pc を飛び先に、IF/ID と ID/EX を捨てる
    wire [63:0] redirect_pc;
    wire front_stall = mem_stall | ex_stall | id_stall;

    // =====================================================================
    // IF: 命令フェッチ
    // =====================================================================
    reg [63:0] pc;
    always @(posedge clk) begin
        if (rst)              pc <= reset_pc;
        else if (redirect)    pc <= redirect_pc;
        else if (!front_stall) pc <= pc + 64'd4;   // 「分岐しない」と予測して次を読む
    end
    assign i_addr = pc;

    // ---- IF/ID パイプラインレジスタ ----
    reg        fd_valid;
    reg [63:0] fd_pc;
    reg [31:0] fd_inst;
    always @(posedge clk) begin
        if (rst || redirect) begin                    // 予測が外れた命令を捨てる
            fd_valid <= 1'b0;
            fd_inst  <= `NOP;                         // (波形で見やすいように nop にしておく)
        end
        else if (!front_stall) begin
            fd_valid <= 1'b1;
            fd_pc    <= pc;
            fd_inst  <= i_data;
        end
    end

    // =====================================================================
    // ID: デコード・レジスタ読み出し・ハザード検出
    // =====================================================================
    wire [4:0] rd, rs1, rs2;
    wire [2:0] funct3;
    wire reg_we, b_sel, is_word, is_muldiv, mem_read, mem_write, branch, jump, jalr, ecall, illegal;
    wire [1:0] a_sel, wb_sel;
    wire [3:0] alu_op;
    wire [2:0] imm_sel;
    decoder u_dec (
        .inst(fd_inst), .rd(rd), .rs1(rs1), .rs2(rs2), .funct3(funct3),
        .reg_we(reg_we), .a_sel(a_sel), .b_sel(b_sel), .alu_op(alu_op), .is_word(is_word),
        .is_muldiv(is_muldiv), .imm_sel(imm_sel), .wb_sel(wb_sel), .mem_read(mem_read),
        .mem_write(mem_write), .branch(branch), .jump(jump), .jalr(jalr), .ecall(ecall),
        .illegal(illegal));

    wire [63:0] imm;
    immgen u_imm (.inst(fd_inst), .imm_sel(imm_sel), .imm(imm));

    // レジスタファイル (書き込みは WB ステージから)
    wire [63:0] rf_rs1, rf_rs2;
    wire        rf_we;
    wire [4:0]  rf_rd;
    wire [63:0] rf_wdata;
    regfile u_rf (.clk(clk), .rs1(rs1), .rs2(rs2), .rs1_val(rf_rs1), .rs2_val(rf_rs2),
                  .we(rf_we), .rd(rf_rd), .rd_val(rf_wdata));

    // 同じサイクルに WB が書こうとしている値は、まだレジスタに入っていないので直接もらう
    // (「前半で書いて後半で読む」レジスタファイルと同じ効果)
    wire [63:0] id_rs1_val = (rf_we && rf_rd != 0 && rf_rd == rs1) ? rf_wdata : rf_rs1;
    wire [63:0] id_rs2_val = (rf_we && rf_rd != 0 && rf_rd == rs2) ? rf_wdata : rf_rs2;

    // この命令が rs1 / rs2 を本当に読むか (読まないレジスタで無駄に止まらないように)
    wire [6:0] opcode   = fd_inst[6:0];
    wire uses_rs1 = !(opcode == `OPC_LUI || opcode == `OPC_AUIPC || opcode == `OPC_JAL);
    wire uses_rs2 = (opcode == `OPC_OP || opcode == `OPC_OP_32 ||
                     opcode == `OPC_STORE || opcode == `OPC_BRANCH);

    // ---- ID/EX レジスタ (宣言だけ先に。ハザード検出が EX の命令を見るため) ----
    reg        de_valid;
    reg [63:0] de_pc, de_imm, de_rs1_val, de_rs2_val;
    reg [31:0] de_inst;
    reg [4:0]  de_rd, de_rs1, de_rs2;
    reg [2:0]  de_funct3;
    reg [1:0]  de_a_sel, de_wb_sel;
    reg [3:0]  de_alu_op;
    reg        de_reg_we, de_b_sel, de_is_word, de_is_muldiv, de_mem_read, de_mem_write,
               de_branch, de_jump, de_jalr, de_ecall, de_illegal;
    reg        em_valid, mw_valid;   // (下で定義。ドレイン判定に使う)

    // load-use ハザード: EX にいるロードの結果を、ID の命令がすぐ使いたい
    wire load_use = de_valid && de_mem_read && de_rd != 0 &&
                    ((uses_rs1 && de_rd == rs1) || (uses_rs2 && de_rd == rs2));
    // ecall は先行する命令が全部完了してから (レジスタファイルが最新になってから) 進める
    wire ecall_drain = ecall && (de_valid || em_valid || mw_valid);
    assign id_stall = fd_valid && (load_use || ecall_drain);

    // =====================================================================
    // EX: フォワーディング・ALU・乗除算・分岐
    // =====================================================================
    // ---- EX/MEM, MEM/WB レジスタ (フォワーディングで参照するので先に宣言) ----
    reg [63:0] em_pc, em_result, em_store, em_addr;
    reg [31:0] em_inst;
    reg [4:0]  em_rd;
    reg [2:0]  em_funct3;
    reg        em_reg_we, em_mem_read, em_mem_write, em_illegal;
    reg [63:0] mw_pc, mw_val, mw_saddr, mw_sdata;
    reg [31:0] mw_inst;
    reg [4:0]  mw_rd;
    reg [3:0]  mw_ssize;
    reg        mw_reg_we, mw_illegal;

    // フォワーディングの選択: 0 = ID で読んだ値, 1 = EX/MEM から, 2 = MEM/WB から
    // (新しい方 = MEM ステージを優先する)
    wire em_fwd_ok = em_valid && em_reg_we && em_rd != 0 && !em_mem_read;  // ロードの値はまだ無い
    wire mw_fwd_ok = mw_valid && mw_reg_we && mw_rd != 0;
    wire [1:0] fwd_a = (em_fwd_ok && em_rd == de_rs1) ? 2'd1 :
                       (mw_fwd_ok && mw_rd == de_rs1) ? 2'd2 : 2'd0;
    wire [1:0] fwd_b = (em_fwd_ok && em_rd == de_rs2) ? 2'd1 :
                       (mw_fwd_ok && mw_rd == de_rs2) ? 2'd2 : 2'd0;
    wire [63:0] ex_rs1 = fwd_a == 2'd1 ? em_result : fwd_a == 2'd2 ? mw_val : de_rs1_val;
    wire [63:0] ex_rs2 = fwd_b == 2'd1 ? em_result : fwd_b == 2'd2 ? mw_val : de_rs2_val;

    // ---- ID/EX レジスタの更新 (フォワーディングの値 ex_rs1/ex_rs2 を使うので EX の定義の後に置く) ----
    always @(posedge clk) begin
        if (rst || redirect) begin                          // 予測が外れた命令を捨てる
            de_valid <= 1'b0; de_inst <= `NOP;
        end
        else if (mem_stall || ex_stall) begin               // EX ごと止まる: 命令はそのまま。ただし
            de_rs1_val <= ex_rs1;                           // フォワーディングで受け取った値は今のうちに
            de_rs2_val <= ex_rs2;                           // 保存する (待つ間に供給元が WB を抜けて消えるので)
        end
        else if (id_stall) begin                            // バブル (= nop) を入れる
            de_valid <= 1'b0; de_inst <= `NOP;
        end
        else begin
            de_valid   <= fd_valid;
            de_pc      <= fd_pc;       de_inst    <= fd_inst;
            de_imm     <= imm;
            de_rs1_val <= id_rs1_val;  de_rs2_val <= id_rs2_val;
            de_rd      <= rd;          de_rs1     <= rs1;        de_rs2 <= rs2;
            de_funct3  <= funct3;
            de_a_sel   <= a_sel;       de_b_sel   <= b_sel;      de_alu_op <= alu_op;
            de_wb_sel  <= wb_sel;      de_reg_we  <= reg_we;
            de_is_word <= is_word;     de_is_muldiv <= is_muldiv;
            de_mem_read <= mem_read;   de_mem_write <= mem_write;
            de_branch  <= branch;      de_jump    <= jump;       de_jalr <= jalr;
            de_ecall   <= ecall;       de_illegal <= illegal;
        end
    end

    wire [63:0] alu_a = (de_a_sel == `A_PC)   ? de_pc :
                        (de_a_sel == `A_ZERO) ? 64'd0 : ex_rs1;
    wire [63:0] alu_b = (de_b_sel == `B_IMM)  ? de_imm : ex_rs2;
    wire [63:0] alu_y;
    alu u_alu (.a(alu_a), .b(alu_b), .alu_op(de_alu_op), .is_word(de_is_word), .y(alu_y));

    wire [63:0] md_a = de_is_muldiv ? ex_rs1 : 64'd0;   // オペランドアイソレーション (core.v 参照)
    wire [63:0] md_b = de_is_muldiv ? ex_rs2 : 64'd0;
    wire [2:0]  md_f = de_is_muldiv ? de_funct3 : 3'd0;
    wire [63:0] md_y;
    muldiv u_md (.a(md_a), .b(md_b), .funct3(md_f), .is_word(de_is_muldiv & de_is_word), .y(md_y));

    wire br_taken;
    branch_unit u_br (.a(ex_rs1), .b(ex_rs2), .funct3(de_funct3), .taken(br_taken));

    // 書き戻す予定の値 (ロードは MEM で差し替える)
    wire [63:0] ex_result = (de_wb_sel == `WB_PC4) ? de_pc + 64'd4 :
                            (de_wb_sel == `WB_SYS) ? sys_ret :
                            de_is_muldiv ? md_y : alu_y;

    assign ecall_o  = de_valid && de_ecall && !rst;
    assign ex_stall = ecall_o && !sys_done;

    assign redirect    = de_valid && (de_jump || (de_branch && br_taken)) && !mem_stall && !ex_stall;
    assign redirect_pc = {alu_y[63:1], alu_y[0] & ~de_jalr};

    always @(posedge clk) begin
        if (rst)            em_valid <= 1'b0;
        else if (mem_stall) ;                       // そのまま
        else if (ex_stall)  begin em_valid <= 1'b0; em_inst <= `NOP; end   // バブル
        else begin
            em_valid     <= de_valid;
            em_pc        <= de_pc;      em_inst <= de_inst;
            em_result    <= ex_result;
            em_addr      <= alu_y;
            em_store     <= ex_rs2;
            em_rd        <= de_ecall ? 5'd10 : de_rd;               // ecall の戻り値は a0 へ
            em_reg_we    <= de_reg_we && (!de_ecall || sys_we);     // exit は書かない
            em_funct3    <= de_funct3;
            em_mem_read  <= de_mem_read;
            em_mem_write <= de_mem_write;
            em_illegal   <= de_illegal;
        end
    end

    // =====================================================================
    // MEM: データバス
    // =====================================================================
    wire [63:0] load_val;
    wire        misaligned;
    lsu u_lsu (.addr(em_addr), .funct3(em_funct3), .store_val(em_store),
               .wdata(d_wdata), .wstrb(d_wstrb), .rdata(d_rdata), .load_val(load_val),
               .misaligned(misaligned));
    assign d_valid   = em_valid && (em_mem_read || em_mem_write) && !rst;
    assign d_we      = em_mem_write;
    assign d_addr    = em_addr;
    assign mem_stall = d_valid && !d_ready;
    assign misaligned_o = misaligned && d_valid;

    wire [63:0] mem_val = em_mem_read ? load_val : em_result;
    wire [3:0]  ssize   = em_mem_write ? (4'd1 << em_funct3[1:0]) : 4'd0;
    wire [63:0] smask   = (ssize == 4'd8) ? ~64'd0 : ((64'd1 << {ssize, 3'b000}) - 64'd1);

    always @(posedge clk) begin
        if (rst || mem_stall) begin                 // 応答待ちの間は WB にバブル
            mw_valid <= 1'b0; mw_inst <= `NOP;
        end
        else begin
            mw_valid   <= em_valid;
            mw_pc      <= em_pc;     mw_inst <= em_inst;
            mw_val     <= mem_val;
            mw_rd      <= em_rd;     mw_reg_we <= em_reg_we;
            mw_ssize   <= ssize;
            mw_saddr   <= em_mem_write ? em_addr : 64'd0;
            mw_sdata   <= em_store & smask;
            mw_illegal <= em_illegal;
        end
    end

    // =====================================================================
    // WB: 書き戻し = 命令の完了
    // =====================================================================
    assign rf_we    = mw_valid && mw_reg_we;
    assign rf_rd    = mw_rd;
    assign rf_wdata = mw_val;

    assign commit    = mw_valid && !rst;
    assign c_pc      = mw_pc;
    assign c_inst    = mw_inst;
    assign c_rd      = (rf_we && mw_rd != 0) ? mw_rd : 5'd0;
    assign c_val     = (c_rd != 0) ? mw_val : 64'd0;
    assign c_saddr   = mw_saddr;
    assign c_ssize   = mw_ssize;
    assign c_sdata   = mw_ssize != 0 ? mw_sdata : 64'd0;
    // 不正命令は「完了するとき」にだけ報告する (分岐予測が外れた側の命令は捨てられるので)
    assign illegal_o = mw_valid && mw_illegal;
endmodule
