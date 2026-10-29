// bus.v — データバス: アドレスを見て、どのデバイス (スレーブ) に話を通すか決める (アドレスデコーダ)
//
//   0x0000_0000 – 0x000F_FFFF  RAM (1 MiB)
//   0x1000_0000 – 0x1000_00FF  コンソール (UART 16550 の THR/LSR)
//   それ以外                   どこにもつながっていない → すぐ ready を返し、bus_error = 1
//
// プロトコル (valid / ready):
//   コア (マスタ) が valid = 1 とアドレス・データを出す。選ばれたスレーブが ready = 1 を返した
//   サイクルの終わり (立ち上がりエッジ) で転送が成立する。ready = 0 の間、コアは同じ値を出したまま待つ。
//   読み出しデータ (rdata) は ready = 1 のサイクルで有効。
// 対応する emu のコード: emu/rv/bus.go の Bus.Load / Store (RAM か、Map したデバイスかを探す)
module bus #(
    parameter RAM_BYTES = 64'h10_0000,
    parameter CON_BASE  = 64'h1000_0000
) (
    // コア側
    input  wire        valid,
    input  wire        we,
    input  wire [63:0] addr,
    output reg  [63:0] rdata,
    output reg         ready,
    output wire        bus_error,
    // RAM 側
    output wire        ram_valid,
    input  wire [63:0] ram_rdata,
    input  wire        ram_ready,
    // コンソール側
    output wire        con_valid,
    input  wire [63:0] con_rdata,
    input  wire        con_ready
);
    // ---- アドレスデコード: 上位ビットを比べるだけの組み合わせ回路 ----
    wire sel_ram = (addr < RAM_BYTES);
    wire sel_con = (addr[63:8] == CON_BASE[63:8]);

    assign ram_valid = valid & sel_ram;
    assign con_valid = valid & sel_con;
    assign bus_error = valid & !sel_ram & !sel_con;

    // ---- 応答を選んでコアに返す ----
    always @(*) begin
        if (sel_ram)      begin rdata = ram_rdata; ready = ram_ready; end
        else if (sel_con) begin rdata = con_rdata; ready = con_ready; end
        else              begin rdata = 64'd0;     ready = 1'b1;      end
    end
endmodule
