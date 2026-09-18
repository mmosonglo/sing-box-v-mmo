//go:build !generate

package main

import (
	_ "github.com/sagernet/sing-box/common/autotune"
	"github.com/sagernet/sing-box/log"
)

func main() {
	if err := mainCommand.Execute(); err != nil {
		log.Fatal(err)
	}
}
