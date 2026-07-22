//go:build !linux

package main

import "fmt"

func main() {
	fmt.Println("eakd is supported only on Linux")
}
