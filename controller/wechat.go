package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

type wechatLoginResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

const (
	maxWeChatLoginCodeBytes     = 1024
	maxWeChatLoginResponseBytes = 64 * 1024
)

var weChatServiceClientCache service.PinnedServiceClientCache

func getWeChatIdByCode(code string) (string, error) {
	if code == "" || len(code) > maxWeChatLoginCodeBytes {
		return "", errors.New("无效的参数")
	}
	baseURL, client, err := weChatServiceClientCache.Get(common.WeChatServerAddress, 5*time.Second)
	if err != nil {
		return "", fmt.Errorf("微信登录服务配置无效: %w", err)
	}
	return getWeChatIdByCodeWithClient(code, baseURL, client)
}

func getWeChatIdByCodeWithClient(code string, baseURL *url.URL, client *http.Client) (string, error) {
	endpoint, err := url.JoinPath(baseURL.String(), "api", "wechat", "user")
	if err != nil {
		return "", errors.New("微信登录服务地址无效")
	}
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return "", errors.New("微信登录服务地址无效")
	}
	query := endpointURL.Query()
	query.Set("code", code)
	endpointURL.RawQuery = query.Encode()

	req, err := http.NewRequest(http.MethodGet, endpointURL.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", common.WeChatServerToken)
	// The client pins the operator-configured scheme, host, and port and rejects
	// redirects, so the user-controlled code cannot change the request origin.
	// codeql[go/request-forgery]
	httpResponse, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("微信登录服务返回异常状态: HTTP %d", httpResponse.StatusCode)
	}
	if httpResponse.ContentLength > maxWeChatLoginResponseBytes {
		return "", errors.New("微信登录服务响应过大")
	}
	body, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxWeChatLoginResponseBytes+1))
	if err != nil {
		return "", errors.New("读取微信登录服务响应失败")
	}
	if len(body) > maxWeChatLoginResponseBytes {
		return "", errors.New("微信登录服务响应过大")
	}
	var res wechatLoginResponse
	if err := common.Unmarshal(body, &res); err != nil {
		return "", errors.New("微信登录服务响应无效")
	}
	if !res.Success {
		if res.Message == "" {
			return "", errors.New("微信登录验证失败")
		}
		return "", errors.New(res.Message)
	}
	if res.Data == "" {
		return "", errors.New("验证码错误或已过期")
	}
	return res.Data, nil
}

func WeChatAuth(c *gin.Context) {
	if !common.WeChatAuthEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "管理员未开启通过微信登录以及注册",
			"success": false,
		})
		return
	}
	code := c.Query("code")
	wechatId, err := getWeChatIdByCode(code)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": err.Error(),
			"success": false,
		})
		return
	}
	user := model.User{
		WeChatId: wechatId,
	}
	if model.IsWeChatIdAlreadyTaken(wechatId) {
		err := user.FillUserByWeChatId()
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
		if user.Id == 0 {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "用户已注销",
			})
			return
		}
	} else {
		if common.RegisterEnabled {
			user.Username = "wechat_" + strconv.Itoa(model.GetMaxUserId()+1)
			user.DisplayName = "WeChat User"
			user.Role = common.RoleCommonUser
			user.Status = common.UserStatusEnabled

			if err := user.Insert(0); err != nil {
				c.JSON(http.StatusOK, gin.H{
					"success": false,
					"message": err.Error(),
				})
				return
			}
		} else {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "管理员关闭了新用户注册",
			})
			return
		}
	}

	if user.Status != common.UserStatusEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "用户已被封禁",
			"success": false,
		})
		return
	}
	setupLogin(&user, c)
}

type wechatBindRequest struct {
	Code string `json:"code"`
}

func WeChatBind(c *gin.Context) {
	if !common.WeChatAuthEnabled {
		c.JSON(http.StatusOK, gin.H{
			"message": "管理员未开启通过微信登录以及注册",
			"success": false,
		})
		return
	}
	var req wechatBindRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "无效的请求",
		})
		return
	}
	code := req.Code
	wechatId, err := getWeChatIdByCode(code)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": err.Error(),
			"success": false,
		})
		return
	}
	if model.IsWeChatIdAlreadyTaken(wechatId) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该微信账号已被绑定",
		})
		return
	}
	session := sessions.Default(c)
	id := session.Get("id")
	user := model.User{
		Id: id.(int),
	}
	err = user.FillUserById()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	user.WeChatId = wechatId
	err = user.Update(false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}
