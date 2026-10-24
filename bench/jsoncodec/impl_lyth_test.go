//go:build impl_lyth

package jsoncodec_test

import "lythbench/jsoncodec/lythimpl"

func decodeTotal(data []byte) (int64, error) { return lythimpl.DecodeTotal(data) }
func roundTrip(data []byte) ([]byte, error)  { return lythimpl.RoundTrip(data) }
