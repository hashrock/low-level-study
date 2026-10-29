// regfile.v — レジスタファイル: x0..x31 (64 ビット × 32 本)
//
//   読み出しポート 2 本 (rs1, rs2) : 組み合わせ回路。番号を入れると同じサイクル内に値が出る
//   書き込みポート 1 本 (rd)       : クロックの立ち上がりで、we = 1 のときだけ書き込まれる
//
// 「1 命令 = 1 クロック」のコアでは、サイクルの間ずっと読み出した値で計算し、
// サイクルの最後 (次の立ち上がりエッジ) で結果が rd に入る。これが「書き戻し」。
// x0 は常に 0 (書き込みは捨て、読むと 0)。emu では Step の最後の c.X[0] = 0 がこれに当たる。
module regfile (
    input  wire        clk,
    input  wire [4:0]  rs1,
    input  wire [4:0]  rs2,
    output wire [63:0] rs1_val,
    output wire [63:0] rs2_val,
    input  wire        we,
    input  wire [4:0]  rd,
    input  wire [63:0] rd_val
);
    reg [63:0] regs [0:31];   // テストベンチは tb.dut.core.u_rf.regs[n] で直接覗ける

    integer i;
    initial for (i = 0; i < 32; i = i + 1) regs[i] = 64'd0;

    assign rs1_val = (rs1 == 5'd0) ? 64'd0 : regs[rs1];
    assign rs2_val = (rs2 == 5'd0) ? 64'd0 : regs[rs2];

    always @(posedge clk) begin
        if (we && rd != 5'd0)
            regs[rd] <= rd_val;   // ノンブロッキング代入 = 「エッジの瞬間に一斉に」更新
    end
endmodule
