//go:build !qualificationhooks

package main

import "net/http"

func registerOptionalRoutes(*http.ServeMux, *server) {}
