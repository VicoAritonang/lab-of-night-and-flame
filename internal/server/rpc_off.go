//go:build arl_nogrpc

package server

import "net/http"

// Built with -tags arl_nogrpc: REST and WebSocket only, no gRPC/Connect.
const rpcCompiled = false

func (s *Server) rpcHandler() http.Handler                    { return nil }
func isRPC(string) bool                                       { return false }
func (s *Server) serveRPC(http.ResponseWriter, *http.Request) {}
