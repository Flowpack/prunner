package test

import "io"

type writeCloser struct {
	io.Writer
}

func (wc *writeCloser) Close() error {
	return nil
}
