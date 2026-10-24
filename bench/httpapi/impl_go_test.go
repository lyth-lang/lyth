//go:build !impl_lyth

package httpapi_test

import (
	"net/http"

	"lythbench/httpapi/goimpl"
)

var handler http.HandlerFunc = goimpl.HandleOrder
