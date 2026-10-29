//go:build !(js && wasm)

package main

import "fmt"

// 通常のビルドでは何もしない (session のロジックを go test で確かめるためのスタブ)。
func main() { fmt.Println("build with GOOS=js GOARCH=wasm (see web/os/build.sh)") }
