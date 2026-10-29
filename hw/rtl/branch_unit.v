// branch_unit.v — 分岐比較器: rs1 と rs2 を比べて、分岐が成立するかを決める
//
// 比較は 3 種類 (等しい / 符号付きで小さい / 符号なしで小さい) だけで、
// funct3 の bit0 は「結果を反転する」(beq↔bne, blt↔bge, bltu↔bgeu) という意味になっている。
// 対応する emu のコード: emu/rv/cpu.go の execute の BEQ..BGEU の case
module branch_unit (
    input  wire [63:0] a,       // rs1
    input  wire [63:0] b,       // rs2
    input  wire [2:0]  funct3,
    output wire        taken
);
    wire eq  = (a == b);
    wire lt  = $signed(a) < $signed(b);
    wire ltu = a < b;
    reg  cond;
    always @(*) begin
        case (funct3[2:1])
            2'b00:   cond = eq;    // beq / bne
            2'b10:   cond = lt;    // blt / bge
            2'b11:   cond = ltu;   // bltu / bgeu
            default: cond = 1'b0;  // (funct3 = 01x の分岐命令は無い)
        endcase
    end
    assign taken = cond ^ funct3[0];
endmodule
