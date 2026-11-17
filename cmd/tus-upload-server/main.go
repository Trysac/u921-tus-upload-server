package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	tus "github.com/Trysac/u921-tus-upload-server"
)

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run parses arguments and starts the server.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tus-upload-server", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", "127.0.0.1:1080", "HTTP address to listen on")
	dir := fs.String("dir", "./uploads", "Directory to store uploads")
	maxSize := fs.Int64("max-size", tus.DefaultMaxSize, "Maximum upload size in bytes")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	store, err := tus.NewDirStore(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "Error opening dir store: %v\n", err)
		return 1
	}

	h, err := tus.NewHandler(tus.Config{
		BasePath: "/files/",
		Store:    store,
		MaxSize:  *maxSize,
		OnEvent: func(e tus.Event) {
			fmt.Fprintf(stdout, "[%s] id=%s offset=%d\n", e.Kind, e.ID, e.Offset)
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error creating handler: %v\n", err)
		return 1
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Fprintf(stdout, "tus-upload-server listening on %s (storing in %s)\n", *addr, *dir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(stderr, "Server error: %v\n", err)
		return 1
	}
	return 0
}
