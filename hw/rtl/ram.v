// ram.v — 統一メモリ (命令とデータが同じ RAM)。64 ビット幅 × WORDS ワード
//
// ポートが 2 つある (デュアルポート RAM):
//   命令ポート: 組み合わせ読み出しのみ。pc を入れると同じサイクル内に命令語が出る (待ちなし)
//   データポート: valid / ready のハンドシェイク付き。書き込みはクロックの立ち上がりで
//                 wstrb の立っているバイトだけ書く
//
// WAIT (wait_cycles) を 1 以上にすると、データポートは valid を受けてから
// その回数だけ ready = 0 を返し続ける (遅い DRAM の真似)。その間コアはストールする。
//
// 単一サイクル CPU は「アドレスを出したその同じサイクルで読めるメモリ」を前提にしている。
// 本物の SRAM はクロック同期で読むのが普通なので、実チップではこうはいかない
// (だからパイプライン CPU はメモリアクセスに専用のステージを設ける)。
// 対応する emu のコード: emu/rv/bus.go の Bus.RAM と Load / Store
module ram #(
    parameter WORDS = 131072                 // 131072 × 8 バイト = 1 MiB (loader.RAMSize と一致)
) (
    input  wire        clk,
    input  wire [3:0]  wait_cycles,          // データポートの待ちサイクル数 (0 = 同じサイクルで応答)

    // 命令ポート
    input  wire [63:0] i_addr,
    output wire [31:0] i_data,

    // データポート
    input  wire        d_valid,
    input  wire        d_we,
    input  wire [63:0] d_addr,
    input  wire [63:0] d_wdata,
    input  wire [7:0]  d_wstrb,
    output wire [63:0] d_rdata,
    output wire        d_ready
);
    reg [63:0] mem [0:WORDS-1];

    // ---- 初期値: ローダ (tools/load) が作った hex ファイルを読む ----
    // (シミュレーション専用。本物なら ROM やブートローダが RAM に書き込む部分)
    reg [1023:0] hexfile;
    integer i;
    initial begin
        for (i = 0; i < WORDS; i = i + 1) mem[i] = 64'd0;
        if ($value$plusargs("hex=%s", hexfile))
            $readmemh(hexfile, mem);
    end

    // ---- 命令ポート: 64 ビットのワードから pc[2] で上下どちらかの 32 ビットを選ぶ ----
    wire [63:0] i_word = mem[i_addr[19:3]];
    assign i_data = i_addr[2] ? i_word[63:32] : i_word[31:0];

    // ---- データポート ----
    wire [16:0] widx = d_addr[19:3];         // ワード番号 (バイトアドレス / 8)
    assign d_rdata = mem[widx];              // 組み合わせ読み出し

    // 待ちサイクルのカウンタ: valid の間 1 ずつ増え、wait_cycles に達したら ready
    reg [3:0] cnt;
    initial cnt = 0;
    assign d_ready = (cnt == wait_cycles);
    always @(posedge clk) begin
        if (d_valid && !d_ready) cnt <= cnt + 4'd1;
        else                     cnt <= 4'd0;     // 応答した (または誰もアクセスしていない)
    end

    // 書き込み: ready のサイクルの終わり (立ち上がりエッジ) で、wstrb のバイトだけ
    integer b;
    always @(posedge clk) begin
        if (d_valid && d_we && d_ready)
            for (b = 0; b < 8; b = b + 1)
                if (d_wstrb[b]) mem[widx][b*8 +: 8] <= d_wdata[b*8 +: 8];
    end
endmodule
