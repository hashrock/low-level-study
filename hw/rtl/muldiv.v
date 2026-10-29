// muldiv.v — M 拡張の乗除算器 (mul mulh mulhsu mulhu div divu rem remu と *W 版)
//
// 学習用に Verilog の * / % をそのまま書いた「1 サイクルで答えが出る」乗除算器。
// シミュレーションでは問題ないが、本物の回路にすると 64 ビットの割り算器は巨大で遅く、
// クロック周波数を大きく下げてしまう。実際の CPU は、乗算は数段のパイプライン、
// 除算は 1 ビットずつ (あるいは数ビットずつ) 何十サイクルもかけて求める
// 「マルチサイクル」ユニットにして、終わるまでコアを止める (ストール) のが普通。
//
// RISC-V は 0 除算でも例外にしない: 商 = 全ビット 1 (-1)、余り = 被除数。
// オーバーフロー (最小値 / -1) は 商 = 最小値、余り = 0。
// Verilog の / は 0 で割ると x (不定) になるので、これらは明示的に場合分けする。
// 対応する emu のコード: emu/rv/cpu.go の mulDiv
module muldiv (
    input  wire [63:0] a,
    input  wire [63:0] b,
    input  wire [2:0]  funct3,   // 000 mul, 001 mulh, 010 mulhsu, 011 mulhu, 100 div, 101 divu, 110 rem, 111 remu
    input  wire        is_word,
    output reg  [63:0] y
);
    // ---- 乗算: 128 ビットの積を作り、上半分か下半分を取る ----
    // 符号付きの値は符号拡張、符号なしの値は 0 拡張して 128 ビットにしてから掛けると、
    // 2 の補数のおかげで下位 128 ビットはどの組み合わせでも正しい。
    wire a_signed = (funct3 == 3'b001) || (funct3 == 3'b010);   // mulh, mulhsu
    wire b_signed = (funct3 == 3'b001);                          // mulh
    wire [127:0] a128 = {{64{a_signed & a[63]}}, a};
    wire [127:0] b128 = {{64{b_signed & b[63]}}, b};
    wire [127:0] prod = a128 * b128;

    // ---- 除算 (64 ビット) ----
    wire        sgn = !funct3[0];                     // div, rem は符号付き。divu, remu は符号なし
    wire [63:0] min64 = 64'h8000_0000_0000_0000;
    wire        b0  = (b == 64'd0);
    wire        ovf = sgn && a == min64 && b == {64{1'b1}};
    // 注意: Verilog では「? :」の片側が符号なしだと式全体が符号なしで計算されてしまうので、
    // 符号付きの割り算は別の wire で計算してから選ぶ
    wire [63:0] qs = $signed(a) / $signed(b),  qu = a / b;
    wire [63:0] rs = $signed(a) % $signed(b),  ru = a % b;
    wire [63:0] q64 = b0 ? {64{1'b1}} : ovf ? min64 : sgn ? qs : qu;
    wire [63:0] r64 = b0 ? a          : ovf ? 64'd0 : sgn ? rs : ru;

    // ---- 除算 (32 ビット, divw など): 下位 32 ビットどうしで割り、結果を符号拡張 ----
    wire [31:0] a32 = a[31:0], b32 = b[31:0];
    wire [31:0] min32 = 32'h8000_0000;
    wire        b0w  = (b32 == 32'd0);
    wire        ovfw = sgn && a32 == min32 && b32 == {32{1'b1}};
    wire [31:0] qsw = $signed(a32) / $signed(b32),  quw = a32 / b32;
    wire [31:0] rsw = $signed(a32) % $signed(b32),  ruw = a32 % b32;
    wire [31:0] q32 = b0w ? {32{1'b1}} : ovfw ? min32 : sgn ? qsw : quw;
    wire [31:0] r32 = b0w ? a32        : ovfw ? 32'd0 : sgn ? rsw : ruw;

    reg [31:0] r;
    always @(*) begin
        r = 32'd0;
        if (is_word) begin
            case (funct3)
                3'b000:          r = prod[31:0];             // mulw
                3'b100, 3'b101:  r = q32;                    // divw, divuw
                3'b110, 3'b111:  r = r32;                    // remw, remuw
                default:         r = 32'd0;                  // (mulhw などは存在しない)
            endcase
            y = {{32{r[31]}}, r};          // 32 ビットの結果を符号拡張
        end else begin
            case (funct3)
                3'b000:          y = prod[63:0];             // mul
                3'b001, 3'b010,
                3'b011:          y = prod[127:64];           // mulh, mulhsu, mulhu
                3'b100, 3'b101:  y = q64;                    // div, divu
                default:         y = r64;                    // rem, remu
            endcase
        end
    end
endmodule
