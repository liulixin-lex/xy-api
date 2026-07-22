package controller

import (
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

type paymentContinuationField struct {
	Name  string
	Value string
}

// LegacyPaymentPageRedirect lets pre-unified Epay clients keep their historic
// POST-form behavior while handing control to the durable local checkout page.
// It never waits for or exposes provider start data.
func LegacyPaymentPageRedirect(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" || len(tradeNo) > 128 {
		paymentAPIError(c, http.StatusBadRequest, "Invalid payment order")
		return
	}
	if _, err := model.GetPaymentOrderForUser(c.GetInt("id"), tradeNo); err != nil {
		paymentAPIError(c, http.StatusNotFound, "Payment order not found")
		return
	}
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "no-referrer")
	c.Redirect(http.StatusSeeOther, legacyPaymentPageURL(tradeNo))
}

type paymentContinuationPage struct {
	Lang    string
	Title   string
	Message string
	Button  string
	Action  string
	Nonce   string
	Fields  []paymentContinuationField
}

var paymentContinuationTemplate = template.Must(template.New("payment-continuation").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>{{.Title}}</title>
  <style nonce="{{.Nonce}}">
    :root{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color-scheme:light dark}
    body{margin:0;min-height:100vh;display:grid;place-items:center;background:#f7f8fa;color:#182230}
    main{width:min(28rem,calc(100vw - 2rem));box-sizing:border-box;border:1px solid #dfe3e8;border-radius:1rem;background:#fff;padding:2rem;text-align:center;box-shadow:0 1rem 3rem rgba(16,24,40,.08)}
    .mark{width:2.5rem;height:2.5rem;margin:0 auto 1rem;border:.2rem solid #d8dee7;border-top-color:#2563eb;border-radius:999px;animation:spin .8s linear infinite}
    p{margin:0 0 1rem;line-height:1.6;color:#52606d}
    button{min-height:2.75rem;border:0;border-radius:.65rem;background:#1f2937;color:#fff;padding:.65rem 1.1rem;font:inherit;font-weight:600;cursor:pointer}
    @media(prefers-color-scheme:dark){body{background:#0f141b;color:#f8fafc}main{background:#171e27;border-color:#2f3946}p{color:#b8c2cf}button{background:#e5e7eb;color:#111827}}
    @media(prefers-reduced-motion:reduce){.mark{animation:none;border-top-color:#2563eb}}
    @keyframes spin{to{transform:rotate(360deg)}}
  </style>
</head>
<body>
  <main>
    <div class="mark" aria-hidden="true"></div>
    <p role="status">{{.Message}}</p>
    <form method="post" action="{{.Action}}">
      {{range .Fields}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">{{end}}
      <button type="submit">{{.Button}}</button>
    </form>
  </main>
  <script nonce="{{.Nonce}}">document.forms[0].submit()</script>
</body>
</html>`))

type paymentContinuationCopy struct {
	lang    string
	title   string
	message string
	button  string
}

func paymentContinuationLocale(acceptLanguage string) paymentContinuationCopy {
	value := strings.ToLower(strings.TrimSpace(acceptLanguage))
	copy := paymentContinuationCopy{lang: "en", title: "Continue payment", message: "Opening the secure payment page…", button: "Continue payment"}
	switch {
	case strings.HasPrefix(value, "zh-tw"), strings.HasPrefix(value, "zh-hk"), strings.HasPrefix(value, "zh-hant"):
		copy = paymentContinuationCopy{lang: "zh-Hant", title: "繼續付款", message: "正在開啟安全付款頁面…", button: "繼續付款"}
	case strings.HasPrefix(value, "zh"):
		copy = paymentContinuationCopy{lang: "zh-Hans", title: "继续支付", message: "正在打开安全支付页面…", button: "继续支付"}
	case strings.HasPrefix(value, "fr"):
		copy = paymentContinuationCopy{lang: "fr", title: "Continuer le paiement", message: "Ouverture de la page de paiement sécurisée…", button: "Continuer le paiement"}
	case strings.HasPrefix(value, "ja"):
		copy = paymentContinuationCopy{lang: "ja", title: "支払いを続ける", message: "安全な支払いページを開いています…", button: "支払いを続ける"}
	case strings.HasPrefix(value, "ru"):
		copy = paymentContinuationCopy{lang: "ru", title: "Продолжить оплату", message: "Открывается защищённая страница оплаты…", button: "Продолжить оплату"}
	case strings.HasPrefix(value, "vi"):
		copy = paymentContinuationCopy{lang: "vi", title: "Tiếp tục thanh toán", message: "Đang mở trang thanh toán an toàn…", button: "Tiếp tục thanh toán"}
	}
	return copy
}

// ContinuePayment keeps hosted URLs and signed form fields out of JSON APIs
// and the application DOM. It is an authenticated, no-store navigation target
// that either redirects or renders a minimal auto-submitting form.
func ContinuePayment(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" || len(tradeNo) > 128 {
		paymentAPIError(c, http.StatusBadRequest, "Invalid payment order")
		return
	}
	start, err := service.PaymentContinuation(c.GetInt("id"), tradeNo)
	if err != nil {
		paymentAPIError(c, http.StatusConflict, "Payment is not ready to continue")
		return
	}
	c.Header("Cache-Control", "no-store, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")

	if start.Flow == service.PaymentFlowAppRedirect {
		if start.Provider != model.PaymentProviderWaffo ||
			start.PaymentMethod != model.PaymentMethodWaffo ||
			service.ValidateConfiguredWaffoAppPaymentURL(start.URL) != nil {
			paymentAPIError(c, http.StatusConflict, "Payment is temporarily unavailable")
			return
		}
		c.Redirect(http.StatusSeeOther, start.URL)
		return
	}
	if start.Flow == service.PaymentFlowHostedRedirect {
		var validationErr error
		switch start.Provider {
		case model.PaymentProviderCreem:
			validationErr = service.ValidateCreemCheckoutURL(start.URL)
		case model.PaymentProviderWaffo:
			validationErr = service.ValidateConfiguredWaffoWebPaymentURL(start.URL)
		case model.PaymentProviderWaffoPancake:
			validationErr = service.ValidateWaffoPancakeCheckoutURL(start.URL)
		case model.PaymentProviderStripe:
			if syncErr := model.SyncPaymentConfigurationIfStale(); syncErr != nil {
				validationErr = syncErr
				break
			}
			unlockPaymentConfiguration := setting.LockPaymentConfigurationForRead()
			allowedHosts := setting.StripeCheckoutAllowedHosts
			unlockPaymentConfiguration()
			validationErr = service.ValidateStripeCheckoutURL(start.URL, allowedHosts)
		default:
			validationErr = service.ValidateExternalPaymentURL(start.URL, true)
		}
		if validationErr != nil {
			paymentAPIError(c, http.StatusConflict, "Payment is temporarily unavailable")
			return
		}
		c.Redirect(http.StatusSeeOther, start.URL)
		return
	}
	if start.Flow != service.PaymentFlowFormPost || start.Action == "" || len(start.Fields) == 0 {
		paymentAPIError(c, http.StatusConflict, "Payment is temporarily unavailable")
		return
	}
	if err := service.ValidateExternalPaymentURL(start.Action, true); err != nil {
		paymentAPIError(c, http.StatusConflict, "Payment is temporarily unavailable")
		return
	}
	actionURL, err := url.Parse(start.Action)
	if err != nil {
		paymentAPIError(c, http.StatusConflict, "Payment is temporarily unavailable")
		return
	}
	nonce, err := common.GenerateRandomCharsKey(24)
	if err != nil {
		paymentAPIError(c, http.StatusInternalServerError, "Payment is temporarily unavailable")
		return
	}
	fieldNames := make([]string, 0, len(start.Fields))
	for name := range start.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	fields := make([]paymentContinuationField, 0, len(fieldNames))
	for _, name := range fieldNames {
		fields = append(fields, paymentContinuationField{Name: name, Value: start.Fields[name]})
	}
	formOrigin := actionURL.Scheme + "://" + actionURL.Host
	c.Header("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action "+formOrigin+"; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'")
	c.Status(http.StatusOK)
	copy := paymentContinuationLocale(c.GetHeader("Accept-Language"))
	if err := paymentContinuationTemplate.Execute(c.Writer, paymentContinuationPage{
		Lang: copy.lang, Title: copy.title, Message: copy.message, Button: copy.button,
		Action: start.Action, Nonce: nonce, Fields: fields,
	}); err != nil {
		c.Error(err)
	}
}
