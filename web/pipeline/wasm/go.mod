module pipelineweb

go 1.27

require (
	minicc v0.0.0
	rvasm v0.0.0
	rvcc v0.0.0
	rvemu v0.0.0
)

replace (
	minicc => ../../../compiler
	rvasm => ../../../asm
	rvcc => ../../../cc
	rvemu => ../../../emu
)
