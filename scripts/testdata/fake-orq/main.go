package main

import (
	"fmt"
	"os"
	"strconv"
)

var version = "1.0.0"
var versionExit = "0"
var setupExit = "0"

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "--version":
		fmt.Println("orq " + version)
		exit(versionExit)
	case "--help":
		fmt.Println("  setup  Configure orq")
	case "setup":
		exit(setupExit)
	default:
		os.Exit(2)
	}
}

func exit(code string) {
	n, err := strconv.Atoi(code)
	if err != nil {
		os.Exit(2)
	}
	os.Exit(n)
}
