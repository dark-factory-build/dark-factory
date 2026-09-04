//go:build !darwin && !linux

package cloudflareadmin

import (
	"fmt"
	"os"
)

func readPrivateRegularFile(string, int64) ([]byte, error) {
	return nil, fmt.Errorf("Cloudflare administration is unsupported on this platform")
}

func acquirePublishLock(string) (*os.File, error) {
	return nil, fmt.Errorf("Cloudflare administration is unsupported on this platform")
}
