// console.v — MMIO コンソール (UART 16550 の送信部分だけを真似たもの)
//
//   +0 THR (書き込み): 1 バイト書くと 1 文字送信する → tx_valid / tx_data に 1 サイクルだけ出る
//   +5 LSR (読み出し): 0x60 (bit5 = 送信バッファ空, bit6 = 送信完了) を常に返す。
//                      zigos などのドライバは「bit5 が立つまで待ってから書く」ので、それに答える
//
// CPU から見ると、ただの「アドレス 0x1000_0000 への sb 命令」が、
// バスのアドレスデコードでこのモジュールに届き、文字になる。これが MMIO。
// tx_valid / tx_data は本物ならシリアル送信回路につながる線。ここではテストベンチが受けて表示する。
// 対応する emu のコード: emu/rv/uart.go の UART.Store / UART.Load
module console (
    input  wire        clk,
    input  wire        valid,
    input  wire        we,
    input  wire [7:0]  off,       // デバイス内オフセット (アドレスの下位 8 ビット)
    input  wire [63:0] wdata,     // バイトレーンに配置済み (アドレス +0 のバイトは [7:0])
    input  wire [7:0]  wstrb,
    output wire [63:0] rdata,
    output wire        ready,
    output reg         tx_valid,
    output reg  [7:0]  tx_data
);
    assign ready = 1'b1;          // 待ちなしで応答する

    // 読み出し: LSR はオフセット 5 → 64 ビットのワードの中ではバイトレーン 5 に置く
    assign rdata = (off[7:3] == 5'd0) ? {16'd0, 8'h60, 40'd0} : 64'd0;

    initial tx_valid = 1'b0;
    always @(posedge clk) begin
        tx_valid <= valid && we && off == 8'd0 && wstrb[0];
        tx_data  <= wdata[7:0];
    end
endmodule
