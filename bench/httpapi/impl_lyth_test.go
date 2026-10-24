//go:build impl_lyth

package httpapi_test

import (
	"net/http"

	"lythbench/httpapi/lythimpl"
)

var handler http.HandlerFunc = lythimpl.HandleOrder
