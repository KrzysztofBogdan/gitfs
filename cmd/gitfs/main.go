package main

import (
	"os"

	_ "github.com/KrzysztofBogdan/gitfs/adapters/imap"
	_ "github.com/KrzysztofBogdan/gitfs/adapters/jira"

	"github.com/KrzysztofBogdan/gitfs/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
