package jrpc2

import (
	"net"
	"net/http"
	"os"
	"syscall"
)

// Start binds the RPCHandler to the server route and starts the http server.
func (s *Service) Start() error {

	var rerr error

	if _, err := os.Stat(s.socket); !os.IsNotExist(err) {
		err := syscall.Unlink(s.socket)
		if err != nil {
			return err
		}
	}

	us, err := net.Listen("unix", s.socket)
	if err != nil {
		return err
	}

	if err = os.Chmod(
		s.socket,
		os.FileMode(s.socketPermissions),
	); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle(s.route, s)

	err = http.Serve(us, mux)
	if err != nil {
		return err
	}

	defer func() {
		err := us.Close()
		if err != nil {
			rerr = err
		}
	}()

	return rerr
}