// Command releasekey manages the ed25519 key that signs lmm releases (#569).
//
//	go run ./tools/releasekey generate -out <file>
//	    Writes a new private key to <file> (mode 0600, never overwritten) and
//	    prints its public key. Keep <file> backed up offline, store it as the
//	    LMM_RELEASE_SIGNING_KEY Actions secret, then delete the working copy.
//	go run ./tools/releasekey public -key <file>
//	    Prints the public key of an existing private key.
//	go run ./tools/releasekey sign -in checksums.txt -out checksums.txt.sig
//	    Signs with the private key in $LMM_RELEASE_SIGNING_KEY; the release
//	    workflow runs this.
//	go run ./tools/releasekey verify -pub <base64> -in checksums.txt -sig checksums.txt.sig
//
// Standard library only; see internal/releasesig.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"
)

// envKey names the environment variable sign reads the private key from.
const envKey = "LMM_RELEASE_SIGNING_KEY"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "releasekey:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: releasekey generate|public|sign|verify [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "generate":
		out := fs.String("out", "", "file to write the private key to")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return generate(*out, stdout)
	case "public":
		key := fs.String("key", "", "private key file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		priv, err := os.ReadFile(*key)
		if err != nil {
			return err
		}
		pub, err := releasesig.PublicKeyOf(string(priv))
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, pub)
		return err
	case "sign":
		in := fs.String("in", "", "file to sign")
		out := fs.String("out", "", "file to write the signature to")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		priv := os.Getenv(envKey)
		if strings.TrimSpace(priv) == "" {
			return fmt.Errorf("$%s is not set", envKey)
		}
		data, err := os.ReadFile(*in)
		if err != nil {
			return err
		}
		sig, err := releasesig.Sign(priv, data)
		if err != nil {
			return err
		}
		return os.WriteFile(*out, []byte(sig+"\n"), 0o644)
	case "verify":
		pub := fs.String("pub", "", "base64 public key")
		in := fs.String("in", "", "signed file")
		sigFile := fs.String("sig", "", "signature file")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		data, err := os.ReadFile(*in)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(*sigFile)
		if err != nil {
			return err
		}
		if err := releasesig.Verify(*pub, data, string(sig)); err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, "OK")
		return err
	default:
		return fmt.Errorf("unknown command %q: want generate, public, sign or verify", args[0])
	}
}

func generate(out string, stdout io.Writer) error {
	if out == "" {
		return errors.New("generate needs -out <file>")
	}
	priv, pub, err := releasesig.Generate()
	if err != nil {
		return err
	}
	// O_EXCL: never overwrite a key that may be the only copy.
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(priv + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, `Private key written to %s (mode 0600).

Public key (compile into lmm; safe to share):
%s

Next:
  1. Back %s up somewhere offline (a password manager is fine).
  2. gh secret set %s < %s
  3. Delete the working copy once it is backed up.
`, out, pub, out, envKey, out)
	return err
}
