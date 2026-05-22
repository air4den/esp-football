//go:build local

package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	r := run()
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
}
