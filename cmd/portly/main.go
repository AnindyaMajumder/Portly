// Portly shows what uses local ports and stops it safely.
package main

import "fmt"

var version = "dev"

// main prints the version until the command line exists.
func main() {
	fmt.Println("portly", version)
}
