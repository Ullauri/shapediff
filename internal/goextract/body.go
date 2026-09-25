package goextract

import (
	"crypto/sha256"
	"encoding/hex"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/Ullauri/shapediff/internal/graph"
)

// bodyHashes computes the four body fields of spec section 6.1 from the
// source bytes of a function body. recvName is the receiver type name for
// methods ("" for functions).
type bodyHashes struct {
	Fingerprint     string
	FingerprintRecv string
	BodyHash        string
	Tokens          int
	Sketch          []uint32
}

const recvPlaceholder = "$RECV"

func hashBody(src []byte, recvName string) bodyHashes {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	s.Init(file, src, nil, 0) // no ScanComments: comments are dropped

	var norm, raw, recv []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON || tok == token.ILLEGAL {
			continue // automatic and explicit semicolons alike are formatting
		}
		var n, r string
		switch tok {
		case token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING:
			n, r = "$"+tok.String(), lit
		case token.IDENT:
			n, r = lit, lit
		default:
			n, r = tok.String(), tok.String()
		}
		norm = append(norm, n)
		raw = append(raw, r)
		if recvName != "" && tok == token.IDENT && lit == recvName {
			recv = append(recv, recvPlaceholder)
		} else {
			recv = append(recv, n)
		}
	}
	h := bodyHashes{
		Fingerprint: sum(norm),
		BodyHash:    sum(raw),
		Tokens:      len(norm),
		Sketch:      graph.MinHash(norm),
	}
	if recvName != "" {
		h.FingerprintRecv = sum(recv)
	}
	return h
}

func sum(tokens []string) string {
	d := sha256.Sum256([]byte(strings.Join(tokens, "\x00")))
	return hex.EncodeToString(d[:])
}
