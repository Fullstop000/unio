package claude

import (
	"os"
	"strings"
)

// defaultHomeDir returns the user's home directory.
func defaultHomeDir() (string, error) {
	return os.UserHomeDir()
}

// defaultFileExists reports whether path exists and is a regular file.
func defaultFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// encodeCwd maps a working directory to Claude's on-disk project folder name.
// Claude encodes the absolute cwd by replacing path separators (and other
// non-alphanumeric characters) with dashes, e.g. /Users/x/repo ->
// -Users-x-repo. We approximate that encoding for the liveness guard; a miss
// only means we skip --resume and start fresh, which is safe.
func encodeCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
