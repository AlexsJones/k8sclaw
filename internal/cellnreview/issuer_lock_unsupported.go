//go:build !linux && !darwin

package cellnreview

import "fmt"

func lockIssuer(string) (func(), error) {
	return nil, fmt.Errorf("host issuer locking unsupported on this platform")
}
