// pc.v — プログラムカウンタ: 次に実行する命令のアドレスを覚えておくレジスタ
//
// 立ち上がりエッジごとに next_pc を取り込む。ただし stall = 1 の間は今の値のまま
// (= 同じ命令を実行し続ける)。バスの応答待ちや ecall の処理待ちで使う。
module pc_reg (
    input  wire        clk,
    input  wire        rst,
    input  wire [63:0] reset_pc,   // リセット直後の PC (ELF のエントリ)
    input  wire        stall,
    input  wire [63:0] next_pc,
    output reg  [63:0] pc
);
    always @(posedge clk) begin
        if (rst)
            pc <= reset_pc;
        else if (!stall)
            pc <= next_pc;
    end
endmodule
