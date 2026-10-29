package main

import "fmt"

// 例外と割り込み。
//
// 例外 (同期): 命令の実行中に起きる (不正命令, ecall, アクセス違反…)
// 割り込み (非同期): 命令の外から来る (タイマ, ソフトウェア, 外部)
//
// どちらも「trap」として同じ手順で処理される (M モードで受ける場合):
//   mepc    <- 例外を起こした命令の PC (割り込みなら次に実行するはずだった PC)
//   mcause  <- 原因番号 (割り込みなら最上位ビットが 1)
//   mtval   <- 補足情報 (不正アドレスや不正命令の値)
//   mstatus.MPP  <- 元の特権レベル
//   mstatus.MPIE <- mstatus.MIE,  mstatus.MIE <- 0 (割り込み禁止)
//   特権レベル <- M,  PC <- mtvec
// mret はこの逆をして元の場所へ戻る。

// 例外の原因番号 (mcause)
const (
	excInstMisaligned  = 0
	excInstAccessFault = 1
	excIllegalInst     = 2
	excBreakpoint      = 3
	excLoadMisaligned  = 4
	excLoadAccessFault = 5
	excStoreMisaligned = 6
	excStoreFault      = 7
	excEcallU          = 8
	excEcallS          = 9
	excEcallM          = 11
)

const interruptBit = 1 << 63

// Exception は命令実行中に起きた同期例外。
type Exception struct {
	Cause uint64
	Tval  uint64
}

func (e *Exception) Error() string {
	names := map[uint64]string{
		0: "instruction address misaligned", 1: "instruction access fault", 2: "illegal instruction",
		3: "breakpoint", 4: "load address misaligned", 5: "load access fault",
		6: "store address misaligned", 7: "store access fault",
		8: "ecall from U-mode", 9: "ecall from S-mode", 11: "ecall from M-mode",
	}
	return fmt.Sprintf("%s (cause=%d, tval=0x%x)", names[e.Cause], e.Cause, e.Tval)
}

// trap は例外/割り込みを処理し、トラップハンドラへ PC を移す。
// cause の最上位ビットが立っていれば割り込み。
func (c *CPU) trap(cause, tval uint64) {
	isInterrupt := cause&interruptBit != 0
	code := cause &^ interruptBit

	// ユーザモードランナーにはトラップハンドラ (カーネル) がいないので、ここで停止する
	if c.UserOnly {
		c.Halt(-1, fmt.Errorf("unhandled trap at pc=0x%x: %v", c.PC, &Exception{cause, tval}))
		return
	}

	// medeleg / mideleg のビットが立っていれば、U/S モードで起きた trap は S モードへ委譲する
	deleg := c.csr[csrMedeleg]
	if isInterrupt {
		deleg = c.csr[csrMideleg]
	}
	ms := c.csr[csrMstatus]

	if c.Priv <= PrivS && deleg>>code&1 != 0 {
		c.csr[csrSepc] = c.PC
		c.csr[csrScause] = cause
		c.csr[csrStval] = tval
		ms = setBit(ms, mstatusSPIE, ms&mstatusSIE != 0)
		ms = setBit(ms, mstatusSPP, c.Priv == PrivS)
		ms &^= mstatusSIE
		c.csr[csrMstatus] = ms
		c.Priv = PrivS
		c.PC = trapVector(c.csr[csrStvec], code, isInterrupt)
		return
	}

	c.csr[csrMepc] = c.PC
	c.csr[csrMcause] = cause
	c.csr[csrMtval] = tval
	ms = setBit(ms, mstatusMPIE, ms&mstatusMIE != 0)
	ms = ms&^mstatusMPP | c.Priv<<11
	ms &^= mstatusMIE
	c.csr[csrMstatus] = ms
	c.Priv = PrivM
	c.PC = trapVector(c.csr[csrMtvec], code, isInterrupt)
}

// trapVector: tvec の下位 2 ビットが 1 (Vectored) なら割り込みは base + 4*code へ飛ぶ。
func trapVector(tvec, code uint64, isInterrupt bool) uint64 {
	base := tvec &^ 3
	if tvec&3 == 1 && isInterrupt {
		return base + 4*code
	}
	return base
}

func setBit(v, mask uint64, on bool) uint64 {
	if on {
		return v | mask
	}
	return v &^ mask
}

// mret: M モードのトラップから戻る。
func (c *CPU) mret() {
	ms := c.csr[csrMstatus]
	prev := ms & mstatusMPP >> 11
	ms = setBit(ms, mstatusMIE, ms&mstatusMPIE != 0)
	ms |= mstatusMPIE
	ms &^= mstatusMPP // MPP <- U
	if prev != PrivM {
		ms &^= mstatusMPRV
	}
	c.csr[csrMstatus] = ms
	c.Priv = prev
	c.nextPC = c.csr[csrMepc]
}

// sret: S モードのトラップから戻る。
func (c *CPU) sret() {
	ms := c.csr[csrMstatus]
	prev := PrivU
	if ms&mstatusSPP != 0 {
		prev = PrivS
	}
	ms = setBit(ms, mstatusSIE, ms&mstatusSPIE != 0)
	ms |= mstatusSPIE
	ms &^= mstatusSPP
	ms &^= mstatusMPRV
	c.csr[csrMstatus] = ms
	c.Priv = prev
	c.nextPC = c.csr[csrSepc]
}

// 割り込みの優先順位 (高い順)
var irqPriority = []uint64{irqMExt, irqMSoft, irqMTimer, irqSExt, irqSSoft, irqSTimer}

// checkInterrupt は受け付け可能な割り込みがあれば trap して true を返す。
func (c *CPU) checkInterrupt() bool {
	pending := c.csr[csrMip] & c.csr[csrMie]
	if pending == 0 {
		return false
	}
	ms := c.csr[csrMstatus]
	// M レベル割り込み: 現在 M 未満なら常に有効、M なら MIE が必要
	mEnabled := c.Priv < PrivM || ms&mstatusMIE != 0
	// S レベル (委譲された) 割り込み: 現在 U なら常に、S なら SIE が必要、M なら無効
	sEnabled := c.Priv < PrivS || (c.Priv == PrivS && ms&mstatusSIE != 0)

	for _, irq := range irqPriority {
		if pending>>irq&1 == 0 {
			continue
		}
		delegated := c.csr[csrMideleg]>>irq&1 != 0
		if (!delegated && mEnabled) || (delegated && sEnabled) {
			c.trap(interruptBit|irq, 0)
			return true
		}
	}
	return false
}
