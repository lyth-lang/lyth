//go:build !impl_lyth

package jsoncodec_test

import "lythbench/jsoncodec/goimpl"

func decodeTotal(data []byte) (int64, error) { return goimpl.DecodeTotal(data) }
func roundTrip(data []byte) ([]byte, error)  { return goimpl.RoundTrip(data) }
