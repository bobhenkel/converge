// Copyright © 2016 Asteris, LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"log"
	"net/http"

	"github.com/braintree/manners"

	"golang.org/x/net/context"
)

// Server whose runtime is controlled by a context for stopping gracefully
type Server struct {
	ctx context.Context
}

// New constructs and returns a server. Mux has the same meaning as in the
// standard HTTP package (that is, if nil it will use the globally registered
// handlers)
func New(ctx context.Context) *Server {
	return &Server{ctx}
}

// ListenAndServe does the same thing as the net/http equivalent, except
// using the context.
func (s *Server) ListenAndServe(addr string, handler http.Handler) error {
	server := manners.NewWithServer(&http.Server{Addr: addr, Handler: handler})
	go s.close(server)

	return server.ListenAndServe()
}

// ListenAndServeTLS does the same thing as the net/http equivalent, except
// using the context.
func (s *Server) ListenAndServeTLS(addr, certFile, keyFile string, handler http.Handler) error {
	server := manners.NewWithServer(&http.Server{Addr: addr, Handler: handler})
	go s.close(server)

	return server.ListenAndServeTLS(certFile, keyFile)
}

func (s *Server) close(server *manners.GracefulServer) {
	<-s.ctx.Done()
	log.Println("[INFO] gracefully stopping server")
	server.BlockingClose()
	log.Println("[INFO] server stopped")
}
