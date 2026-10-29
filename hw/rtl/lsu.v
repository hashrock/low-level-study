// lsu.v — ロード/ストアユニット: CPU の「何バイトをどこに」とバスの「64 ビット幅のワード」の間をつなぐ
//
// バス (と RAM) は 8 バイト単位 (64 ビット) でしかデータを運ばない。そこで
//   ストア: rs2 の値をアドレスの下位 3 ビット分ずらして正しいバイト位置 (レーン) に置き、
//           どのバイトを書くかを wstrb (書き込みストローブ, 1 ビット = 1 バイト) で伝える
//   ロード: 読んできた 64 ビットから必要なバイトを取り出して、右端にずらし、
//           lb/lh/lw は符号拡張、lbu/lhu/lwu は 0 拡張する
// リトルエンディアンなので、アドレスの小さいバイトが下位ビットに来る。
//
// 8 バイト境界をまたぐアクセス (ミスアライン) は扱わない (misaligned を 1 にしてテストベンチが止める)。
// 対応する emu のコード: emu/rv/bus.go の Load / Store, emu/rv/cpu.go の LB..LD / SB..SD
module lsu (
    input  wire [63:0] addr,       // ALU が計算した rs1 + imm
    input  wire [2:0]  funct3,     // bit1:0 = サイズ (0:1, 1:2, 2:4, 3:8 バイト), bit2 = 符号なし
    input  wire [63:0] store_val,  // rs2
    output wire [63:0] wdata,      // バスに出すデータ (レーンに配置済み)
    output wire [7:0]  wstrb,      // どのバイトを書くか
    input  wire [63:0] rdata,      // バスから返ってきたデータ (64 ビット)
    output reg  [63:0] load_val,   // rd に書く値
    output wire        misaligned
);
    wire [2:0] off   = addr[2:0];            // ワード内のバイト位置
    wire [5:0] shift = {off, 3'b000};        // = off * 8 ビット
    wire [1:0] size  = funct3[1:0];

    // サイズに応じたバイトマスク: 1 バイト = 0000_0001, 2 = 0000_0011, 4 = 0000_1111, 8 = 1111_1111
    wire [7:0] mask = (size == 2'd0) ? 8'h01 :
                      (size == 2'd1) ? 8'h03 :
                      (size == 2'd2) ? 8'h0f : 8'hff;
    wire [15:0] strb16 = {8'd0, mask} << off;
    assign wstrb      = strb16[7:0];
    assign misaligned = (strb16[15:8] != 8'd0);   // はみ出したらワードをまたいでいる

    assign wdata = store_val << shift;

    // ロード: 目的のバイトを右端に寄せてから拡張する
    wire [63:0] sh = rdata >> shift;
    always @(*) begin
        case (funct3)
            3'b000: load_val = {{56{sh[7]}},  sh[7:0]};    // lb
            3'b001: load_val = {{48{sh[15]}}, sh[15:0]};   // lh
            3'b010: load_val = {{32{sh[31]}}, sh[31:0]};   // lw
            3'b011: load_val = sh;                         // ld
            3'b100: load_val = {56'd0, sh[7:0]};           // lbu
            3'b101: load_val = {48'd0, sh[15:0]};          // lhu
            3'b110: load_val = {32'd0, sh[31:0]};          // lwu
            default: load_val = 64'd0;
        endcase
    end
endmodule
