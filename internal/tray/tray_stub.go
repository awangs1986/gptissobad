//go:build !linux

package tray

func Run(onActivate func(), status func() (bool, bool, string)) error {
	select {}
}

func Available() bool { return false }

func IconPixmap(size int) (int32, int32, []byte) { return 0, 0, nil }

func IconPixmapFor(size int, ok bool) (int32, int32, []byte) { return 0, 0, nil }

func IconPixmapDim(size int) (int32, int32, []byte) { return 0, 0, nil }
