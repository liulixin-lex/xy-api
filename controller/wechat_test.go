package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type weChatRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn weChatRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestGetWeChatIdByCodeWithClient(t *testing.T) {
	originalToken := common.WeChatServerToken
	common.WeChatServerToken = "server-token"
	t.Cleanup(func() { common.WeChatServerToken = originalToken })

	baseURL, err := url.Parse("https://wechat.example.com/gateway")
	require.NoError(t, err)
	client := &http.Client{Transport: weChatRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "wechat.example.com", request.URL.Host)
		assert.Equal(t, "/gateway/api/wechat/user", request.URL.Path)
		assert.Equal(t, "a&b", request.URL.Query().Get("code"))
		assert.Equal(t, "server-token", request.Header.Get("Authorization"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"success":true,"data":"wx-user-id"}`)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}

	wechatID, err := getWeChatIdByCodeWithClient("a&b", baseURL, client)
	require.NoError(t, err)
	assert.Equal(t, "wx-user-id", wechatID)
}

func TestGetWeChatIdByCodeWithClientRejectsUnsafeResponses(t *testing.T) {
	baseURL, err := url.Parse("https://wechat.example.com")
	require.NoError(t, err)
	tests := []struct {
		name        string
		statusCode  int
		body        string
		contentSize int64
		wantErr     string
	}{
		{name: "non-success status", statusCode: http.StatusBadGateway, body: `{}`, wantErr: "HTTP 502"},
		{name: "invalid JSON", statusCode: http.StatusOK, body: `{`, wantErr: "响应无效"},
		{name: "provider failure", statusCode: http.StatusOK, body: `{"success":false,"message":"验证码无效"}`, wantErr: "验证码无效"},
		{name: "empty identity", statusCode: http.StatusOK, body: `{"success":true,"data":""}`, wantErr: "错误或已过期"},
		{name: "declared oversized", statusCode: http.StatusOK, body: `{}`, contentSize: maxWeChatLoginResponseBytes + 1, wantErr: "响应过大"},
		{name: "chunked oversized", statusCode: http.StatusOK, body: strings.Repeat("x", maxWeChatLoginResponseBytes+1), contentSize: -1, wantErr: "响应过大"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: weChatRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    test.statusCode,
					Body:          io.NopCloser(strings.NewReader(test.body)),
					ContentLength: test.contentSize,
					Header:        make(http.Header),
					Request:       request,
				}, nil
			})}

			wechatID, err := getWeChatIdByCodeWithClient("code", baseURL, client)
			require.Error(t, err)
			assert.Empty(t, wechatID)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestGetWeChatIdByCodeRejectsInvalidCodeBeforeNetwork(t *testing.T) {
	originalAddress := common.WeChatServerAddress
	common.WeChatServerAddress = ""
	t.Cleanup(func() { common.WeChatServerAddress = originalAddress })

	wechatID, err := getWeChatIdByCode("")
	require.Error(t, err)
	assert.Empty(t, wechatID)
	assert.Contains(t, err.Error(), "无效的参数")

	wechatID, err = getWeChatIdByCode(strings.Repeat("x", maxWeChatLoginCodeBytes+1))
	require.Error(t, err)
	assert.Empty(t, wechatID)
	assert.Contains(t, err.Error(), "无效的参数")
}

func TestGetWeChatIdByCodePreservesPrivateHTTPServiceCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assert.Equal(t, "/gateway/api/wechat/user", request.URL.Path)
		assert.Equal(t, "compat-code", request.URL.Query().Get("code"))
		assert.Equal(t, "server-token", request.Header.Get("Authorization"))
		response.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(response, `{"success":true,"data":"legacy-private-user"}`)
		assert.NoError(t, err)
	}))
	defer server.Close()

	originalAddress := common.WeChatServerAddress
	originalToken := common.WeChatServerToken
	common.WeChatServerAddress = server.URL + "/gateway"
	common.WeChatServerToken = "server-token"
	t.Cleanup(func() {
		common.WeChatServerAddress = originalAddress
		common.WeChatServerToken = originalToken
	})

	wechatID, err := getWeChatIdByCode("compat-code")
	require.NoError(t, err)
	assert.Equal(t, "legacy-private-user", wechatID)
}
