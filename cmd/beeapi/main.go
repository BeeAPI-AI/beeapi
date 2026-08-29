package main

import (
	"context"
	"fmt"
	"os"

	"github.com/BeeAPI-AI/beeapi/internal/app"
)

var version = "dev"

func main() {
	if err := app.Run(context.Background(), os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, app.ErrorPrefix(), err)
		os.Exit(1)
	}
}
