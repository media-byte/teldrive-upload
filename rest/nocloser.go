package rest

import "io"

type noClose struct {
	in io.Reader
}

func (nc noClose) Read(p []byte) (n int, err error) {
	return nc.in.Read(p)
}

func NoCloser(in io.Reader) io.Reader {
	if in == nil {
		return in
	}

	if _, canClose := in.(io.Closer); !canClose {
		return in
	}
	return noClose{in: in}
}
