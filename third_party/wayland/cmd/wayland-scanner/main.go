package main

import (
	"flag"
	"strings"
)

func main() {
	var inputFile, reuse string

	flag.StringVar(&inputFile, "i", "", "input xml file name")
	flag.StringVar(&reuse, "reuse", "", "comma-separated interfaces whose proxies delete_id hands back to their constructor")

	flag.Parse()

	reused := map[string]bool{}
	for _, name := range strings.Split(reuse, ",") {
		if name != "" {
			reused[name] = true
		}
	}
	xmlProcess(inputFile, inputFile+".go", reused)
}
