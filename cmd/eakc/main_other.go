//go:build !linux

package main

import "fmt"

func main() {
	fmt.Println("eakc is supported only on Linux")
}
