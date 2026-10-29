//go:build js || wasip1 || windows

package minirt

// No O_NOFOLLOW here. OSFS is not used by the browser build (it uses MemFS,
// whose ReadFile/WriteFile refuse a final symlink themselves).
const oNOFOLLOW = 0
