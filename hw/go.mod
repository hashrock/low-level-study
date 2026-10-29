module rvhw

go 1.27

require (
	rvasm v0.0.0
	rvcc v0.0.0
	rvemu v0.0.0
)

replace (
	rvasm => ../asm
	rvcc => ../cc
	rvemu => ../emu
)
