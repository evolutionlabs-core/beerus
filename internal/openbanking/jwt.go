package openbanking

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid,omitempty"`
}

var b64 = base64.RawURLEncoding

func JWKOf(pub *ecdsa.PublicKey, kid string) JWK {
	size := (pub.Curve.Params().BitSize + 7) / 8
	return JWK{Kty: "EC", Crv: "P-256", X: b64.EncodeToString(pub.X.FillBytes(make([]byte, size))), Y: b64.EncodeToString(pub.Y.FillBytes(make([]byte, size))), Kid: kid}
}

func (k JWK) Public() (*ecdsa.PublicKey, error) {
	if k.Kty != "EC" || k.Crv != "P-256" {
		return nil, errors.New("only EC P-256 keys are supported")
	}
	x, err := b64.DecodeString(k.X)
	if err != nil {
		return nil, err
	}
	y, err := b64.DecodeString(k.Y)
	if err != nil {
		return nil, err
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if _, err := pub.ECDH(); err != nil {
		return nil, errors.New("the key is not on the curve")
	}
	return pub, nil
}

type rawSig struct {
	R, S *big.Int
}

func Sign(key *ecdsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	head, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": kid})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64.EncodeToString(head) + "." + b64.EncodeToString(body)
	sum := sha256.Sum256([]byte(input))
	der, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	var sig rawSig
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := append(sig.R.FillBytes(make([]byte, 32)), sig.S.FillBytes(make([]byte, 32))...)
	return input + "." + b64.EncodeToString(raw), nil
}

func Verify(token string, keys []*ecdsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a signed token")
	}
	head, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("bad header")
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(head, &h) != nil || h.Alg != "ES256" {
		return nil, errors.New("alg must be ES256")
	}
	raw, err := b64.DecodeString(parts[2])
	if err != nil || len(raw) != 64 {
		return nil, errors.New("bad signature")
	}
	der, err := asn1.Marshal(rawSig{new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	ok := false
	for _, k := range keys {
		if ecdsa.VerifyASN1(k, sum[:], der) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, errors.New("signature does not match")
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("bad claims")
	}
	claims := map[string]any{}
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, errors.New("bad claims")
	}
	return claims, nil
}
