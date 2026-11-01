//go:build !windows

package focus

func Foreground() (Info, error) {
	return Info{}, nil
}
