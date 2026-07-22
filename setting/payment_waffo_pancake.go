package setting

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	// WaffoPancakeTestMode binds newly quoted orders and accepted provider
	// evidence to the test environment. False preserves the historical
	// production default.
	WaffoPancakeTestMode  bool
	WaffoPancakeUnitPrice float64 = 1.0
	WaffoPancakeMinTopUp  int     = 1
	WaffoPancakeStoreID   string
	WaffoPancakeProductID string
)
