package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PublicRetainedProductID returns a deterministic identifier that does not
// directly serialize the integration's product value and cannot be decoded
// from the token itself. It is not an authorization secret or a canonical
// payment route ID: callers must resolve it only against the current validated
// server-side product catalog.
func PublicRetainedProductID(integration, productID string) string {
	return publicRetainedSelectionID("product", integration, productID)
}

// PublicRetainedOptionID returns a deterministic identifier that does not
// directly serialize a retained gateway option. It is not an authorization
// secret and must be resolved only against the current validated server-side
// option catalog. Its namespace is distinct from routes and products.
func PublicRetainedOptionID(integration, optionIdentity string) string {
	return publicRetainedSelectionID("option", integration, optionIdentity)
}

func publicRetainedSelectionID(kind, integration, identity string) string {
	integration = strings.ToLower(strings.TrimSpace(integration))
	identity = strings.TrimSpace(identity)
	if integration == "" || identity == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte("new-api-public-payment-selection-v1"))
	_, _ = mac.Write([]byte(kind + "\x00" + integration + "\x00" + identity))
	digest := mac.Sum(nil)
	return kind + "_" + hex.EncodeToString(digest[:12])
}
