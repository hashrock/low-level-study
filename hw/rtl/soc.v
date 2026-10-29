// soc.v — トップレベル: コア + バス + RAM + コンソールを配線する
// (コアは単一サイクル版 core.v、`define PIPE のときは 5 段パイプライン版 core_pipe.v)
//
//            i_addr/i_data (命令ポート)
//   ┌──────┐ ─────────────────────────────┐
//   │ core │                              ▼
//   │      │ d_* ──▶ ┌─────┐ ram_* ──▶ ┌─────┐
//   └──────┘ ◀────── │ bus │ ◀──────── │ ram │
//      │ecall        │     │ con_* ──▶ ┌─────────┐ tx_valid/tx_data ──▶ (テストベンチが表示)
//      ▼             └─────┘ ◀──────── │ console │
//   (テストベンチ = 「OS」)              └─────────┘
module soc (
    input  wire        clk,
    input  wire        rst,
    input  wire [63:0] reset_pc,
    input  wire [3:0]  ram_wait,
    // システムコール
    output wire        ecall,
    input  wire        sys_done,
    input  wire        sys_we,
    input  wire [63:0] sys_ret,
    // コンソール出力
    output wire        tx_valid,
    output wire [7:0]  tx_data,
    // 観測用 (core.v の c_* と同じ)
    output wire        commit,
    output wire [63:0] c_pc,
    output wire [31:0] c_inst,
    output wire [4:0]  c_rd,
    output wire [63:0] c_val,
    output wire [63:0] c_saddr,
    output wire [3:0]  c_ssize,
    output wire [63:0] c_sdata,
    output wire        illegal,
    output wire        misaligned,
    output wire        bus_error
);
    wire [63:0] i_addr;
    wire [31:0] i_data;
    wire        d_valid, d_we, d_ready;
    wire [63:0] d_addr, d_wdata, d_rdata;
    wire [7:0]  d_wstrb;

    // `define PIPE (tb/tb_pipe.v) なら 5 段パイプライン版のコアを使う。ポートは同じ
`ifdef PIPE
    core_pipe core (
`else
    core core (
`endif
        .clk(clk), .rst(rst), .reset_pc(reset_pc),
        .i_addr(i_addr), .i_data(i_data),
        .d_valid(d_valid), .d_we(d_we), .d_addr(d_addr), .d_wdata(d_wdata), .d_wstrb(d_wstrb),
        .d_rdata(d_rdata), .d_ready(d_ready),
        .ecall_o(ecall), .sys_done(sys_done), .sys_we(sys_we), .sys_ret(sys_ret),
        .commit(commit), .c_pc(c_pc), .c_inst(c_inst), .c_rd(c_rd), .c_val(c_val),
        .c_saddr(c_saddr), .c_ssize(c_ssize), .c_sdata(c_sdata),
        .illegal_o(illegal), .misaligned_o(misaligned));

    wire        ram_valid, ram_ready, con_valid, con_ready;
    wire [63:0] ram_rdata, con_rdata;

    bus u_bus (
        .valid(d_valid), .we(d_we), .addr(d_addr), .rdata(d_rdata), .ready(d_ready),
        .bus_error(bus_error),
        .ram_valid(ram_valid), .ram_rdata(ram_rdata), .ram_ready(ram_ready),
        .con_valid(con_valid), .con_rdata(con_rdata), .con_ready(con_ready));

    ram u_ram (
        .clk(clk), .wait_cycles(ram_wait),
        .i_addr(i_addr), .i_data(i_data),
        .d_valid(ram_valid), .d_we(d_we), .d_addr(d_addr), .d_wdata(d_wdata), .d_wstrb(d_wstrb),
        .d_rdata(ram_rdata), .d_ready(ram_ready));

    console u_con (
        .clk(clk), .valid(con_valid), .we(d_we), .off(d_addr[7:0]), .wdata(d_wdata),
        .wstrb(d_wstrb), .rdata(con_rdata), .ready(con_ready),
        .tx_valid(tx_valid), .tx_data(tx_data));
endmodule
