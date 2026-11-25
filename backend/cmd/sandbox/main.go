package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/sandbox"
)

func main() {
	inputs := flag.String("inputs", "", "directory containing main.py and its input files")
	outputs := flag.String("outputs", "", "new directory to receive the task's files")
	run := flag.String("run", "", "completed run that receives the output files")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	address := os.Getenv("DATABASE_URL")
	if *run != "" && (*outputs == "" || address == "") {
		log.Fatal("provide --outputs and DATABASE_URL to register artifacts")
	}
	if address == "" {
		if err := sandbox.Run(ctx, *inputs, *outputs); err != nil {
			log.Fatal(err)
		}
		return
	}
	db, err := database.Open(ctx, address)
	if err != nil {
		log.Fatal("could not open the Alliance database")
	}
	defer db.Close()
	if *inputs != "" || *run == "" {
		if err := (sandbox.Pool{DB: db}).Run(ctx, *inputs, *outputs); err != nil {
			log.Fatal(err)
		}
	}
	if *run != "" {
		artifacts, err := sandbox.ImportArtifacts(ctx, db, knowledge.ConfiguredObjects(), *run, *outputs)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(artifacts); err != nil {
			log.Fatal(err)
		}
	}
}
