package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"alliance/internal/sandbox"
)

func main() {
	inputs := flag.String("inputs", "", "directory containing main.py and its input files")
	outputs := flag.String("outputs", "", "new directory to receive the task's files")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := sandbox.Run(ctx, *inputs, *outputs); err != nil {
		log.Fatal(err)
	}
}
