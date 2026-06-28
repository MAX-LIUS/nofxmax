package main

// gentoken mints a short-lived JWT for the given user id using the same auth
// package + JWT_SECRET env the server uses, so admin/maintenance API calls can
// authenticate. Prints ONLY the token to stdout. Secret is never printed.

import (
	"fmt"
	"os"

	"nofx/auth"
)

func main() {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "JWT_SECRET not set")
		os.Exit(1)
	}
	auth.SetJWTSecret(secret)
	userID := os.Getenv("GEN_USER_ID")
	email := os.Getenv("GEN_EMAIL")
	if userID == "" {
		fmt.Fprintln(os.Stderr, "GEN_USER_ID not set")
		os.Exit(1)
	}
	tok, err := auth.GenerateJWT(userID, email)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate failed:", err)
		os.Exit(1)
	}
	fmt.Print(tok)
}
