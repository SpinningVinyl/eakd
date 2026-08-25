// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !linux

package main

import (
	"flag"
	"fmt"

	"eak/internal/buildinfo"
)

func main() {
	showVersion := flag.Bool("version", false, "display version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("eakc %s\n", buildinfo.Version)
		return
	}
	fmt.Println("eakc is supported only on Linux")
}
