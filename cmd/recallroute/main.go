// Command recallroute runs the RecallRoute HTTP server.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "recallroute:", err)
		os.Exit(1)
	}
}

func run() error {
	fmt.Println("recallroute: not wired up yet")
	return nil
}
