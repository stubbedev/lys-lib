package main

import (
	"flag"
	"strings"
)

func main() {
	var inputFile, outputFile, reuse string

	flag.StringVar(&inputFile, "i", "", "input xml file name")
	flag.StringVar(&outputFile, "o", "", "output go file name; the input file name with .go appended when empty")
	flag.StringVar(&reuse, "reuse", "", "comma-separated interfaces whose proxies delete_id hands back to their constructor")

	flag.Parse()

	reused := map[string]bool{}
	for _, name := range strings.Split(reuse, ",") {
		if name != "" {
			reused[name] = true
		}
	}
	if outputFile == "" {
		outputFile = inputFile + ".go"
	}
	xmlProcess(inputFile, outputFile, reused)
}
