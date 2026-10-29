//go:build !js && !wasip1 && !windows

package minirt

import "syscall"

const oNOFOLLOW = syscall.O_NOFOLLOW
