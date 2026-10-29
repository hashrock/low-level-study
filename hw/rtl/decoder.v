// decoder.v — 命令デコーダ: 32 ビットの命令語 → 名前の付いた制御信号
//
// ここが「命令のビットが電気信号になる」場所。組み合わせ回路 (クロック無し) なので、
// inst が変わるとすぐ (同じクロックサイクルの中で) 全部の出力が変わる。
// 出力の制御信号が、データパスのマルチプレクサ (どの値を選ぶか) と
// 書き込みイネーブル (書くか書かないか) を決める。
//
// 対応する emu のコード: emu/rv/decode.go の Decode (ビットの切り出し) と
// emu/rv/cpu.go の execute の switch (命令ごとに何をするか)。
`include "defs.vh"

module decoder (
    input  wire [31:0] inst,

    // ---- レジスタ番号 (命令のどのビットかは全形式で共通) ----
    output wire [4:0]  rd,
    output wire [4:0]  rs1,
    output wire [4:0]  rs2,
    output wire [2:0]  funct3,

    // ---- 制御信号 ----
    output reg         reg_we,     // rd に書き戻す
    output reg  [1:0]  a_sel,      // ALU 入力 A: rs1 / pc / 0
    output reg         b_sel,      // ALU 入力 B: rs2 / imm
    output reg  [3:0]  alu_op,     // ALU の演算
    output reg         is_word,    // *W 命令 (32 ビットで計算して符号拡張)
    output reg         is_muldiv,  // M 拡張 (mul/div/rem)。結果は乗除算器から取る
    output reg  [2:0]  imm_sel,    // 即値の形式 I/S/B/U/J
    output reg  [1:0]  wb_sel,     // 書き戻す値: ALU / メモリ / pc+4 / syscall
    output reg         mem_read,   // ロード
    output reg         mem_write,  // ストア
    output reg         branch,     // 条件分岐 (成立するかは branch_unit が決める)
    output reg         jump,       // jal / jalr (無条件ジャンプ)
    output reg         jalr,       // 飛び先の bit0 を 0 にする (jalr だけ)
    output reg         ecall,      // システムコール (外に信号として出す)
    output reg         illegal     // 知らない命令
);
    wire [6:0] opcode = inst[6:0];
    wire [6:0] funct7 = inst[31:25];
    assign rd     = inst[11:7];
    assign funct3 = inst[14:12];
    assign rs1    = inst[19:15];
    assign rs2    = inst[24:20];

    always @(*) begin
        // まず全部「何もしない」にしておき、命令ごとに必要なものだけ 1 にする。
        // (こう書くと、書き忘れた信号がラッチにならず 0 になる)
        reg_we    = 1'b0;
        a_sel     = `A_RS1;
        b_sel     = `B_RS2;
        alu_op    = `ALU_ADD;
        is_word   = 1'b0;
        is_muldiv = 1'b0;
        imm_sel   = `IMM_I;
        wb_sel    = `WB_ALU;
        mem_read  = 1'b0;
        mem_write = 1'b0;
        branch    = 1'b0;
        jump      = 1'b0;
        jalr      = 1'b0;
        ecall     = 1'b0;
        illegal   = 1'b0;

        case (opcode)
            `OPC_LUI: begin            // rd = 0 + imm
                reg_we = 1; a_sel = `A_ZERO; b_sel = `B_IMM; imm_sel = `IMM_U;
            end
            `OPC_AUIPC: begin          // rd = pc + imm
                reg_we = 1; a_sel = `A_PC; b_sel = `B_IMM; imm_sel = `IMM_U;
            end
            `OPC_JAL: begin            // rd = pc + 4, 飛び先 = pc + imm (ALU で計算)
                reg_we = 1; jump = 1; a_sel = `A_PC; b_sel = `B_IMM; imm_sel = `IMM_J;
                wb_sel = `WB_PC4;
            end
            `OPC_JALR: begin           // rd = pc + 4, 飛び先 = (rs1 + imm) & ~1
                reg_we = 1; jump = 1; jalr = 1; b_sel = `B_IMM; imm_sel = `IMM_I;
                wb_sel = `WB_PC4;
            end
            `OPC_BRANCH: begin         // 飛び先 = pc + imm (ALU)。比較は branch_unit
                branch = 1; a_sel = `A_PC; b_sel = `B_IMM; imm_sel = `IMM_B;
            end
            `OPC_LOAD: begin           // アドレス = rs1 + imm, rd = メモリ
                reg_we = 1; mem_read = 1; b_sel = `B_IMM; imm_sel = `IMM_I;
                wb_sel = `WB_MEM;
                illegal = (funct3 == 3'b111);           // そんなロードは無い
            end
            `OPC_STORE: begin          // アドレス = rs1 + imm, メモリ = rs2
                mem_write = 1; b_sel = `B_IMM; imm_sel = `IMM_S;
                illegal = funct3[2];
            end
            `OPC_OP_IMM, `OPC_OP_IMM_32: begin   // rd = rs1 (演算) imm
                reg_we  = 1; b_sel = `B_IMM; imm_sel = `IMM_I;
                is_word = (opcode == `OPC_OP_IMM_32);
                // bit30 が意味を持つのは右シフト (srli / srai) だけ。
                // addi の即値の bit30 を「sub」と読み違えないように funct3 で絞る。
                alu_op  = {(funct3 == 3'b101) & inst[30], funct3};
            end
            `OPC_OP, `OPC_OP_32: begin           // rd = rs1 (演算) rs2
                reg_we    = 1;
                is_word   = (opcode == `OPC_OP_32);
                is_muldiv = (funct7 == 7'b0000001);   // M 拡張は funct7 = 1
                alu_op    = {inst[30], funct3};       // add/sub, srl/sra は bit30 で区別
            end
            `OPC_MISC_MEM: begin
                // fence: 単一サイクル・キャッシュ無しの CPU ではメモリの順序は
                // 最初から守られているので、何もしない (nop) でよい。
            end
            `OPC_SYSTEM: begin
                if (inst == 32'h00000073) begin   // ecall
                    ecall = 1; reg_we = 1; wb_sel = `WB_SYS;  // 戻り値を a0 に書く (rd 欄は 0 なので core が a0 に差し替える)
                end else
                    illegal = 1;                  // ebreak / CSR 命令は未実装
            end
            default: illegal = 1;
        endcase
    end
endmodule
