package setting

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const StripeWebhookSecretOverlap = 24 * time.Hour

const StripeTestModeEnabledEnv = "PAYMENT_STRIPE_TEST_MODE_ENABLED"

var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripeWebhookSecretPrevious = ""
var StripeWebhookSecretPreviousExpiresAt int64

// Generation 2 is the upgrade-safe implicit current generation. Installations
// predating persisted generations may still have an active previous secret;
// generation 1 is reserved for that legacy overlap so its creation cutoff can
// be enforced instead of treating it as unrestricted current credentials.
var StripeWebhookCredentialGeneration int64 = 2
var StripeWebhookPreviousCredentialGeneration int64
var StripeWebhookPreviousValidBefore int64

// StripePriceId is retained as the persisted option key for compatibility.
// Its only current meaning is a catalog template used to create a server-
// quoted one-time Checkout price; it is not a recurring subscription price.
var StripePriceId = ""
var StripeUnitPrice = 8.0
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false
var StripeCurrency = "USD"
var StripeAccountId = ""

// StripeCredentialAccountId is the platform/direct account resolved from the
// active API credential via Stripe's /v1/account endpoint. It prevents a key
// rotation from silently switching historical payments to another account.
var StripeCredentialAccountId = ""
var StripeCredentialLivemode = ""

// StripeWebhookCredentialLivemode binds the active and overlapping webhook
// signing secrets to the Stripe test/live domain in which they were issued.
// It is managed atomically with the secrets and is never client-writable.
var StripeWebhookCredentialLivemode = ""

// StripeConfigurationVerifiedFingerprint proves that the current API key,
// platform account, optional connected account, Price, currency, and custom
// Checkout host policy were successfully probed together. It contains only a
// one-way digest.
var StripeConfigurationVerifiedFingerprint = ""
var StripeConfigurationVerifiedAt int64

// StripeTestModeEnabled is intentionally environment-only so a database or
// dashboard mutation cannot turn a production instance into a payment
// sandbox. It defaults to false and must be set consistently on every node.
func StripeTestModeEnabled() bool {
	return common.GetEnvOrDefaultBool(StripeTestModeEnabledEnv, false)
}

func StripeCredentialModeAllowed(mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	return mode == "live" || mode == "test" && StripeTestModeEnabled()
}

func StripePreviousWebhookSecretActive() bool {
	return StripeWebhookSecretPrevious != "" && StripeWebhookSecretPreviousExpiresAt > time.Now().Unix()
}

func StripeCheckoutPriceTemplateID() string {
	return StripePriceId
}
