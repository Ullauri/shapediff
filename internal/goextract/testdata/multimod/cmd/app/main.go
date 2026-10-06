// Command app is the root module's program; it calls into the lib module.
package main

import (
	"fmt"

	"example.com/mm/lib"
	"example.com/mm/lib/sub"
)

func main() {
	fmt.Println(lib.F() + sub.G())
}
