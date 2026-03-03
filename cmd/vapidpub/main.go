package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	pemPath := flag.String("pem", "", "path to VAPID EC private key PEM (prime256v1/P-256)")
	flag.Parse()

	if *pemPath == "" {
		log.Fatal("missing required -pem flag")
	}

	b, err := os.ReadFile(*pemPath)
	if err != nil {
		log.Fatalf("read pem file: %v", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		log.Fatal("no PEM block found")
	}

	ecKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		parsed, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			log.Fatalf("parse private key: %v", err)
		}
		k, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			log.Fatal("PEM key is not ECDSA")
		}
		ecKey = k
	}

	if ecKey.Curve == nil || ecKey.Curve.Params().Name != "P-256" {
		log.Fatal("expected P-256 key")
	}

	x := leftPad(ecKey.PublicKey.X.Bytes(), 32)
	y := leftPad(ecKey.PublicKey.Y.Bytes(), 32)
	uncompressed := append([]byte{0x04}, append(x, y...)...)
	pubB64 := base64.RawURLEncoding.EncodeToString(uncompressed)

	privBytes := leftPad(ecKey.D.Bytes(), 32)
	privB64 := base64.RawURLEncoding.EncodeToString(privBytes)

	fmt.Printf("VAPID_PUBLIC_B64=%s\n", pubB64)
	fmt.Printf("VAPID_PRIVATE_B64=%s\n", privB64)
}

func leftPad(in []byte, size int) []byte {
	if len(in) >= size {
		return in
	}
	out := make([]byte, size)
	copy(out[size-len(in):], in)
	return out
}
