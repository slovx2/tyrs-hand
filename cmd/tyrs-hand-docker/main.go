package main

import (
	"context"
	"os"

	"github.com/slovx2/tyrs-hand/internal/hostdocker"
)

func main() {
	os.Exit(hostdocker.RunWrapper(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
