// Command oidcfake runs the fake OpenID provider from internal/oidcfake for
// local and browser runs. It signs in any identity, so it listens on
// loopback addresses only.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/fiatcode-gh/answerbox/internal/oidcfake"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9096", "loopback address to listen on")
	clientID := flag.String("client-id", "dev-client", "OAuth client id the provider accepts")
	clientSecret := flag.String("client-secret", "dev-secret", "OAuth client secret the provider accepts")
	redirectURL := flag.String("redirect-url", "http://localhost:8080/admin/auth/callback", "the only redirect_uri the provider accepts")
	flag.Parse()

	if !loopbackAddr(*listen) {
		fmt.Fprintln(os.Stderr, "oidcfake: refusing to listen on a non-local address")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "oidcfake:", err)
		os.Exit(1)
	}
	issuer := "http://" + ln.Addr().String()
	p := oidcfake.New(oidcfake.Config{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
		RedirectURL:  *redirectURL,
	})
	p.SetIssuer(issuer)
	fmt.Fprintf(os.Stderr, "oidcfake: fake OpenID provider at %s (signs in any identity)\n", issuer)

	srv := &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "oidcfake:", err)
		os.Exit(1)
	}
}

// loopbackAddr reports whether addr names localhost, 127.0.0.1 or ::1.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
