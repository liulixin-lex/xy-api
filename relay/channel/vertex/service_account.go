package vertex

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/bytedance/gopkg/cache/asynccache"
	"github.com/golang-jwt/jwt/v5"

	"fmt"
	"time"
)

const vertexAccessTokenResponseMaxBytes = 1 << 20

type Credentials struct {
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	ClientID     string `json:"client_id"`
}

var Cache = asynccache.NewAsyncCache(asynccache.Options{
	RefreshDuration: time.Minute * 35,
	EnableExpire:    true,
	ExpireDuration:  time.Minute * 30,
	Fetcher: func(key string) (interface{}, error) {
		return nil, errors.New("not found")
	},
})

var vertexTokenHTTPClientFactory = service.GetStatefulFetchHTTPClient

func getAccessToken(ctx context.Context, a *Adaptor, info *relaycommon.RelayInfo, beforeSend func() error) (string, error) {
	cacheKey, err := vertexAccessTokenCacheKey(info)
	if err != nil {
		return "", err
	}
	val, err := Cache.Get(cacheKey)
	if err == nil {
		if token, ok := val.(string); ok && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	signedJWT, err := createSignedJWT(a.AccountCredentials.ClientEmail, a.AccountCredentials.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to create signed JWT: %w", err)
	}
	newToken, err := exchangeJwtForAccessToken(ctx, signedJWT, info, beforeSend)
	if err != nil {
		return "", fmt.Errorf("failed to exchange JWT for access token: %w", err)
	}
	if err := Cache.SetDefault(cacheKey, newToken); err {
		return newToken, nil
	}
	return newToken, nil
}

func vertexAccessTokenCacheKey(info *relaycommon.RelayInfo) (string, error) {
	if info == nil || info.ChannelId <= 0 {
		return "", errors.New("Vertex channel identity is unavailable")
	}
	if info.RoutingCredentialID > 0 {
		return fmt.Sprintf("access-token-channel-%d-credential-%d", info.ChannelId, info.RoutingCredentialID), nil
	}
	if info.ChannelIsMultiKey {
		return "", errors.New("Vertex multi-key credential identity is unavailable")
	}
	return fmt.Sprintf("access-token-channel-%d-created-%d", info.ChannelId, info.ChannelCreateTime), nil
}

func createSignedJWT(email, privateKeyPEM string) (string, error) {

	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "-----BEGIN PRIVATE KEY-----", "")
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "-----END PRIVATE KEY-----", "")
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "\r", "")
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "\n", "")
	privateKeyPEM = strings.ReplaceAll(privateKeyPEM, "\\n", "")

	block, _ := pem.Decode([]byte("-----BEGIN PRIVATE KEY-----\n" + privateKeyPEM + "\n-----END PRIVATE KEY-----"))
	if block == nil {
		return "", fmt.Errorf("failed to parse PEM block containing the private key")
	}

	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}

	rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("not an RSA private key")
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   email,
		"scope": "https://www.googleapis.com/auth/cloud-platform",
		"aud":   "https://www.googleapis.com/oauth2/v4/token",
		"exp":   now.Add(time.Minute * 35).Unix(),
		"iat":   now.Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedToken, err := token.SignedString(rsaPrivateKey)
	if err != nil {
		return "", err
	}

	return signedToken, nil
}

func exchangeJwtForAccessToken(
	ctx context.Context,
	signedJWT string,
	info *relaycommon.RelayInfo,
	beforeSend func() error,
) (string, error) {
	if info == nil {
		return "", errors.New("Vertex relay info is unavailable")
	}
	return exchangeJwtForAccessTokenWithProxy(ctx, signedJWT, info.ChannelSetting.Proxy, beforeSend)
}

func AcquireAccessToken(ctx context.Context, creds Credentials, proxy string) (string, error) {
	return acquireAccessToken(ctx, creds, proxy, nil)
}

func AcquireAccessTokenForRelay(
	ctx context.Context,
	creds Credentials,
	proxy string,
	beforeSend func() error,
) (string, error) {
	return acquireAccessToken(ctx, creds, proxy, beforeSend)
}

func acquireAccessToken(ctx context.Context, creds Credentials, proxy string, beforeSend func() error) (string, error) {
	signedJWT, err := createSignedJWT(creds.ClientEmail, creds.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to create signed JWT: %w", err)
	}
	return exchangeJwtForAccessTokenWithProxy(ctx, signedJWT, proxy, beforeSend)
}

func exchangeJwtForAccessTokenWithProxy(
	ctx context.Context,
	signedJWT string,
	proxy string,
	beforeSend func() error,
) (string, error) {
	authURL := "https://www.googleapis.com/oauth2/v4/token"
	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	data.Set("assertion", signedJWT)

	client, err := vertexTokenHTTPClientFactory(proxy)
	if err != nil {
		return "", fmt.Errorf("new stateful token client failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return "", fmt.Errorf("mark Vertex token request sent: %w", err)
		}
	}
	resp, err := service.DoStatefulFetch(client, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > vertexAccessTokenResponseMaxBytes {
		return "", errors.New("Vertex token response is too large")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, vertexAccessTokenResponseMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read Vertex token response: %w", err)
	}
	if len(body) > vertexAccessTokenResponseMaxBytes {
		return "", errors.New("Vertex token response is too large")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Vertex token endpoint returned status %d", resp.StatusCode)
	}
	var result map[string]interface{}
	if err := common.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode Vertex token response: %w", err)
	}

	if accessToken, ok := result["access_token"].(string); ok && strings.TrimSpace(accessToken) != "" {
		return accessToken, nil
	}
	return "", errors.New("Vertex token response did not contain an access token")
}
