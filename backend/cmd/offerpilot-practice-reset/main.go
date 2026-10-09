package main

import (
	"context"
	"fmt"
	"offerpilot/backend/internal/interview"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: offerpilot-practice-reset <database path>")
		os.Exit(2)
	}
	store, err := interview.OpenSQLiteStore(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	count, err := store.ResetLegacyPractices(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Removed %d legacy practice records; other records retained.\n", count)
}
