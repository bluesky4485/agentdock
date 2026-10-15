//go:build !windows

package installer

import "os"

func copyWindowsStableFile(source, target string, mode os.FileMode) error {
	return copyTree(source, target, mode)
}
