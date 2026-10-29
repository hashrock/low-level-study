// alu.v — 算術論理演算器 (ALU)
//
// 組み合わせ回路。a, b, alu_op を入れると同じサイクル内に y が出る。
// is_word = 1 (addw, sllw などの *W 命令) のときは下位 32 ビットで計算し、
// 結果を 64 ビットに符号拡張する。
// 対応する emu のコード: emu/rv/cpu.go の execute の ADD/SUB/SLL.../ADDW... の case と sx32
`include "defs.vh"

module alu (
    input  wire [63:0] a,
    input  wire [63:0] b,
    input  wire [3:0]  alu_op,
    input  wire        is_word,
    output wire [63:0] y
);
    // シフト量: 64 ビット演算は下位 6 ビット、32 ビット演算は下位 5 ビットだけを使う
    wire [5:0] shamt = is_word ? {1'b0, b[4:0]} : b[5:0];

    // *W の右シフトは「下位 32 ビットだけ」をシフトする。
    // 論理シフト (srlw) は上位を 0 で、算術シフト (sraw) は bit31 で埋めてから 64 ビットでシフトすればよい。
    wire [63:0] a_srl = is_word ? {32'd0, a[31:0]}        : a;
    wire [63:0] a_sra = is_word ? {{32{a[31]}}, a[31:0]}  : a;

    reg [63:0] r;
    always @(*) begin
        case (alu_op)
            `ALU_ADD:  r = a + b;
            `ALU_SUB:  r = a - b;
            `ALU_SLL:  r = a << shamt;
            `ALU_SLT:  r = {63'd0, $signed(a) < $signed(b)};
            `ALU_SLTU: r = {63'd0, a < b};
            `ALU_XOR:  r = a ^ b;
            `ALU_SRL:  r = a_srl >> shamt;
            `ALU_SRA:  r = $signed(a_sra) >>> shamt;
            `ALU_OR:   r = a | b;
            `ALU_AND:  r = a & b;
            default:   r = 64'd0;
        endcase
    end

    // *W 命令: 下位 32 ビットを符号拡張 (emu の sx32)
    assign y = is_word ? {{32{r[31]}}, r[31:0]} : r;
endmodule
