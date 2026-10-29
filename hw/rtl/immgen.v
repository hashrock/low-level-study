// immgen.v — 即値生成器: 命令のあちこちに散らばった即値ビットを集めて 64 ビットに符号拡張する
//
// 符号ビットはどの形式でも inst[31] にあるので、{N{inst[31]}} で上位を埋めれば符号拡張になる。
// B / J 形式の並びがバラバラなのは、rs1/rs2/rd の位置を全形式で揃えた代償 (asm/README.md 参照)。
// ハードウェアでは「ビットを並べ替える」のは配線をつなぎ替えるだけで、ゲートは要らない。
// 対応する emu のコード: emu/rv/decode.go の immI / immS / immB / immU / immJ
`include "defs.vh"

module immgen (
    input  wire [31:0] inst,
    input  wire [2:0]  imm_sel,
    output reg  [63:0] imm
);
    wire s = inst[31];  // 符号ビット
    always @(*) begin
        case (imm_sel)
            //                  上位を符号で埋める   集めたビット
            `IMM_I: imm = {{52{s}}, inst[31:20]};
            `IMM_S: imm = {{52{s}}, inst[31:25], inst[11:7]};
            `IMM_B: imm = {{51{s}}, inst[31], inst[7], inst[30:25], inst[11:8], 1'b0};
            `IMM_U: imm = {{32{s}}, inst[31:12], 12'b0};
            `IMM_J: imm = {{43{s}}, inst[31], inst[19:12], inst[20], inst[30:21], 1'b0};
            default: imm = 64'd0;
        endcase
    end
endmodule
