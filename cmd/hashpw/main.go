// Command hashpw prints the argon2id PHC hash of a password, for use as the
// OWNER_PASSWORD_HASH environment variable. Usage:
//
//	go run ./cmd/hashpw            # prompts; reads the password from stdin
//	echo -n 'secret' | go run ./cmd/hashpw
//
// The password itself is never printed or logged.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/alexedwards/argon2id"
)

func main() {
	fmt.Fprint(os.Stderr, "password: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		fmt.Fprintln(os.Stderr, "hashpw: empty password")
		os.Exit(1)
	}

	hash, err := argon2id.CreateHash(pw, argon2id.DefaultParams)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hashpw:", err)
		os.Exit(1)
	}
	fmt.Println(hash)
}
