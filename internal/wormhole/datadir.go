package wormhole

import (
	"os"
	"path/filepath"
)

// resolveDataDir makes state storage work everywhere: on serverless
// platforms (Vercel, AWS Lambda) only /tmp is writable, so any relative
// data dir is redirected there.
func resolveDataDir(dir string) string {
	if dir == "" || isServerless() {
		dir = filepath.Join(os.TempDir(), "wormhole-data")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func isServerless() bool {
	return os.Getenv("VERCEL") != "" || os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != ""
}

// IsServerless reports whether the process runs on a serverless platform.
func IsServerless() bool { return isServerless() }
