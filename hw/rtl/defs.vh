// defs.vh — モジュール間で共有する「信号の値の名前」
//
// Verilog では信号はただのビット列なので、2'd1 のような数字の意味を
// 人間が覚えなくて済むように名前を付けておく (C の enum のようなもの)。
`ifndef DEFS_VH
`define DEFS_VH

// ---- opcode (命令の下位 7 ビット) : 命令の大分類 ----
`define OPC_LOAD      7'b0000011  // lb lh lw ld lbu lhu lwu
`define OPC_MISC_MEM  7'b0001111  // fence
`define OPC_OP_IMM    7'b0010011  // addi slti ... slli srli srai
`define OPC_AUIPC     7'b0010111
`define OPC_OP_IMM_32 7'b0011011  // addiw slliw srliw sraiw
`define OPC_STORE     7'b0100011  // sb sh sw sd
`define OPC_OP        7'b0110011  // add sub ... + M 拡張 (mul div rem)
`define OPC_LUI       7'b0110111
`define OPC_OP_32     7'b0111011  // addw subw sllw srlw sraw + mulw divw remw ...
`define OPC_BRANCH    7'b1100011  // beq bne blt bge bltu bgeu
`define OPC_JALR      7'b1100111
`define OPC_JAL       7'b1101111
`define OPC_SYSTEM    7'b1110011  // ecall ebreak (CSR 命令は未実装)

// ---- ALU の演算 (alu_op) ----
// 下位 3 ビットは funct3 そのもの、bit3 は sub / sra を区別する命令の bit30。
// こうしておくとデコーダは {inst[30], funct3} をほぼそのまま流すだけで済む。
`define ALU_ADD  4'b0000
`define ALU_SUB  4'b1000
`define ALU_SLL  4'b0001
`define ALU_SLT  4'b0010
`define ALU_SLTU 4'b0011
`define ALU_XOR  4'b0100
`define ALU_SRL  4'b0101
`define ALU_SRA  4'b1101
`define ALU_OR   4'b0110
`define ALU_AND  4'b0111

// ---- ALU の入力 A の選び方 (a_sel) ----
`define A_RS1  2'd0   // 普通の演算
`define A_PC   2'd1   // auipc, jal, 分岐 (pc + imm を ALU で計算する)
`define A_ZERO 2'd2   // lui (0 + imm)

// ---- ALU の入力 B の選び方 (b_sel) ----
`define B_RS2  1'b0
`define B_IMM  1'b1

// ---- 書き戻す値の選び方 (wb_sel) ----
`define WB_ALU 2'd0   // ALU (または乗除算器) の結果
`define WB_MEM 2'd1   // ロードしたデータ
`define WB_PC4 2'd2   // pc + 4 (jal / jalr の戻り番地)
`define WB_SYS 2'd3   // ecall の戻り値 (テストベンチ = 「OS」から返ってくる)

// ---- 即値の形式 (imm_sel) ----
`define IMM_I 3'd0
`define IMM_S 3'd1
`define IMM_B 3'd2
`define IMM_U 3'd3
`define IMM_J 3'd4

// バブル (何もしない命令) を波形で見やすくするための nop = addi x0, x0, 0
`define NOP 32'h00000013

`endif
